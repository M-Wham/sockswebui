<script lang="ts">
  import { tick } from 'svelte';
  import { api, ApiError } from '../lib/api';
  import { restartAll, restartConfirmText, restartSummary } from '../lib/restartAll';
  import Icon from './Icon.svelte';

  let confirming = $state(false);
  let running = $state(false);
  let count = $state(0);
  let summary = $state('');
  let failures = $state<{ id: number; name: string; message: string }[]>([]);
  let loadError = $state('');
  let trigger: HTMLButtonElement | undefined = $state();
  let yes: HTMLButtonElement | undefined = $state();

  async function ask() {
    summary = '';
    failures = [];
    loadError = '';
    try {
      const out = await api.request<{ slots?: { configured?: boolean; enabled?: boolean }[] }>('GET', '/api/slots');
      count = (out?.slots ?? []).filter((s) => s.configured && s.enabled).length;
    } catch (e) {
      loadError = e instanceof ApiError ? e.message : 'request failed';
      return;
    }
    confirming = true;
    await tick();
    yes?.focus();
  }

  async function cancel() {
    confirming = false;
    await tick();
    trigger?.focus();
  }

  async function go() {
    confirming = false;
    running = true;
    try {
      const r = await restartAll(api.request);
      summary = restartSummary(r);
      failures = r.failed;
    } catch (e) {
      loadError = e instanceof ApiError ? e.message : 'request failed';
    } finally {
      running = false;
    }
    await tick();
    trigger?.focus();
  }
</script>

<div class="restart-all">
  <button type="button" class="secondary" bind:this={trigger} disabled={running} onclick={ask}>
    <Icon name="restart" />{running ? 'Restarting…' : 'Restart all'}
  </button>
  {#if confirming}
    <div role="group" aria-label="Confirm restart all" class="confirm">
      <p>{restartConfirmText(count)}</p>
      <button type="button" bind:this={yes} onclick={go}>Yes, restart all</button>
      <button type="button" class="secondary" onclick={cancel}>Cancel</button>
    </div>
  {/if}
  {#if summary}<p role="status" class="result">{summary}</p>{/if}
  {#if loadError}<p role="alert" class="result err">{loadError}</p>{/if}
  {#if failures.length > 0}
    <ul role="alert" class="result err">
      {#each failures as f (f.id)}
        <li>{f.name || `Slot ${f.id}`}: {f.message}</li>
      {/each}
    </ul>
  {/if}
</div>

<style>
  .restart-all {
    display: grid;
    gap: 0.4rem;
  }
  button {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    gap: 0.4rem;
    width: 100%;
    font-size: 0.78rem;
    letter-spacing: 0.1em;
    text-transform: uppercase;
  }
  .confirm {
    display: grid;
    gap: 0.4rem;
    padding: 0.5rem;
    border: 1px solid var(--border);
  }
  .confirm p,
  .result {
    margin: 0;
    font-size: 0.78rem;
  }
  ul.result {
    padding-left: 1rem;
  }
  .err {
    color: var(--error);
  }
</style>
