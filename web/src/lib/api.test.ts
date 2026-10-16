import { beforeEach, describe, expect, it, vi } from 'vitest';
import { ApiError, createApi, type Api } from './api';
import { createSession, type Session } from './session';

type Call = { url: string; init: RequestInit };

function json(status: number, body: unknown): Response {
  return new Response(body === undefined ? null : JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

let calls: Call[];
let replies: Response[];
let navigate: ReturnType<typeof vi.fn<(hash: string) => void>>;
let session: Session;
let api: Api;

async function failure(p: Promise<unknown>): Promise<ApiError> {
  try {
    await p;
  } catch (e) {
    if (e instanceof ApiError) return e;
    throw e;
  }
  throw new Error('expected the request to fail');
}

function headersOf(c: Call): Headers {
  return new Headers(c.init.headers);
}

beforeEach(() => {
  calls = [];
  replies = [];
  navigate = vi.fn<(hash: string) => void>();
  session = createSession(null); // in-memory storage
  const fetchStub = vi.fn(async (url: RequestInfo | URL, init?: RequestInit) => {
    calls.push({ url: String(url), init: init ?? {} });
    return replies.shift() ?? json(204, undefined);
  });
  api = createApi({ fetch: fetchStub as typeof fetch, session, navigate });
});

describe('CSRF header', () => {
  it('stores the token from login and sends it on every POST/PUT/DELETE', async () => {
    replies.push(json(200, { csrf_token: 'tok-1' }));
    await api.login('hunter22');
    expect(session.token()).toBe('tok-1');
    expect(headersOf(calls[0]).has('X-CSRF-Token')).toBe(false);

    await api.request('POST', '/api/slots/1/start');
    await api.request('PUT', '/api/settings', { ui_bind_addr: '0.0.0.0:8787' });
    await api.request('DELETE', '/api/endpoints/x');
    for (const c of calls.slice(1)) {
      expect(headersOf(c).get('X-CSRF-Token')).toBe('tok-1');
      expect(c.init.credentials).toBe('same-origin');
    }
    expect(calls.slice(1).map((c) => c.init.method)).toEqual(['POST', 'PUT', 'DELETE']);
  });

  it('uses the token from the most recent login', async () => {
    replies.push(json(200, { csrf_token: 'old' }), json(200, { csrf_token: 'new' }));
    await api.login('pw-one-1');
    await api.login('pw-two-2');
    await api.request('POST', '/api/logout');
    expect(headersOf(calls[2]).get('X-CSRF-Token')).toBe('new');
  });

  it('GET sends no CSRF header', async () => {
    session.setToken('tok-1');
    replies.push(json(200, { ui_bind_addr: 'a', config_error: '' }));
    await api.getSettings();
    expect(calls[0].init.method).toBe('GET');
    expect(headersOf(calls[0]).has('X-CSRF-Token')).toBe(false);
  });

  it('a mutating call without a token routes to login without hitting the network', async () => {
    await expect(api.request('POST', '/api/slots/1/start')).rejects.toBeInstanceOf(ApiError);
    expect(calls).toHaveLength(0);
    expect(navigate).toHaveBeenCalledWith('#/login');
  });

  it('a 403 clears the token and routes to login', async () => {
    session.setToken('stale');
    replies.push(json(403, { error: 'missing or invalid CSRF token' }));
    await expect(api.request('POST', '/api/slots/1/stop')).rejects.toMatchObject({ status: 403 });
    expect(session.token()).toBeNull();
    expect(navigate).toHaveBeenCalledWith('#/login');
  });
});

describe('401 handling', () => {
  for (const [method, path] of [
    ['GET', '/api/slots'],
    ['POST', '/api/slots/1/start'],
    ['PUT', '/api/slots/1'],
    ['DELETE', '/api/endpoints/x'],
  ] as const) {
    it(`${method} ${path}: clears the token and routes to #/login`, async () => {
      session.setToken('tok-1');
      replies.push(json(401, { error: 'login required' }));
      const err = await failure(api.request(method, path, method === 'PUT' ? {} : undefined));
      expect(err).toBeInstanceOf(ApiError);
      expect(err.status).toBe(401);
      expect(session.token()).toBeNull();
      expect(navigate).toHaveBeenCalledWith('#/login');
    });
  }

  it('a 401 from login (wrong password) surfaces the message', async () => {
    replies.push(json(401, { error: 'wrong password' }));
    await expect(api.login('bad-pass')).rejects.toMatchObject({ status: 401, message: 'wrong password' });
    expect(session.token()).toBeNull();
  });

  it('a 503 from login (hash queue full) surfaces "busy, retry shortly" without a redirect', async () => {
    session.setToken('tok-1');
    replies.push(
      new Response(JSON.stringify({ error: 'busy, retry shortly' }), {
        status: 503,
        headers: { 'Content-Type': 'application/json', 'Retry-After': '1' },
      }),
    );
    const err = await failure(api.login('correct-horse-1'));
    expect(err).toBeInstanceOf(ApiError);
    expect(err).toMatchObject({ status: 503, message: 'busy, retry shortly', retryAfter: 1 });
    expect(navigate).not.toHaveBeenCalled();
    expect(session.token()).toBe('tok-1');
  });
});

describe('field errors', () => {
  it('returns a 400 body as typed {field, message}[]', async () => {
    session.setToken('tok-1');
    replies.push(
      json(400, {
        error: 'invalid input',
        errors: [
          { field: 'bind_port', message: 'must be 1-65535' },
          { field: 'route[0]', message: 'unknown endpoint' },
        ],
      }),
    );
    const err = await failure(api.request('PUT', '/api/slots/1', {}));
    expect(err).toBeInstanceOf(ApiError);
    expect(err.status).toBe(400);
    expect(err.message).toBe('invalid input');
    expect(err.fieldErrors).toEqual([
      { field: 'bind_port', message: 'must be 1-65535' },
      { field: 'route[0]', message: 'unknown endpoint' },
    ]);
    expect(navigate).not.toHaveBeenCalled();
  });

  it('drops malformed field error entries and tolerates non-JSON bodies', async () => {
    session.setToken('tok-1');
    replies.push(json(400, { error: 'invalid input', errors: [{ field: 1 }, { field: 'a', message: 'b' }] }));
    const err = await failure(api.request('PUT', '/api/slots/1', {}));
    expect(err.fieldErrors).toEqual([{ field: 'a', message: 'b' }]);

    replies.push(new Response('oops', { status: 500 }));
    const err2 = await failure(api.request('GET', '/api/slots'));
    expect(err2).toBeInstanceOf(ApiError);
    expect(err2.status).toBe(500);
    expect(err2.fieldErrors).toEqual([]);
  });
});

describe('setup flow', () => {
  it('a 409 "setup required" from POST /api/login routes to the setup form', async () => {
    replies.push(json(409, { error: 'setup required' }));
    await expect(api.login('whatever1')).rejects.toMatchObject({ status: 409 });
    expect(navigate).toHaveBeenCalledWith('#/setup');
  });

  it('setup posts the password without a CSRF header', async () => {
    replies.push(json(204, undefined));
    await api.setup('correct horse');
    expect(calls[0].url).toBe('/api/setup');
    expect(calls[0].init.method).toBe('POST');
    expect(JSON.parse(String(calls[0].init.body))).toEqual({ password: 'correct horse' });
    expect(headersOf(calls[0]).has('X-CSRF-Token')).toBe(false);
  });

  it('logout clears the token even on success', async () => {
    session.setToken('tok-1');
    replies.push(json(204, undefined));
    await api.logout();
    expect(session.token()).toBeNull();
    expect(navigate).toHaveBeenCalledWith('#/login');
  });
});

describe('session storage', () => {
  it('survives a throwing storage', () => {
    const bad = {
      getItem: () => {
        throw new Error('blocked');
      },
      setItem: () => {
        throw new Error('blocked');
      },
      removeItem: () => {
        throw new Error('blocked');
      },
    } as unknown as Storage;
    const s = createSession(bad);
    expect(s.token()).toBeNull();
    s.setToken('x');
    expect(s.token()).toBe('x');
    s.clear();
    expect(s.token()).toBeNull();
  });

  it('reads a token persisted by an earlier page load', () => {
    const map = new Map<string, string>();
    const store = {
      getItem: (k: string) => map.get(k) ?? null,
      setItem: (k: string, v: string) => void map.set(k, v),
      removeItem: (k: string) => void map.delete(k),
    } as unknown as Storage;
    createSession(store).setToken('persisted');
    expect(createSession(store).token()).toBe('persisted');
  });
});

describe('multipart upload', () => {
  it('passes FormData through untouched with CSRF and no JSON content type', async () => {
    session.setToken('tok-9');
    const form = new FormData();
    form.append('files', new File(['x'], 'a.conf'));
    replies.push(json(200, { results: [] }));
    await api.request('POST', '/api/endpoints/import', form);
    const c = calls[0];
    expect(c.init.body).toBe(form);
    expect(headersOf(c).get('X-CSRF-Token')).toBe('tok-9');
    expect(headersOf(c).has('Content-Type')).toBe(false);
  });

  it('rotateEndpoint posts with CSRF and parses Retry-After on 429', async () => {
    session.setToken('tok');
    replies.push(json(200, { current_id: 'abc', rebuilt_slots: [1] }));
    expect(await api.rotateEndpoint('a b')).toEqual({ current_id: 'abc', rebuilt_slots: [1] });
    expect(calls[0]!.url).toBe('/api/endpoints/a%20b/rotate');
    expect(calls[0]!.init.method).toBe('POST');
    replies.push(new Response('{"error":"slow down"}', { status: 429, headers: { 'Retry-After': '4' } }));
    const err = await failure(api.rotateEndpoint('x'));
    expect(err.status).toBe(429);
    expect(err.retryAfter).toBe(4);
  });
});

describe('exportConfig / importConfig', () => {
  const file = (status: number, headers: Record<string, string> = {}) =>
    new Response('{"mode":"encrypted"}', { status, headers });

  it('exportConfig POSTs JSON with CSRF and returns blob + filename', async () => {
    session.setToken('tok-x');
    replies.push(file(200, { 'Content-Disposition': 'attachment; filename="sockswebui-a.json"' }));
    const out = await api.exportConfig({ current_password: 'pw', mode: 'encrypted', passphrase: 'a'.repeat(12) });
    expect(calls[0].url).toBe('/api/config/export');
    expect(calls[0].init.method).toBe('POST');
    expect(headersOf(calls[0]).get('X-CSRF-Token')).toBe('tok-x');
    expect(JSON.parse(String(calls[0].init.body)).mode).toBe('encrypted');
    expect(out.filename).toBe('sockswebui-a.json');
    expect(await out.blob.text()).toBe('{"mode":"encrypted"}');
  });

  it('surfaces current_password field errors', async () => {
    session.setToken('tok-x');
    replies.push(json(400, { error: 'invalid', errors: [{ field: 'current_password', message: 'is incorrect' }] }));
    const e = await failure(api.exportConfig({ current_password: 'bad', mode: 'no-secrets' }));
    expect(e.status).toBe(400);
    expect(e.fieldErrors).toEqual([{ field: 'current_password', message: 'is incorrect' }]);
  });

  it('429 carries Retry-After', async () => {
    session.setToken('tok-x');
    replies.push(new Response('{"error":"too many"}', { status: 429, headers: { 'Retry-After': '30' } }));
    const e = await failure(api.exportConfig({ current_password: 'bad', mode: 'no-secrets' }));
    expect(e.status).toBe(429);
    expect(e.retryAfter).toBe(30);
  });

  it('importConfig sends multipart with one file part, password and optional passphrase', async () => {
    session.setToken('tok-x');
    replies.push(json(200, { endpoints: 1, slots: 1, secrets_missing: { endpoints: [], slots: [] }, backup: 'b' }));
    const f = new File(['{}'], 'c.json');
    await api.importConfig(f, 'pw', 'pass-phrase-123');
    const body = calls[0].init.body as FormData;
    expect(body).toBeInstanceOf(FormData);
    expect(body.getAll('file')).toHaveLength(1);
    expect(body.get('current_password')).toBe('pw');
    expect(body.get('passphrase')).toBe('pass-phrase-123');
    expect(headersOf(calls[0]).get('X-CSRF-Token')).toBe('tok-x');
    replies.push(json(200, {}));
    await api.importConfig(f, 'pw');
    expect((calls[1].init.body as FormData).has('passphrase')).toBe(false);
  });
});
