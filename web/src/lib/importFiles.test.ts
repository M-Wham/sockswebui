import { describe, expect, it, vi } from 'vitest';
import {
  MAX_IMPORT_BYTES,
  isSupportedImport,
  planImport,
  runImport,
  toRows,
  type ImportRow,
} from './importFiles';

function file(name: string, size = 10): File {
  return new File([new Uint8Array(size)], name);
}

describe('isSupportedImport', () => {
  it('accepts .conf and .zip case-insensitively only', () => {
    for (const n of ['a.conf', 'B.CONF', 'x.zip', 'Y.Zip', 'se-got-wg-001.conf']) {
      expect(isSupportedImport(n)).toBe(true);
    }
    for (const n of ['a.txt', 'conf', 'a.conf.bak', 'zip', 'a.tar.gz', '']) {
      expect(isSupportedImport(n)).toBe(false);
    }
  });
});

describe('planImport', () => {
  it('splits accepted files from unsupported ones, which become error rows', () => {
    const p = planImport([file('a.conf'), file('notes.txt'), file('B.ZIP')]);
    expect(p.accepted.map((f) => f.name)).toEqual(['a.conf', 'B.ZIP']);
    expect(p.rejected).toEqual<ImportRow[]>([
      { file: 'notes.txt', status: 'error', reason: 'unsupported file type', warnings: [] },
    ]);
    expect(p.tooLarge).toBe('');
  });

  it('refuses a total over 5 MB before upload', () => {
    const half = MAX_IMPORT_BYTES / 2;
    expect(planImport([file('a.zip', half), file('b.zip', half)]).tooLarge).toBe('');
    const p = planImport([file('a.zip', half), file('b.zip', half + 1)]);
    expect(p.tooLarge).toMatch(/5 MB/);
  });

  it('ignores unsupported files when totalling the size', () => {
    const p = planImport([file('a.zip', 10), file('big.iso', MAX_IMPORT_BYTES + 1)]);
    expect(p.tooLarge).toBe('');
  });
});

describe('toRows', () => {
  it('maps server per-file results to table rows', () => {
    const rows = toRows({
      results: [
        { file: 'se-got-wg-001.conf', status: 'imported', id: 'ep-1' },
        { file: 'b.conf', status: 'skipped-duplicate' },
        { file: 'c.conf', status: 'error', reason: 'missing PrivateKey' },
        { file: 'home.conf', status: 'imported', warnings: ['example warning'], id: 'ep-2' },
      ],
    });
    expect(rows).toEqual<ImportRow[]>([
      { file: 'se-got-wg-001.conf', status: 'imported', reason: '', warnings: [], id: 'ep-1' },
      { file: 'b.conf', status: 'skipped-duplicate', reason: '', warnings: [] },
      { file: 'c.conf', status: 'error', reason: 'missing PrivateKey', warnings: [] },
      { file: 'home.conf', status: 'imported', reason: '', warnings: ['example warning'], id: 'ep-2' },
    ]);
  });

  it('tolerates a malformed response', () => {
    expect(toRows(undefined)).toEqual([]);
    expect(toRows({ results: [{ nope: 1 }] })).toEqual([]);
  });
});

describe('runImport', () => {
  it('sends all accepted files in ONE multipart request under field "files"', async () => {
    const send = vi.fn(async (_form: FormData) => ({
      results: [
        { file: 'a.conf', status: 'imported', id: 'ep-1' },
        { file: 'b.zip', status: 'skipped-duplicate' },
      ],
    }));
    const out = await runImport([file('a.conf'), file('x.png'), file('b.zip')], send);
    expect(send).toHaveBeenCalledTimes(1);
    const form = send.mock.calls[0][0];
    expect(form.getAll('files').map((f) => (f as File).name)).toEqual(['a.conf', 'b.zip']);
    expect(out.message).toBe('');
    expect(out.rows.map((r) => [r.file, r.status])).toEqual([
      ['a.conf', 'imported'],
      ['b.zip', 'skipped-duplicate'],
      ['x.png', 'error'],
    ]);
  });

  it('does not upload when nothing is supported', async () => {
    const send = vi.fn(async () => ({}));
    const out = await runImport([file('a.txt')], send);
    expect(send).not.toHaveBeenCalled();
    expect(out.rows).toEqual([{ file: 'a.txt', status: 'error', reason: 'unsupported file type', warnings: [] }]);
  });

  it('does not upload when the total is over 5 MB', async () => {
    const send = vi.fn(async () => ({}));
    const out = await runImport([file('a.zip', MAX_IMPORT_BYTES + 1)], send);
    expect(send).not.toHaveBeenCalled();
    expect(out.message).toMatch(/5 MB/);
    expect(out.rows).toEqual([]);
  });
});
