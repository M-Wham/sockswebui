<script lang="ts">
  // "Rotating session" settings. current_id is shown read-only.
  import type { ProxyForm, ProxyFormErrors } from '../lib/proxyForm';

  let { form = $bindable(), errors, uid, serverErrors = {} }: {
    form: ProxyForm;
    errors: ProxyFormErrors;
    uid: string;
    serverErrors?: Record<string, string>;
  } = $props();
  const id = (f: string) => `${uid}-${f}`;
</script>

<fieldset class="session">
  <legend>Rotating session</legend>
  <label class="check">
    <input type="checkbox" bind:checked={form.sessionEnabled} aria-describedby={id('help')} />
    Rotating session
  </label>
  <p class="hint" id={id('help')}>
    Rotating proxies pick the exit IP from a session ID inside your credentials. Put the placeholder
    <code>{'{session}'}</code> exactly once in the username or the password and the app fills in an ID.
    Username example: <code>myuser-country-GB-ssid-{'{session}'}</code>. Password example:
    <code>mypass_{'{session}'}</code>. The placeholder cannot be the whole field. Editing a password that
    contains the placeholder means retyping the whole password.
  </p>
  {#if form.sessionEnabled}
    <div class="row">
      <div class="field">
        <label for={id('charset')}>ID characters</label>
        <select id={id('charset')} bind:value={form.sessionCharset}>
          <option value="alnum">Letters and digits</option>
          <option value="digits">Digits only</option>
        </select>
      </div>
      <div class="field len">
        <label for={id('length')}>ID length (1-32)</label>
        <input id={id('length')} bind:value={form.sessionLength} inputmode="numeric"
          aria-invalid={!!(errors.sessionLength || serverErrors['session.length'])}
          aria-describedby={errors.sessionLength ? id('length-err') : undefined} />
      </div>
    </div>
    {#if errors.sessionLength}<p class="field-error" id={id('length-err')}>{errors.sessionLength}</p>{/if}
    {#if form.currentId}
      <p>Current session ID: <code data-testid="current-id">{form.currentId}</code> <span class="hint">(read-only)</span></p>
    {/if}
  {/if}
  {#if errors.session}<p class="field-error" role="alert">{errors.session}</p>{/if}
</fieldset>

<style>
  .session {
    border: 1px solid var(--border);
    border-radius: 0.375rem;
    margin: 0 0 1rem;
    padding: 0.5rem 0.75rem;
  }
  .check {
    display: flex;
    align-items: center;
    gap: 0.5rem;
  }
  .check input {
    width: auto;
    min-height: 1.25rem;
  }
  .row {
    display: flex;
    flex-wrap: wrap;
    gap: 0 0.75rem;
  }
  .len {
    flex: 0 1 8rem;
  }
  select {
    font: inherit;
    min-height: 2.75rem;
    padding: 0.5rem;
    border: 1px solid var(--border);
    border-radius: 0.375rem;
    background: var(--bg);
    color: var(--fg);
  }
</style>
