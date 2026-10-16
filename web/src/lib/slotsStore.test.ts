import { describe, expect, it } from 'vitest';
import { applyStatus, formatRate, initialSlots, reduce, seedFromList, type SlotsState } from './slotsStore';

// Frame payloads captured from a real /api/events stream.
const SNAPSHOT = JSON.parse(
  '{"slots":[{"id":1,"name":"rig","enabled":false,"state":"STOPPED","msg":"","bytes_in":0,"bytes_out":0}]}',
);
const STATE_STARTING = JSON.parse(
  '{"type":"state","slot":1,"state":"STARTING","msg":"starting","ts":"2026-10-04T03:04:23.710441997Z"}',
);
const HEALTH_OK = JSON.parse(
  '{"type":"health","slot":1,"health":{"ok":true,"exit_ip":"10.64.0.2","latency_ms":5},"ts":"2026-10-04T03:04:23.717075457Z"}',
);
const STATE_UP = JSON.parse(
  '{"type":"state","slot":1,"state":"UP","msg":"probe ok: exit 10.64.0.2","ts":"2026-10-04T03:04:23.717100926Z"}',
);
const THROUGHPUT = JSON.parse(
  '{"type":"throughput","slot":1,"throughput":{"bytes_in":0,"bytes_out":0,"rate_in":0,"rate_out":0},"ts":"2026-10-04T03:04:24.121551438Z"}',
);
const HEALTH_FAIL = JSON.parse(
  '{"type":"health","slot":1,"health":{"ok":false},"ts":"2026-10-04T03:04:31.723563429Z"}',
);
const STATE_DEGRADED = JSON.parse(
  '{"type":"state","slot":1,"state":"DEGRADED","msg":"kill switch: 2 consecutive probe failures: health: probe: Get \\"http://echo.rig.invalid/\\": context deadline exceeded","ts":"2026-10-04T03:04:35.725460855Z"}',
);

function feed(s: SlotsState, frames: [string, unknown][]): SlotsState {
  return frames.reduce((acc, [ev, data]) => reduce(acc, ev, data), s);
}

describe('slotsStore reducer', () => {
  it('starts with 4 slots and no known state', () => {
    const s = initialSlots();
    expect(Object.keys(s).map(Number)).toEqual([1, 2, 3, 4]);
    for (const id of [1, 2, 3, 4]) expect(s[id].state).toBeNull();
  });

  it('snapshot then state, health, throughput yields the live fields', () => {
    const s = feed(initialSlots(), [
      ['snapshot', SNAPSHOT],
      ['state', STATE_STARTING],
      ['health', HEALTH_OK],
      ['state', STATE_UP],
      ['throughput', THROUGHPUT],
    ]);
    expect(s[1]).toMatchObject({
      state: 'UP',
      msg: 'probe ok: exit 10.64.0.2',
      exit_ip: '10.64.0.2',
      country: '',
      latency_ms: 5,
      rx_rate: 0,
      tx_rate: 0,
    });
    expect(s[2].state).toBeNull();
  });

  it('snapshot sets state and msg', () => {
    const s = reduce(initialSlots(), 'snapshot', SNAPSHOT);
    expect(s[1]).toMatchObject({ state: 'STOPPED', msg: '', name: 'rig' });
  });

  it('throughput rates map to rx/tx', () => {
    const frame = { ...THROUGHPUT, throughput: { bytes_in: 10, bytes_out: 20, rate_in: 1500, rate_out: 300 } };
    const s = feed(initialSlots(), [['snapshot', SNAPSHOT], ['throughput', frame]]);
    expect(s[1]).toMatchObject({ rx_rate: 1500, tx_rate: 300, bytes_in: 10, bytes_out: 20 });
  });

  it('health with country records it; a failed probe clears exit ip and latency', () => {
    const withCountry = { ...HEALTH_OK, health: { ok: true, exit_ip: '1.2.3.4', country: 'Sweden', latency_ms: 40 } };
    let s = feed(initialSlots(), [['snapshot', SNAPSHOT], ['health', withCountry]]);
    expect(s[1]).toMatchObject({ health_ok: true, exit_ip: '1.2.3.4', country: 'Sweden', latency_ms: 40 });
    s = feed(s, [['health', HEALTH_FAIL], ['state', STATE_DEGRADED]]);
    expect(s[1]).toMatchObject({ health_ok: false, exit_ip: '', country: '', latency_ms: null, state: 'DEGRADED' });
    expect(s[1].msg).toContain('kill switch');
  });

  it('ignores frames for unknown slots and malformed frames', () => {
    const base = reduce(initialSlots(), 'snapshot', SNAPSHOT);
    for (const [ev, data] of [
      ['state', { ...STATE_UP, slot: 9 }],
      ['state', { ...STATE_UP, slot: 0 }],
      ['health', { ...HEALTH_OK, slot: 7 }],
      ['throughput', { ...THROUGHPUT, slot: -1 }],
      ['state', { ...STATE_UP, state: 'BOGUS' }],
      ['state', null],
      ['state', 'nope'],
      ['mystery', STATE_UP],
    ] as [string, unknown][]) {
      expect(reduce(base, ev, data)).toEqual(base);
    }
  });

  it('a later snapshot replaces state (reconnect)', () => {
    let s = feed(initialSlots(), [['snapshot', SNAPSHOT], ['state', STATE_UP], ['health', HEALTH_OK]]);
    s = reduce(s, 'snapshot', { slots: [{ id: 2, name: 'b', enabled: true, state: 'ERROR', msg: 'bad key' }] });
    expect(s[1].state).toBeNull();
    expect(s[1].exit_ip).toBe('');
    expect(s[2]).toMatchObject({ state: 'ERROR', msg: 'bad key' });
  });

  it('applyStatus takes state only from an API response, never invents UP', () => {
    const s = reduce(initialSlots(), 'snapshot', SNAPSHOT);
    const after = applyStatus(s, { id: 1, name: 'rig', enabled: true, state: 'STARTING', msg: 'starting' });
    expect(after[1].state).toBe('STARTING');
    expect(applyStatus(s, { id: 1 })).toEqual(s); // no state in body: unchanged
    expect(applyStatus(s, { id: 8, state: 'UP' })).toEqual(s);
  });

  it('SSE wins: a state frame that arrived during the POST beats the stale response', () => {
    let s = feed(initialSlots(), [['snapshot', SNAPSHOT]]);
    const seen = s[1].seq; // recorded before POST /start
    s = feed(s, [['state', STATE_STARTING], ['state', STATE_UP]]);
    s = applyStatus(s, { id: 1, name: 'rig', enabled: true, state: 'STARTING', msg: 'starting' }, seen);
    expect(s[1].state).toBe('UP');
  });

  it('a snapshot during the POST also beats the stale response', () => {
    let s = feed(initialSlots(), [['snapshot', SNAPSHOT]]);
    const seen = s[1].seq;
    s = reduce(s, 'snapshot', { slots: [{ id: 1, name: 'rig', enabled: true, state: 'UP', msg: '' }] });
    s = applyStatus(s, { id: 1, state: 'STARTING', msg: 'starting' }, seen);
    expect(s[1].state).toBe('UP');
  });

  it('the response applies when no state frame arrived since the POST', () => {
    let s = feed(initialSlots(), [['snapshot', SNAPSHOT]]);
    const seen = s[1].seq;
    s = feed(s, [['throughput', THROUGHPUT], ['health', HEALTH_OK]]);
    s = applyStatus(s, { id: 1, state: 'STARTING', msg: 'starting' }, seen);
    expect(s[1].state).toBe('STARTING');
  });

  it('does not mutate the previous state', () => {
    const s = reduce(initialSlots(), 'snapshot', SNAPSHOT);
    const copy = structuredClone(s);
    reduce(s, 'state', STATE_UP);
    expect(s).toEqual(copy);
  });
});

describe('formatRate', () => {
  it('formats bytes per second', () => {
    expect(formatRate(0)).toBe('0 B/s');
    expect(formatRate(1500)).toBe('1.5 KB/s');
    expect(formatRate(2_500_000)).toBe('2.5 MB/s');
  });
});

describe('status seeding after reload', () => {
  const STATUS = {
    state: 'UP',
    exit_ip: '203.0.113.9',
    country: 'SE',
    latency_ms: 42,
    last_error: null,
    since: '2026-10-14T01:00:00Z',
    checked_at: '2026-10-14T01:00:05Z',
  };
  const snap = (status: unknown) => ({
    slots: [{ id: 1, name: 'rig', enabled: true, state: 'UP', msg: '', bytes_in: 0, bytes_out: 0, status }],
  });

  it('snapshot seeds exit, country and latency', () => {
    const s = reduce(initialSlots(), 'snapshot', snap(STATUS));
    expect(s[1].exit_ip).toBe('203.0.113.9');
    expect(s[1].country).toBe('SE');
    expect(s[1].latency_ms).toBe(42);
    expect(s[1].last_error).toBe('');
  });

  it('null status fields stay empty and a missing status is tolerated', () => {
    const nul = { state: 'STOPPED', exit_ip: null, country: null, latency_ms: null, last_error: null };
    let s = reduce(initialSlots(), 'snapshot', snap(nul));
    expect([s[1].exit_ip, s[1].country, s[1].latency_ms]).toEqual(['', '', null]);
    s = reduce(initialSlots(), 'snapshot', snap(undefined));
    expect(s[1].exit_ip).toBe('');
  });

  it('a later health frame overrides the seed', () => {
    let s = reduce(initialSlots(), 'snapshot', snap(STATUS));
    s = reduce(s, 'health', { slot: 1, health: { ok: true, exit_ip: '198.51.100.1', country: 'NO', latency_ms: 7 } });
    expect([s[1].exit_ip, s[1].country, s[1].latency_ms]).toEqual(['198.51.100.1', 'NO', 7]);
  });

  it('keeps last_error from the snapshot', () => {
    const s = reduce(initialSlots(), 'snapshot', snap({ ...STATUS, state: 'DEGRADED', last_error: 'probe: timeout' }));
    expect(s[1].last_error).toBe('probe: timeout');
  });

  it('seedFromList seeds slots that have no live data and never overrides SSE data', () => {
    const list = [
      { id: 1, status: STATUS },
      { id: 2, status: { ...STATUS, exit_ip: '192.0.2.2' } },
    ];
    let s = reduce(initialSlots(), 'health', { slot: 2, health: { ok: true, exit_ip: '10.0.0.2', latency_ms: 1 } });
    s = seedFromList(s, list);
    expect(s[1].exit_ip).toBe('203.0.113.9');
    expect(s[2].exit_ip).toBe('10.0.0.2');
    expect(s[1].state).toBeNull(); // state still comes only from SSE / action responses
  });
});
