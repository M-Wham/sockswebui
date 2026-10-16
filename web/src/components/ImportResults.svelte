<script lang="ts">
  // Per-file import outcomes: imported / skipped-duplicate / error: <reason>.
  import type { ImportRow } from '../lib/importFiles';

  let { rows }: { rows: ImportRow[] } = $props();

  const LABEL: Record<ImportRow['status'], string> = {
    imported: 'imported',
    'skipped-duplicate': 'skipped (duplicate)',
    error: 'error',
  };

  let counts = $derived({
    imported: rows.filter((r) => r.status === 'imported').length,
    skipped: rows.filter((r) => r.status === 'skipped-duplicate').length,
    errors: rows.filter((r) => r.status === 'error').length,
  });
</script>

{#if rows.length > 0}
  <div class="wrap">
    <table data-testid="import-results">
      <caption>
        Import results: {counts.imported} imported, {counts.skipped} skipped, {counts.errors} errors
      </caption>
      <thead>
        <tr><th scope="col">File</th><th scope="col">Result</th><th scope="col">Details</th></tr>
      </thead>
      <tbody>
        {#each rows as row, i (i)}
          <tr>
            <td class="file">{row.file}</td>
            <td><span class="status {row.status}">{LABEL[row.status]}</span></td>
            <td>
              {#if row.reason}<span class="reason">{row.reason}</span>{/if}
              {#each row.warnings as w, j (j)}<span class="warning">Warning: {w}</span>{/each}
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  </div>
{/if}

<style>
  .wrap {
    overflow-x: auto;
    margin-top: 1rem;
  }
  table {
    width: 100%;
    min-width: 20rem;
    border-collapse: collapse;
  }
  caption {
    text-align: left;
    font-weight: 600;
    padding-bottom: 0.25rem;
  }
  th,
  td {
    padding: 0.4rem;
    border-bottom: 1px solid var(--border);
    text-align: left;
    vertical-align: top;
  }
  .file {
    font-family: var(--font-mono);
    font-size: 0.85rem;
    word-break: break-all;
  }
  th {
    font-family: var(--font-mono);
    font-size: 0.72rem;
    letter-spacing: 0.14em;
    text-transform: uppercase;
    color: var(--muted);
  }
  .status {
    display: inline-block;
    padding: 2px 8px;
    border: 1px solid currentColor;
    font-family: var(--font-mono);
    font-size: 0.72rem;
    white-space: nowrap;
  }
  .status.imported {
    color: var(--tag-up);
  }
  .status.error {
    border: 2px solid var(--error);
    background: var(--danger-bg);
    color: var(--danger-fg);
    font-weight: 600;
  }
  .status.skipped-duplicate {
    border-style: dashed;
    color: var(--fg);
  }
  .reason,
  .warning {
    display: block;
  }
  .reason {
    color: var(--error);
  }
</style>
