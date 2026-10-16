<script lang="ts">
  import { onMount } from 'svelte';
  import { api, ApiError } from '../lib/api';
  import { LOG_SLOTS, loadLogs } from '../lib/logs';
  import ScreenHeader from '../components/ScreenHeader.svelte';
  import Icon from '../components/Icon.svelte';

  let slot = $state(1);
  let lines = $state<string[]>([]);
  let error = $state('');
  let loading = $state(false);
  let seq = 0;

  async function refresh() {
    const mine = ++seq;
    loading = true;
    try {
      const out = await loadLogs(api.request, slot);
      if (mine !== seq) return;
      lines = out;
      error = '';
    } catch (err) {
      if (mine !== seq) return;
      error = err instanceof ApiError ? err.message : 'could not load logs';
    } finally {
      if (mine === seq) loading = false;
    }
  }

  onMount(() => void refresh());
</script>

<ScreenHeader title="Logs" />
<section class="panel" aria-labelledby="log-title">
<h3 id="log-title"><Icon name="logs" /> Slot log</h3>
<div class="bar">
  <div class="field">
    <label for="log-slot">Slot</label>
    <select id="log-slot" bind:value={slot} onchange={refresh}>
      {#each LOG_SLOTS as id (id)}<option value={id}>Slot {id}</option>{/each}
    </select>
  </div>
  <button type="button" class="secondary" onclick={refresh} aria-busy={loading}>Refresh</button>
</div>
{#if error}<p class="form-error" role="alert">{error}</p>{/if}
<p class="hint">Last 500 lines, newest at the bottom. Secrets are redacted.</p>
<!-- svelte-ignore a11y_no_noninteractive_tabindex -->
<pre class="log" tabindex="0" aria-label="Slot {slot} log">{lines.length ? lines.join('\n') : 'No log lines yet.'}</pre>
</section>

<style>
  .bar {
    display: flex;
    flex-wrap: wrap;
    align-items: end;
    gap: 0.75rem;
  }
  .bar .field {
    margin-bottom: 0;
  }
  .log {
    max-height: 60vh;
    margin: 0;
    padding: 0.75rem;
    overflow: auto;
    border: 1px solid var(--border);
    border-radius: 2px;
    background: var(--surface);
    color: var(--fg);
    font-family: var(--font-mono);
    font-size: 0.85rem;
    white-space: pre-wrap;
    overflow-wrap: anywhere;
  }
</style>
