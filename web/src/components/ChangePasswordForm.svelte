<script lang="ts">
  import Icon from './Icon.svelte';
  import { api } from '../lib/api';
  import { session } from '../lib/session';
  import { emptyPasswordForm, submitPasswordChange } from '../lib/passwordForm';

  let { bindAddr }: { bindAddr: string } = $props();

  let form = $state(emptyPasswordForm());
  let fields = $state<Record<string, string>>({});
  let formError = $state('');
  let notice = $state('');
  let busy = $state(false);

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    busy = true;
    notice = '';
    const out = await submitPasswordChange({ request: api.request, session }, bindAddr, form);
    busy = false;
    fields = out.fields;
    formError = out.form;
    if (out.ok) {
      form = emptyPasswordForm();
      notice = out.notice;
    }
  }
</script>

<form class="panel" onsubmit={submit} aria-labelledby="pw-title" novalidate>
  <h3 id="pw-title"><Icon name="shield" /> Change password</h3>
  <div class="field">
    <label for="pw-current">Current password</label>
    <input id="pw-current" type="password" autocomplete="current-password" bind:value={form.current}
      aria-invalid={fields.current_password ? 'true' : undefined} aria-describedby="pw-current-err" />
    <p id="pw-current-err" class="field-error">{fields.current_password ?? ''}</p>
  </div>
  <div class="field">
    <label for="pw-new">New password</label>
    <input id="pw-new" type="password" autocomplete="new-password" bind:value={form.next}
      aria-invalid={fields.new_password ? 'true' : undefined} aria-describedby="pw-new-hint pw-new-err" />
    <p id="pw-new-hint" class="hint">At least 8 characters.</p>
    <p id="pw-new-err" class="field-error">{fields.new_password ?? ''}</p>
  </div>
  <div class="field">
    <label for="pw-confirm">Confirm new password</label>
    <input id="pw-confirm" type="password" autocomplete="new-password" bind:value={form.confirm}
      aria-invalid={fields.confirm ? 'true' : undefined} aria-describedby="pw-confirm-err" />
    <p id="pw-confirm-err" class="field-error">{fields.confirm ?? ''}</p>
  </div>
  <div aria-live="polite">
    {#if formError}<p class="form-error">{formError}</p>{/if}
    {#if notice}<p class="hint" role="status">{notice}</p>{/if}
  </div>
  <button type="submit" disabled={busy}>{busy ? 'Changing…' : 'Change password'}</button>
</form>
