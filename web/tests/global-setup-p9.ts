// Round-trip setup: builds the UI and one -tags testrig binary, then starts two independent
// --dev-testrig instances (own data dir, own free UI port). Base URLs go to the check
// via P9_BASE_A / P9_BASE_B. Both are torn down and the temp dir removed afterwards.
import { spawn, spawnSync, type ChildProcess } from 'node:child_process';
import { mkdtempSync, rmSync } from 'node:fs';
import { createServer } from 'node:net';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';

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

async function waitUp(child: ChildProcess, base: string) {
  const deadline = Date.now() + 30_000;
  for (;;) {
    if (child.exitCode !== null) throw new Error('testrig binary exited early');
    try {
      if ((await fetch(base + '/')).ok) return;
    } catch {
      /* not up yet */
    }
    if (Date.now() > deadline) throw new Error('testrig binary did not start');
    await new Promise((r) => setTimeout(r, 200));
  }
}

export default async function globalSetup() {
  const web = resolve(import.meta.dirname, '..');
  const root = resolve(web, '..');
  const tmp = mkdtempSync(join(tmpdir(), 'swt9-'));
  const children: ChildProcess[] = [];
  const teardown = async () => {
    for (const c of children) c.kill('SIGTERM');
    await new Promise((r) => setTimeout(r, 300));
    for (const c of children) if (c.exitCode === null) c.kill('SIGKILL');
    rmSync(tmp, { recursive: true, force: true });
  };
  try {
    run('npm', ['run', 'build'], web); // the binary embeds web/dist
    const bin = join(tmp, 'swt');
    run('go', ['build', '-tags', 'testrig', '-o', bin, './cmd/sockswebui'], root);
    for (const [name, env] of [['a', 'P9_BASE_A'], ['b', 'P9_BASE_B']] as const) {
      const addr = `127.0.0.1:${await freePort()}`;
      const child = spawn(bin, ['--dev-testrig', '--data-dir', join(tmp, `data-${name}`), '--ui-addr', addr], {
        stdio: ['ignore', 'inherit', 'inherit'],
      });
      children.push(child);
      await waitUp(child, `http://${addr}`);
      process.env[env] = `http://${addr}`;
    }
  } catch (e) {
    await teardown();
    throw e;
  }
  return teardown;
}
