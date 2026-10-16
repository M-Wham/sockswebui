<script lang="ts">
  // "New IP" button + confirm dialog. Disabled while the request is in
  // flight; a 429 shows the wait derived from Retry-After. Focus returns to the button.
  import { api, ApiError, type RotateResult } from '../lib/api';
  import type { EndpointView } from '../lib/endpointFilter';
  import { retryWaitText, rotateDialogText } from '../lib/rotate';
  import type { SlotConfig } from '../lib/slotsStore';

  let { endpoint, affected, onRotated }: {
    endpoint: EndpointView;
    affected: SlotConfig[];
    onRotated: (r: RotateResult) => void;
  } = $props();

  let dlg: HTMLDialogElement | undefined = $state();
  let trigger: HTMLButtonElement | undefined = $state();
  let busy = $state(false);
  let message = $state('');
  let failed = $state(false);
  let text = $derived(rotateDialogText(endpoint.name || endpoint.id, affected));

  function close() {
    dlg?.close();
    trigger?.focus();
  }

  async function confirm() {
    close();
    busy = true;
    failed = false;
    message = '';
    try {
      const r = await api.rotateEndpoint(endpoint.id);
      message = `New IP requested. Current session ID: ${r.current_id}.`;
      onRotated(r);
    } catch (err) {
      failed = true;
      if (err instanceof ApiError && err.status === 429) message = retryWaitText(err.retryAfter);
      else if (err instanceof ApiError && err.status === 409) message = 'This endpoint has no session template.';
      else message = err instanceof ApiError ? `New IP failed: ${err.message}` : 'New IP failed.';
    } finally {
      busy = false;
    }
  }
</script>

<button type="button" class="secondary" bind:this={trigger} disabled={busy}
  aria-label={`New IP for ${endpoint.name || endpoint.id}`} onclick={() => dlg?.showModal()}
  >{busy ? 'Requesting…' : 'New IP'}</button>
<span class={failed ? 'form-error' : 'hint'} role={failed ? 'alert' : 'status'}>{message}</span>

<dialog bind:this={dlg} aria-labelledby={`rot-${endpoint.id}-t`} onclose={() => trigger?.focus()}>
  <h4 id={`rot-${endpoint.id}-t`}>New IP</h4>
  <p>{text.message}</p>
  {#each text.warnings as w (w)}<p class="form-error">{w}</p>{/each}
  <div class="actions">
    <button type="button" onclick={confirm}>Confirm new IP</button>
    <button type="button" class="secondary" onclick={close}>Cancel</button>
  </div>
</dialog>

<style>
  dialog {
    width: calc(100vw - 1rem);
    max-width: 28rem;
    padding: 1rem;
    border: 1px solid var(--border);
    border-radius: 2px;
    background: var(--surface);
    color: var(--fg);
    overflow-wrap: anywhere;
  }
  dialog::backdrop {
    background: color-mix(in srgb, var(--bg) 78%, transparent);
  }
  h4 {
    margin: 0 0 0.5rem;
  }
  .actions {
    display: flex;
    gap: 0.5rem;
    margin-top: 1rem;
  }
</style>
