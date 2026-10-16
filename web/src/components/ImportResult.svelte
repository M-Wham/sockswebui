<script lang="ts">
  // Post-import summary: counts, Start all (sequential, per-slot errors),
  // and links to every endpoint/slot whose secrets must be re-entered.
  import { api, ApiError } from '../lib/api';
  import type { ImportView } from '../lib/configTransfer';

  let { view, onDone }: { view: ImportView; onDone: () => void } = $props();

  let busy = $state(false);
  let results = $state<{ id: number; ok: boolean; text: string }[]>([]);

  async function startAll() {
    busy = true;
    results = [];
    for (const id of view.startable) {
      try {
        await api.request('POST', `/api/slots/${id}/start`);
        results = [...results, { id, ok: true, text: 'start requested' }];
      } catch (err) {
        results = [...results, { id, ok: false, text: err instanceof ApiError ? err.message : 'start failed' }];
      }
    }
    busy = false;
  }
</script>

<h4 id="imp-title0">Import complete</h4>
<p role="status">
  Imported {view.endpoints} endpoint{view.endpoints === 1 ? '' : 's'} and {view.slots} slot{view.slots === 1 ? '' : 's'}.
  All slots are stopped.{view.backup ? ' A backup of the previous configuration was kept.' : ''}
</p>

{#if view.missingEndpoints.length > 0 || view.missingSlots.length > 0}
  <section aria-labelledby="imp-missing">
    <h5 id="imp-missing">Secrets to re-enter</h5>
    <ul>
      {#each view.missingEndpoints as e (e.id)}
        <li><a href={e.href} onclick={onDone}>Endpoint {e.id}</a></li>
      {/each}
      {#each view.missingSlots as s (s.id)}
        <li>
          <a href={s.href} onclick={onDone}>Slot {s.id}</a>
          {#if s.via.length > 0}<span class="hint"> (blocked by endpoint {s.via.join(', ')})</span>{/if}
        </li>
      {/each}
    </ul>
  </section>
{/if}

{#if view.startable.length > 0}
  <button type="button" data-testid="import-start-all" disabled={busy} onclick={startAll}>
    {busy ? 'Starting…' : `Start all (${view.startable.length})`}
  </button>
{/if}
<ul aria-live="polite">
  {#each results as r (r.id)}
    <li class={r.ok ? 'hint' : 'form-error'}>Slot {r.id}: {r.text}</li>
  {/each}
</ul>
<div class="actions"><button type="button" class="secondary" onclick={onDone}>Close</button></div>

<style>
  .actions {
    margin-top: 1rem;
  }
  h5 {
    margin: 0.5rem 0 0.25rem;
  }
</style>
