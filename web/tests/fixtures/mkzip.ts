// Tiny stored (uncompressed) zip writer plus Mullvad-style .conf generator. Keys are
// freshly generated fake keys at test time, so no key material is ever committed.
import { randomBytes } from 'node:crypto';

const TABLE = (() => {
  const t = new Uint32Array(256);
  for (let n = 0; n < 256; n++) {
    let c = n;
    for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
    t[n] = c >>> 0;
  }
  return t;
})();

export function crc32(buf: Uint8Array): number {
  let c = 0xffffffff;
  for (const b of buf) c = TABLE[(c ^ b) & 0xff]! ^ (c >>> 8);
  return (c ^ 0xffffffff) >>> 0;
}

export function makeZip(files: { name: string; data: string }[]): Buffer {
  const parts: Buffer[] = [];
  const central: Buffer[] = [];
  let offset = 0;
  for (const f of files) {
    const name = Buffer.from(f.name);
    const data = Buffer.from(f.data);
    const crc = crc32(data);
    const lh = Buffer.alloc(30);
    lh.writeUInt32LE(0x04034b50, 0);
    lh.writeUInt16LE(20, 4);
    lh.writeUInt32LE(crc, 14);
    lh.writeUInt32LE(data.length, 18);
    lh.writeUInt32LE(data.length, 22);
    lh.writeUInt16LE(name.length, 26);
    parts.push(lh, name, data);
    const ch = Buffer.alloc(46);
    ch.writeUInt32LE(0x02014b50, 0);
    ch.writeUInt16LE(20, 4);
    ch.writeUInt16LE(20, 6);
    ch.writeUInt32LE(crc, 16);
    ch.writeUInt32LE(data.length, 20);
    ch.writeUInt32LE(data.length, 24);
    ch.writeUInt16LE(name.length, 28);
    ch.writeUInt32LE(offset, 42);
    central.push(ch, name);
    offset += lh.length + name.length + data.length;
  }
  const cd = Buffer.concat(central);
  const end = Buffer.alloc(22);
  end.writeUInt32LE(0x06054b50, 0);
  end.writeUInt16LE(files.length, 8);
  end.writeUInt16LE(files.length, 10);
  end.writeUInt32LE(cd.length, 12);
  end.writeUInt32LE(offset, 16);
  return Buffer.concat([...parts, cd, end]);
}

const key = () => randomBytes(32).toString('base64');

export function mullvadConf(endpointIP: string): string {
  return [
    '[Interface]',
    `PrivateKey = ${key()}`,
    'Address = 10.64.0.2/32, fc00:bbbb:bbbb:bb01::2/128',
    'DNS = 10.64.0.1',
    '',
    '[Peer]',
    `PublicKey = ${key()}`,
    `PresharedKey = ${key()}`,
    'AllowedIPs = 0.0.0.0/0, ::/0',
    `Endpoint = ${endpointIP}:51820`,
    '',
  ].join('\n');
}

export function mullvadZip(): Buffer {
  return makeZip([
    { name: 'se-got-wg-001.conf', data: mullvadConf('198.51.100.10') },
    { name: 'se-got-wg-002.conf', data: mullvadConf('198.51.100.11') },
  ]);
}
