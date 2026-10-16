<script lang="ts">
  // "Import config" button + dialog: pick or drop a file, the passphrase
  // field appears only when the file CONTENT says mode=encrypted, then an explicit
  // confirmation. Secrets stay in local state and are wiped in a finally.
  import { api, ApiError } from '../lib/api';
      import type { SlotConfig } from '../lib/slotsStore';
  import {
    blankTransferForm,
    clearTransferForm,
    importResultView,
    parseExportMode,
    rateLimitText,
    withClearedForm,
    type ExportMode,
    type ImportView,
  } from '../lib/configTransfer';
  import ImportResult from './ImportResult.svelte';

  const MAX_BYTES = 1024 * 1024;
  const CONFIRM_TEXT =
    'This replaces all endpoints and slots on this device. All slots will stop and stay stopped until you start them. A backup of the current configuration is kept.';

  let dlg: HTMLDialogElement | undefined = $state();
  let trigger: HTMLButtonElement | undefined = $state();
  let form = $state(blankTransferForm());
  let file = $state<File | null>(null);
  let fileMode = $state<ExportMode | null>(null);
  let step = $state<'form' | 'confirm' | 'result'>('form');
  let errors = $state<Record<string, string>>({});
  let formError = $state('');
  let busy = $state(false);
  let over = $state(false);
  let view = $state<ImportView | null>(null);

  function reset() {
    clearTransferForm(form);
    file = null;
    fileMode = null;
    step = 'form';
    errors = {};
    formError = '';
    view = null;
  }

  function open() {
    reset();
    dlg?.showModal();
  }

  function close() {
    clearTransferForm(form);
    dlg?.close();
  }

  async function choose(f: File | undefined) {
    file = null;
    fileMode = null;
    errors = {};
    if (!f) return;
    if (f.size > MAX_BYTES) {
      errors = { file: 'This file is larger than 1 MiB, so it is not a sockswebui export.' };
      return;
    }
    const parsed = parseExportMode(await f.text());
    if ('error' in parsed) {
      errors = { file: parsed.error };
      return;
    }
    file = f;
    fileMode = parsed.mode;
  }

  function review(e: SubmitEvent) {
    e.preventDefault();
    const next: Record<string, string> = {};
    if (!file) next.file = 'Choose an export file.';
    if (form.current_password === '') next.current_password = 'Enter your current admin password.';
    if (fileMode === 'encrypted' && form.passphrase === '') next.passphrase = 'Enter the export passphrase.';
    errors = next;
    if (Object.keys(next).length === 0) step = 'confirm';
  }

  async function loadSlots(): Promise<SlotConfig[]> {
    try {
      const out = await api.request<{ slots?: SlotConfig[] }>('GET', '/api/slots');
      return out?.slots ?? [];
    } catch {
      return [];
    }
  }

  async function apply() {
    if (!file) return;
    busy = true;
    formError = '';
    try {
      const f = file;
      const pass = fileMode === 'encrypted' ? form.passphrase : undefined;
      const res = await withClearedForm(form, () => api.importConfig(f, form.current_password, pass));
      // The SSE store does not carry the new slot list: refetch it.
      const slots = await loadSlots();
      view = importResultView(res, slots);
      step = 'result';
    } catch (err) {
      step = 'form';
      if (err instanceof ApiError && err.status === 429) {
        formError = rateLimitText(err.retryAfter);
      } else if (err instanceof ApiError && err.fieldErrors.length > 0) {
        errors = Object.fromEntries(err.fieldErrors.map((f) => [f.field, f.message]));
      } else formError = err instanceof ApiError ? err.message : 'Import failed.';
    } finally {
      busy = false;
    }
  }

  function onDrop(e: DragEvent) {
    e.preventDefault();
    over = false;
    void choose(e.dataTransfer?.files?.[0]);
  }
</script>

<button type="button" bind:this={trigger} data-testid="import-config" onclick={open}>Import config</button>

<dialog bind:this={dlg} aria-labelledby="imp-title0" onclose={() => {
  clearTransferForm(form);
  trigger?.focus();
}}>
  {#if step === 'result' && view}
    <ImportResult {view} onDone={close} />
  {:else if step === 'confirm'}
    <h4 id="imp-title0">Confirm import</h4>
    <p>{CONFIRM_TEXT}</p>
    <div aria-live="polite">{#if formError}<p class="form-error">{formError}</p>{/if}</div>
    <div class="actions">
      <button type="button" data-testid="import-confirm" disabled={busy} onclick={apply}>{busy ? 'Importing…' : 'Replace configuration'}</button>
      <button type="button" class="secondary" disabled={busy} onclick={() => (step = 'form')}>Back</button>
    </div>
  {:else}
    <form onsubmit={review} novalidate>
      <h4 id="imp-title0">Import config</h4>
      <div class="field drop" class:over ondragover={(e) => { e.preventDefault(); over = true; }}
        ondragleave={() => (over = false)} ondrop={onDrop} role="group" aria-label="Export file">
        <label for="imp-file">Export file (.json, drop also accepted)</label>
        <input id="imp-file" type="file" accept=".json,application/json" data-testid="import-file"
          aria-invalid={errors.file ? 'true' : undefined} aria-describedby="imp-file-err"
          onchange={(e) => choose(e.currentTarget.files?.[0])} />
        <p id="imp-file-err" class="field-error">{errors.file ?? ''}</p>
        {#if file}<p class="hint">{file.name}: {fileMode === 'encrypted' ? 'encrypted' : 'without secrets'}</p>{/if}
      </div>
      {#if fileMode === 'encrypted'}
        <div class="field">
          <label for="imp-pass">Export passphrase</label>
          <input id="imp-pass" type="password" autocomplete="off" data-testid="import-passphrase"
            bind:value={form.passphrase} aria-invalid={errors.passphrase ? 'true' : undefined} aria-describedby="imp-pass-err" />
          <p id="imp-pass-err" class="field-error">{errors.passphrase ?? ''}</p>
        </div>
      {/if}
      <div class="field">
        <label for="imp-pw">Current admin password</label>
        <input id="imp-pw" type="password" autocomplete="current-password" data-testid="import-password"
          bind:value={form.current_password} aria-invalid={errors.current_password ? 'true' : undefined} aria-describedby="imp-pw-err" />
        <p id="imp-pw-err" class="field-error">{errors.current_password ?? ''}</p>
      </div>
      <div aria-live="polite">{#if formError}<p class="form-error">{formError}</p>{/if}</div>
      <div class="actions">
        <button type="submit">Review</button>
        <button type="button" class="secondary" onclick={close}>Cancel</button>
      </div>
    </form>
  {/if}
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
  .drop.over {
    outline: 2px solid var(--accent);
  }
  .actions {
    display: flex;
    gap: 0.5rem;
    margin-top: 1rem;
  }
</style>
