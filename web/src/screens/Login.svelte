<script lang="ts">
  import { api, ApiError, type FieldError } from '../lib/api';
  import { takeFlash } from '../lib/flash';

  let { onLoggedIn }: { onLoggedIn: () => void } = $props();

  const notice = takeFlash();
  let password = $state('');
  let busy = $state(false);
  let error = $state('');
  let fieldErrors = $state<FieldError[]>([]);

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    busy = true;
    error = '';
    fieldErrors = [];
    try {
      await api.login(password);
      password = '';
      onLoggedIn();
    } catch (err) {
      // A 409 "setup required" already routed to #/setup inside the client.
      error = err instanceof ApiError ? err.message : 'login failed';
      fieldErrors = err instanceof ApiError ? err.fieldErrors : [];
    } finally {
      busy = false;
    }
  }
</script>

<form class="form-card" onsubmit={submit} aria-labelledby="login-title" novalidate>
  <h2 id="login-title">Log in</h2>
  {#if notice}<p class="hint" role="status">{notice}</p>{/if}
  <div class="field">
    <label for="login-password">Admin password</label>
    <input
      id="login-password"
      type="password"
      autocomplete="current-password"
      required
      bind:value={password}
      aria-invalid={fieldErrors.length > 0}
      aria-describedby="login-errors"
    />
  </div>
  <div id="login-errors" aria-live="polite">
    {#each fieldErrors as fe (fe.field + fe.message)}
      <p class="field-error">{fe.field}: {fe.message}</p>
    {/each}
    {#if error && fieldErrors.length === 0}<p class="form-error">{error}</p>{/if}
  </div>
  <button type="submit" disabled={busy || password === ''}>{busy ? 'Logging in…' : 'Log in'}</button>
  <p class="hint">First run? <a href="#/setup">Create the admin password</a>.</p>
</form>
