// Bundle-size report: gzip every file in dist (node:zlib), print per-file sizes and the
// gzipped total. Exits 1 when the total reaches the 200 KB budget (frontend rule 3).
import { readdirSync, readFileSync } from 'node:fs';
import { join, relative } from 'node:path';
import { fileURLToPath } from 'node:url';
import { gzipSync } from 'node:zlib';

const LIMIT_KB = 200;
const dist = fileURLToPath(new URL('../dist', import.meta.url));

function walk(dir) {
  return readdirSync(dir, { withFileTypes: true }).flatMap((d) =>
    d.isDirectory() ? walk(join(dir, d.name)) : [join(dir, d.name)],
  );
}

const kb = (n) => (n / 1024).toFixed(2);
let total = 0;
for (const file of walk(dist).sort()) {
  const gz = gzipSync(readFileSync(file), { level: 9 }).length;
  total += gz;
  console.log(`  ${relative(dist, file).padEnd(40)} ${kb(gz).padStart(8)} KB gz`);
}
console.log(`gzip total: ${kb(total)} KB (limit ${LIMIT_KB} KB)`);
if (total >= LIMIT_KB * 1024) {
  console.error(`bundle too large: ${kb(total)} KB gz >= ${LIMIT_KB} KB`);
  process.exit(1);
}
