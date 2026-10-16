// Builds the UI and the -tags testrig binary, starts it on a free port with a fresh
// data dir, and tears it down afterwards. The base URL is passed via env to the spec.
import { spawn, spawnSync, type ChildProcess } from 'node:child_process';
import { mkdtempSync, rmSync } from 'node:fs';
import { createServer } from 'node:net';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';

let child: ChildProcess | undefined;
let tmp = '';

function freePort(): Promise<number> {
  return new Promise((res, rej) => {
    const s = createServer();
    s.listen(0, '127.0.0.1', () => {
      const p = (s.address() as { port: number }).port;
      s.close(() => res(p));
    });
    s.on('error', rej);
  });
}

function run(cmd: string, args: string[], cwd: string) {
  const r = spawnSync(cmd, args, { cwd, stdio: 'inherit', env: { ...process.env, CGO_ENABLED: '0' } });
  if (r.status !== 0) throw new Error(`${cmd} ${args.join(' ')} failed (${r.status})`);
}

export default async function globalSetup() {
  const web = resolve(import.meta.dirname, '..');
  const root = resolve(web, '..');
  tmp = mkdtempSync(join(tmpdir(), 'swt-'));
  run('npm', ['run', 'build'], web); // the binary embeds web/dist
  const bin = join(tmp, 'swt');
  run('go', ['build', '-tags', 'testrig', '-o', bin, './cmd/sockswebui'], root);
  const port = await freePort();
  const addr = `127.0.0.1:${port}`;
  // SWT_RIG_SLOTS seeds more rig slots (readme-shots uses 4); default 1 as before.
  const extra = process.env.SWT_RIG_SLOTS ? ['--dev-testrig-slots', process.env.SWT_RIG_SLOTS] : [];
  child = spawn(bin, ['--dev-testrig', ...extra, '--data-dir', join(tmp, 'data'), '--ui-addr', addr], {
    stdio: ['ignore', 'inherit', 'inherit'],
  });
  const base = `http://${addr}`;
  const deadline = Date.now() + 30_000;
  for (;;) {
    if (child.exitCode !== null) throw new Error('testrig binary exited early');
    try {
      if ((await fetch(base + '/')).ok) break;
    } catch {
      /* not up yet */
    }
    if (Date.now() > deadline) throw new Error('testrig binary did not start');
    await new Promise((r) => setTimeout(r, 200));
  }
  process.env.SMOKE_BASE_URL = base;
  process.env.SMOKE_SESSION_DIR = tmp; // mkdtemp, 0700; the session cache lives and dies with it
  return async () => {
    child?.kill('SIGTERM');
    await new Promise((r) => setTimeout(r, 300));
    if (child && child.exitCode === null) child.kill('SIGKILL');
    rmSync(tmp, { recursive: true, force: true });
  };
}
