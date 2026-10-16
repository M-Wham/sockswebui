<script lang="ts">
  // Add/edit an upstream SOCKS5/HTTP proxy. The password input is write-only: never
  // pre-filled; the stored value is shown only as "set / not set".
  import { api, ApiError } from '../lib/api';
  import type { EndpointView } from '../lib/endpointFilter';
  import { buildProxyRequest, emptyProxyForm, formFromEndpoint, hasErrors, passwordStatus, proxyFormWarnings, validateProxyForm } from '../lib/proxyForm';
  import SessionFields from './SessionFields.svelte';

  let {
    endpoint = null,
    onSaved,
    onCancel,
  }: { endpoint?: EndpointView | null; onSaved: () => void; onCancel: () => void } = $props();

  // svelte-ignore state_referenced_locally -- parent re-keys this form per endpoint
  let form = $state(endpoint ? formFromEndpoint(endpoint) : emptyProxyForm());
  let fieldErrors = $state<Record<string, string>>({});
  let formError = $state('');
  let saving = $state(false);
  const uid = Math.random().toString(36).slice(2, 8);
  const live = $derived(validateProxyForm(form, endpoint !== null));
  const warnings = $derived(proxyFormWarnings(form));

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    if (hasErrors(live)) {
      formError = 'Fix the highlighted fields first.';
      return;
    }
    saving = true;
    fieldErrors = {};
    formError = '';
    try {
      const req = buildProxyRequest(form, endpoint?.id);
      await api.request(req.method, req.path, req.body);
      form.password = '';
      onSaved();
    } catch (err) {
      if (err instanceof ApiError) {
        const map: Record<string, string> = {};
        const other: string[] = [];
        for (const fe of err.fieldErrors) {
          if (['name', 'kind', 'host', 'port', 'username', 'password', 'session', 'session.length'].includes(fe.field)) {
            map[fe.field] = map[fe.field] ? `${map[fe.field]}; ${fe.message}` : fe.message;
          } else other.push(`${fe.field}: ${fe.message}`);
        }
        fieldErrors = map;
        formError = other.length ? other.join('; ') : err.fieldErrors.length ? '' : err.message;
      } else formError = 'could not save the proxy';
    } finally {
      saving = false;
    }
  }

  const id = (f: string) => `proxy-${uid}-${f}`;
</script>

<form class="proxy-form" onsubmit={submit} novalidate aria-labelledby={id('title')}>
  <h3 id={id('title')}>{endpoint ? `Edit proxy “${endpoint.name}”` : 'Add upstream proxy'}</h3>

  {#snippet err(f: string)}
    {#if fieldErrors[f]}<p class="field-error" id={id(f + '-err')}>{fieldErrors[f]}</p>{/if}
  {/snippet}

  <div class="field">
    <label for={id('name')}>Name</label>
    <input id={id('name')} bind:value={form.name} required aria-invalid={!!fieldErrors.name}
      aria-describedby={fieldErrors.name ? id('name-err') : undefined} />
    {@render err('name')}
  </div>
  <div class="field">
    <label for={id('kind')}>Type</label>
    <select id={id('kind')} bind:value={form.kind} aria-invalid={!!fieldErrors.kind}>
      <option value="socks5">SOCKS5</option>
      <option value="http">HTTP (CONNECT)</option>
    </select>
    {@render err('kind')}
  </div>
  <div class="row">
    <div class="field grow">
      <label for={id('host')}>Host</label>
      <input id={id('host')} bind:value={form.host} autocomplete="off" aria-invalid={!!fieldErrors.host}
        aria-describedby={fieldErrors.host ? id('host-err') : warnings.length ? id('host-warn') : undefined} />
      {@render err('host')}
      {#each warnings as w}<p class="warn" data-testid="proxy-host-warning" id={id('host-warn')}>Warning: {w}</p>{/each}
    </div>
    <div class="field port">
      <label for={id('port')}>Port</label>
      <input id={id('port')} bind:value={form.port} inputmode="numeric" aria-invalid={!!fieldErrors.port}
        aria-describedby={fieldErrors.port ? id('port-err') : undefined} />
      {@render err('port')}
    </div>
  </div>
  <div class="field">
    <label for={id('username')}>Username (optional)</label>
    <input id={id('username')} bind:value={form.username} autocomplete="off" aria-invalid={!!(fieldErrors.username || live.username)}
      aria-describedby={live.username ? id('username-live') : undefined} />
    {#if live.username}<p class="field-error" id={id('username-live')}>{live.username}</p>{/if}
    {@render err('username')}
  </div>
  <div class="field">
    <label for={id('password')}>
      Password <span class="hint">(currently {passwordStatus(form.passwordSet)}{form.passwordSet && form.passwordHasSession ? ', contains {session}' : ''})</span>
    </label>
    <input id={id('password')} type="password" bind:value={form.password} autocomplete="new-password"
      placeholder={endpoint && form.passwordSet ? 'leave empty to keep' : ''} aria-invalid={!!fieldErrors.password}
      disabled={form.clearPassword} />
    {#if live.password}<p class="field-error">{live.password}</p>{/if}
    {@render err('password')}
    {#if endpoint && form.passwordSet}
      <label class="check"><input type="checkbox" bind:checked={form.clearPassword} /> Remove stored password</label>
    {/if}
  </div>

  <SessionFields bind:form errors={live} uid={id('session')} serverErrors={fieldErrors} />
  {#if fieldErrors.session}<p class="field-error">{fieldErrors.session}</p>{/if}
  {#if fieldErrors['session.length']}<p class="field-error">{fieldErrors['session.length']}</p>{/if}

  {#if formError}<p class="form-error" role="alert">{formError}</p>{/if}
  <div class="actions">
    <button type="submit" disabled={saving}>{saving ? 'Saving…' : endpoint ? 'Save proxy' : 'Add proxy'}</button>
    <button type="button" class="secondary" onclick={onCancel}>Cancel</button>
  </div>
</form>

<style>
  .proxy-form {
    padding: 1rem;
    border: 1px solid var(--border);
    border-radius: 0.5rem;
    background: var(--surface);
  }
  h3 {
    margin-top: 0;
    font-size: 1.05rem;
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
  .warn {
    border-left: 4px solid var(--error);
    padding: 0.25rem 0.5rem;
    margin: 0.25rem 0 0;
    font-weight: 600;
  }
  .row {
    display: flex;
    flex-wrap: wrap;
    gap: 0 0.75rem;
  }
  .grow {
    flex: 1 1 12rem;
  }
  .port {
    flex: 0 1 7rem;
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
  .actions {
    display: flex;
    flex-wrap: wrap;
    gap: 0.5rem;
  }
</style>
