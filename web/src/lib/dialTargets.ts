// Host-literal classifier mirroring internal/egress/devicedeny.go.
// Drift is guarded by internal/egress/testdata/devicedeny-vectors.json (read by the test only).

export type DialClass = 'deny' | 'lan' | 'allow';

type Bytes = number[]; // 16 bytes, IPv4 as v4-mapped

function parseV4(s: string): number[] | null {
  const parts = s.split('.');
  if (parts.length !== 4) return null;
  const out: number[] = [];
  for (const p of parts) {
    if (!/^\d{1,3}$/.test(p)) return null;
    const n = Number(p);
    if (n > 255) return null;
    out.push(n);
  }
  return out;
}

function parseV6(s: string): Bytes | null {
  if (!s.includes(':')) return null;
  let tail: number[] = [];
  let str = s;
  const lastColon = str.lastIndexOf(':');
  if (str.slice(lastColon + 1).includes('.')) {
    const v4 = parseV4(str.slice(lastColon + 1));
    if (!v4) return null;
    tail = v4;
    str = str.slice(0, lastColon + 1) + '0:0';
  }
  const halves = str.split('::');
  if (halves.length > 2) return null;
  const groups = (h: string): number[] | null => {
    if (h === '') return [];
    const out: number[] = [];
    for (const g of h.split(':')) {
      if (!/^[0-9a-fA-F]{1,4}$/.test(g)) return null;
      out.push(parseInt(g, 16));
    }
    return out;
  };
  const head = groups(halves[0]);
  const rest = halves.length === 2 ? groups(halves[1]) : [];
  if (!head || !rest) return null;
  let words: number[];
  if (halves.length === 2) {
    const fill = 8 - head.length - rest.length;
    if (fill < 1) return null;
    words = [...head, ...new Array<number>(fill).fill(0), ...rest];
  } else {
    if (head.length !== 8) return null;
    words = head;
  }
  const out: Bytes = [];
  for (const w of words) out.push(w >> 8, w & 255);
  if (tail.length) out.splice(12, 4, ...tail);
  return out;
}

interface Parsed {
  b: Bytes;
  zone: boolean;
}

/** Parses an IP literal (optionally bracketed, optionally with an IPv6 zone). */
function parseIP(host: string): Parsed | null {
  let h = host.trim();
  if (h.startsWith('[') && h.endsWith(']')) h = h.slice(1, -1);
  let zone = false;
  const pct = h.indexOf('%');
  if (pct >= 0) {
    zone = true;
    h = h.slice(0, pct);
  }
  const v4 = parseV4(h);
  if (v4) return zone ? null : { b: [0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 255, 255, ...v4], zone };
  const v6 = parseV6(h);
  return v6 ? { b: v6, zone } : null;
}

type Cidr = [Bytes, number];

function cidr(s: string): Cidr {
  const [a, n] = s.split('/');
  const p = parseIP(a)!;
  return [p.b, Number(n) + (a.includes(':') ? 0 : 96)];
}

function inCidr(b: Bytes, [net, bits]: Cidr): boolean {
  for (let i = 0; i < bits; i++) {
    const mask = 0x80 >> (i & 7);
    if ((b[i >> 3] & mask) !== (net[i >> 3] & mask)) return false;
  }
  return true;
}

const any = (b: Bytes, t: Cidr[]) => t.some((c) => inCidr(b, c));

// Same rows as alwaysDeny / lanClass in devicedeny.go.
const ALWAYS = [
  '0.0.0.0/8', '::/128', '127.0.0.0/8', '::1/128', '169.254.0.0/16', 'fe80::/10', 'fec0::/10',
  '224.0.0.0/4', 'ff00::/8', '240.0.0.0/4', '255.255.255.255/32', '::/96', '100::/64',
  '192.0.2.0/24', '198.51.100.0/24', '203.0.113.0/24', '2001:db8::/32', '3fff::/20',
  '2002::/16', '2001::/32', 'fd00:ec2::254/128', '100.100.100.200/32',
].map(cidr);
const LAN = ['10.0.0.0/8', '172.16.0.0/12', '192.168.0.0/16', 'fc00::/7', '100.64.0.0/10', '192.0.0.0/24', '198.18.0.0/15', '64:ff9b:1::/48'].map(cidr);
const NAT64 = cidr('64:ff9b::/96');
// Warning subset (shown as warnings): loopback, unspecified, link-local, multicast, metadata pins.
const WARN = [
  '0.0.0.0/8', '::/128', '127.0.0.0/8', '::1/128', '169.254.0.0/16', 'fe80::/10', 'fec0::/10',
  '224.0.0.0/4', 'ff00::/8', 'fd00:ec2::254/128', '100.100.100.200/32',
].map(cidr);
const COMPAT = cidr('::/96');

/** ::ffff:a.b.c.d is stored as-is (IPv4 rows use the mapped prefix), so no unmap is needed. */
function embeddedV4(b: Bytes): Bytes {
  return [0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 255, 255, b[12], b[13], b[14], b[15]];
}

function classifyBytes(b: Bytes): DialClass {
  if (any(b, ALWAYS)) return 'deny';
  return any(b, LAN) ? 'lan' : 'allow';
}

function localName(host: string): boolean {
  const n = host.trim().toLowerCase().replace(/\.$/, '');
  return n === 'localhost' || n.endsWith('.localhost');
}

/** Classifies a host the way the device route would: deny / lan / allow. */
export function classify(addr: string): DialClass {
  const p = parseIP(addr);
  if (!p) return addr.includes('%') || localName(addr) ? 'deny' : 'allow';
  if (p.zone) return 'deny';
  if (inCidr(p.b, NAT64) && classifyBytes(embeddedV4(p.b)) !== 'allow') return 'deny';
  return classifyBytes(p.b);
}

/**
 * Proxy-form warning: loopback, unspecified, link-local, multicast and metadata hosts.
 * Documentation and reserved ranges are deliberately not warned about. Zones are ignored.
 */
export function isSensitiveHost(host: string): boolean {
  const p = parseIP(host.includes('%') ? host.replace(/%[^\]]*/, '') : host);
  if (!p) return localName(host);
  if ((inCidr(p.b, NAT64) || inCidr(p.b, COMPAT)) && any(embeddedV4(p.b), WARN)) return true;
  return any(p.b, WARN);
}
