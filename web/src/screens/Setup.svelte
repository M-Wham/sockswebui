<script lang="ts">
  import { api, ApiError, type FieldError } from '../lib/api';
  import { setFlash } from '../lib/flash';
  import { navigate } from '../lib/router';

  let { onLoggedIn }: { onLoggedIn: () => void } = $props();

  const MIN_LEN = 8;
  let password = $state('');
  let confirm = $state('');
  let busy = $state(false);
  let error = $state('');
  let fieldErrors = $state<FieldError[]>([]);
  let alreadySetUp = $state(false);

  let tooShort = $derived(password !== '' && [...password].length < MIN_LEN);
  let mismatch = $derived(confirm !== '' && confirm !== password);

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    if (tooShort || mismatch || password === '') return;
    busy = true;
    error = '';
    fieldErrors = [];
    try {
      await api.setup(password);
    } catch (err) {
      const ae = err instanceof ApiError ? err : null;
      alreadySetUp = ae?.status === 409;
      error = ae ? ae.message : 'setup failed';
      fieldErrors = ae ? ae.fieldErrors : [];
      busy = false;
      return;
    }
    try {
      await api.login(password); // log in automatically with the same password
      password = '';
      confirm = '';
      onLoggedIn();
    } catch {
      // The password is saved; only the automatic login failed.
      password = '';
      confirm = '';
      setFlash('Password saved. Automatic login failed. Please log in with your new password.');
      navigate('#/login');
    } finally {
      busy = false;
    }
  }
</script>

<form class="form-card" onsubmit={submit} aria-labelledby="setup-title" novalidate>
  <h2 id="setup-title">First-run setup</h2>
  <p class="hint">Create the admin password for this device (at least {MIN_LEN} characters).</p>
  <div class="field">
    <label for="setup-password">New admin password</label>
    <input
      id="setup-password"
      type="password"
      autocomplete="new-password"
      required
      minlength={MIN_LEN}
      bind:value={password}
      aria-invalid={tooShort}
      aria-describedby="setup-password-help"
    />
    <span id="setup-password-help" class={tooShort ? 'field-error' : 'hint'}>
      {tooShort ? `Must be at least ${MIN_LEN} characters.` : `Minimum ${MIN_LEN} characters.`}
    </span>
  </div>
  <div class="field">
    <label for="setup-confirm">Confirm password</label>
    <input
      id="setup-confirm"
      type="password"
      autocomplete="new-password"
      required
      bind:value={confirm}
      aria-invalid={mismatch}
      aria-describedby="setup-confirm-help"
    />
    <span id="setup-confirm-help" class="field-error" aria-live="polite">
      {mismatch ? 'Passwords do not match.' : ''}
    </span>
  </div>
  <div aria-live="polite">
    {#each fieldErrors as fe (fe.field + fe.message)}
      <p class="field-error">{fe.field}: {fe.message}</p>
    {/each}
    {#if error && fieldErrors.length === 0}<p class="form-error">{error}</p>{/if}
    {#if alreadySetUp}<p><a href="#/login">Go to login</a></p>{/if}
  </div>
  <button type="submit" disabled={busy || password === '' || tooShort || confirm !== password}>
    {busy ? 'Saving…' : 'Set password and log in'}
  </button>
</form>
