<script lang="ts">
  // WireGuard keys editor. Both inputs are write-only: never pre-filled, never echoed, wiped
  // after every save attempt. Only "set / not set" is shown.
  import { api } from '../lib/api';
  import type { EndpointView } from '../lib/endpointFilter';
  import { passwordStatus } from '../lib/proxyForm';
  import { emptyWgSecretsForm, saveWgSecrets, type WgSecretsErrors } from '../lib/wgSecretsForm';

  let { endpoint, onSaved, onCancel }: { endpoint: EndpointView; onSaved: () => void; onCancel: () => void } = $props();

  let form = $state(emptyWgSecretsForm());
  let errors = $state<WgSecretsErrors>({});
  let saving = $state(false);
  const uid = Math.random().toString(36).slice(2, 8);

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    saving = true;
    errors = {};
    const out = await saveWgSecrets(endpoint, form, (m, p, b) => api.request(m, p, b));
    saving = false;
    if (out.ok) onSaved();
    else errors = out.errors;
  }
</script>

<form class="wg-form" onsubmit={submit} novalidate aria-labelledby={`wg-${uid}-title`} data-testid="wg-secrets-form">
  <h3 id={`wg-${uid}-title`}>Keys for “{endpoint.name}”</h3>
  <p class="hint">Keys are write-only and are never shown again. Leave a field empty to keep its stored value.</p>

  <div class="field">
    <label for={`wg-${uid}-priv`}>
      Private key <span class="hint">(currently {passwordStatus(endpoint.private_key_set === true)})</span>
    </label>
    <input id={`wg-${uid}-priv`} type="password" autocomplete="off" spellcheck="false" bind:value={form.privateKey}
      aria-invalid={!!errors.privateKey} />
    {#if errors.privateKey}<p class="field-error">{errors.privateKey}</p>{/if}
  </div>
  <div class="field">
    <label for={`wg-${uid}-psk`}>
      Preshared key (optional) <span class="hint">(currently {passwordStatus(endpoint.preshared_key_set === true)})</span>
    </label>
    <input id={`wg-${uid}-psk`} type="password" autocomplete="off" spellcheck="false" bind:value={form.presharedKey}
      aria-invalid={!!errors.presharedKey} />
    {#if errors.presharedKey}<p class="field-error">{errors.presharedKey}</p>{/if}
  </div>

  {#if errors.form}<p class="form-error" role="alert">{errors.form}</p>{/if}
  <div class="actions">
    <button type="submit" disabled={saving}>{saving ? 'Saving…' : 'Save keys'}</button>
    <button type="button" class="secondary" onclick={onCancel}>Cancel</button>
  </div>
</form>

<style>
  .wg-form {
    margin-top: 0.75rem;
    padding: 1rem;
    border: 1px solid var(--border);
    border-radius: 0.5rem;
    background: var(--surface);
  }
  h3 {
    margin-top: 0;
    font-size: 1.05rem;
  }
  .actions {
    display: flex;
    flex-wrap: wrap;
    gap: 0.5rem;
  }
</style>
