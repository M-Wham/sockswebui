<script lang="ts">
  // "Export config" button + dialog. Passphrase and password live only in
  // this component's local state and are wiped in a finally; nothing reaches a store,
  // storage or the URL. The download is fetch -> Blob -> object URL -> <a download>.
  import { api, ApiError } from '../lib/api';
    import {
    blankTransferForm,
    buildExportRequest,
    clearTransferForm,
    downloadBlob,
    rateLimitText,
    withClearedForm,
    type ExportMode,
  } from '../lib/configTransfer';

  let dlg: HTMLDialogElement | undefined = $state();
  let trigger: HTMLButtonElement | undefined = $state();
  let form = $state(blankTransferForm());
  let errors = $state<Record<string, string>>({});
  let formError = $state('');
  let notice = $state('');
  let busy = $state(false);

  function open() {
    clearTransferForm(form);
    errors = {};
    formError = '';
    dlg?.showModal();
  }

  function close() {
    clearTransferForm(form);
    dlg?.close();
  }

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    const built = buildExportRequest(form);
    if (!built.ok) {
      errors = built.errors;
      formError = '';
      return;
    }
    errors = {};
    formError = '';
    busy = true;
    try {
      const out = await withClearedForm(form, () => api.exportConfig(built.body));
      downloadBlob(out.blob, out.filename);
      notice = `Exported to ${out.filename}. Keep this file safe.`;
      dlg?.close();
    } catch (err) {
      if (err instanceof ApiError && err.status === 429) formError = rateLimitText(err.retryAfter);
      else if (err instanceof ApiError && err.fieldErrors.length > 0) {
        errors = Object.fromEntries(err.fieldErrors.map((f) => [f.field, f.message]));
      } else formError = err instanceof ApiError ? err.message : 'Export failed.';
    } finally {
      busy = false;
    }
  }

  const MODES: { value: ExportMode; label: string }[] = [
    { value: 'encrypted', label: 'Encrypted (recommended)' },
    { value: 'no-secrets', label: 'Without secrets' },
  ];
</script>

<button type="button" bind:this={trigger} data-testid="export-config" onclick={open}>Export config</button>
<span class="hint" role="status">{notice}</span>

<dialog bind:this={dlg} aria-labelledby="exp-title" onclose={() => {
  clearTransferForm(form);
  trigger?.focus();
}}>
  <form onsubmit={submit} novalidate>
    <h4 id="exp-title">Export config</h4>
    <fieldset>
      <legend>Mode</legend>
      {#each MODES as m (m.value)}
        <label class="radio"><input type="radio" name="exp-mode" value={m.value} bind:group={form.mode} /> {m.label}</label>
      {/each}
    </fieldset>
    {#if form.mode === 'encrypted'}
      <p class="hint">
        Keep this file and passphrase safe. Anyone with both can use your VPN and proxy accounts. The passphrase cannot be recovered.
      </p>
      <div class="field">
        <label for="exp-pass">Passphrase (12 to 1024 bytes)</label>
        <input id="exp-pass" type="password" autocomplete="new-password" data-testid="export-passphrase"
          bind:value={form.passphrase} aria-invalid={errors.passphrase ? 'true' : undefined} aria-describedby="exp-pass-err" />
        <p id="exp-pass-err" class="field-error">{errors.passphrase ?? ''}</p>
      </div>
      <div class="field">
        <label for="exp-conf">Confirm passphrase</label>
        <input id="exp-conf" type="password" autocomplete="new-password" data-testid="export-confirm"
          bind:value={form.confirm} aria-invalid={errors.confirm ? 'true' : undefined} aria-describedby="exp-conf-err" />
        <p id="exp-conf-err" class="field-error">{errors.confirm ?? ''}</p>
      </div>
    {:else}
      <p class="hint">Keys and passwords are left out. You will need to re-enter them after import.</p>
    {/if}
    <div class="field">
      <label for="exp-pw">Current admin password</label>
      <input id="exp-pw" type="password" autocomplete="current-password" data-testid="export-password"
        bind:value={form.current_password} aria-invalid={errors.current_password ? 'true' : undefined} aria-describedby="exp-pw-err" />
      <p id="exp-pw-err" class="field-error">{errors.current_password ?? ''}</p>
    </div>
    <div aria-live="polite">{#if formError}<p class="form-error">{formError}</p>{/if}</div>
    <div class="actions">
      <button type="submit" data-testid="export-submit" disabled={busy}>{busy ? 'Exporting…' : 'Export'}</button>
      <button type="button" class="secondary" onclick={close}>Cancel</button>
    </div>
  </form>
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
  fieldset {
    border: 0;
    padding: 0;
    margin: 0 0 0.5rem;
  }
  .radio {
    display: block;
  }
  .actions {
    display: flex;
    gap: 0.5rem;
    margin-top: 1rem;
  }
</style>
