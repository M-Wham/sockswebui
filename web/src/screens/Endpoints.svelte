<script lang="ts">
  // Endpoints screen: import drop zone + results, WireGuard list filtered by
  // country, upstream proxy list with add/edit form. Delete asks first and shows the
  // server's reason (e.g. still used by a slot route).
  import { onMount } from 'svelte';
  import { api, ApiError } from '../lib/api';
  import { navigate } from '../lib/router';
  import {
    ALL_COUNTRIES,
    countryOptions,
    filterByCountry,
    UNKNOWN_COUNTRY,
    type EndpointView,
  } from '../lib/endpointFilter';
  import { runImport, type ImportRow } from '../lib/importFiles';
  import { passwordStatus } from '../lib/proxyForm';
  import Icon from '../components/Icon.svelte';
  import ScreenHeader from '../components/ScreenHeader.svelte';
  import DropZone from '../components/DropZone.svelte';
  import ImportResults from '../components/ImportResults.svelte';
  import ProxyForm from '../components/ProxyForm.svelte';
  import WgSecretsForm from '../components/WgSecretsForm.svelte';
  import { privateKeyHint } from '../lib/wgSecretsForm';
    import RotateDialog from '../components/RotateDialog.svelte';
  import { affectedSlots } from '../lib/rotate';
  import type { SlotConfig } from '../lib/slotsStore';

  let endpoints = $state<EndpointView[]>([]);
  let loadError = $state('');
  let importing = $state(false);
  let importRows = $state<ImportRow[]>([]);
  let importMessage = $state('');
  let country = $state(ALL_COUNTRIES);
  let deleteError = $state('');
  let notice = $state('');
  // null = form closed; 'new' = add; otherwise the endpoint being edited
  let editing = $state<EndpointView | 'new' | null>(null);
  // WireGuard endpoint whose keys are being edited (opened by Edit keys or the #/endpoints/<id> link)
  let editingKeys = $state<EndpointView | null>(null);
  let { routeId, focusImport = false }: { routeId?: string; focusImport?: boolean } = $props();

  let slotConfigs = $state<SlotConfig[]>([]);

  async function loadSlots() {
    try {
      const out = await api.request<{ slots?: SlotConfig[] }>('GET', '/api/slots');
      slotConfigs = out?.slots ?? [];
    } catch {
      slotConfigs = [];
    }
  }

  let countries = $derived(countryOptions(endpoints));
  let wireguard = $derived(filterByCountry(endpoints, country));
  let proxies = $derived(endpoints.filter((e) => e.kind === 'socks5' || e.kind === 'http'));

  function errText(err: unknown, fallback: string): string {
    if (!(err instanceof ApiError)) return fallback;
    return err.fieldErrors.length ? err.fieldErrors.map((f) => f.message).join('; ') : err.message;
  }

  async function load() {
    try {
      const out = await api.request<{ endpoints?: EndpointView[] }>('GET', '/api/endpoints');
      endpoints = out?.endpoints ?? [];
      loadError = '';
      if (country !== ALL_COUNTRIES && !countryOptions(endpoints).includes(country)) country = ALL_COUNTRIES;
    } catch (err) {
      loadError = errText(err, 'could not load endpoints');
    }
  }

  let dismissedId = '';

  function openRouteKeys() {
    if (!routeId || routeId === dismissedId) return;
    const hit = endpoints.find((e) => e.id === routeId && e.kind === 'wireguard');
    if (hit && editingKeys?.id !== hit.id) editingKeys = hit;
  }

  $effect(() => {
    void endpoints;
    if (!routeId) dismissedId = '';
    openRouteKeys();
  });

  function closeKeys() {
    editingKeys = null;
    if (routeId) dismissedId = routeId;
    if (routeId) navigate('#/endpoints'); // drop the id so the link does not reopen the editor
  }

  async function onKeysSaved() {
    notice = `Keys saved for “${editingKeys?.name ?? ''}”.`;
    closeKeys();
    await load();
  }

  async function onFiles(files: File[]) {
    importing = true;
    importMessage = '';
    try {
      const out = await runImport(files, (form) => api.request('POST', '/api/endpoints/import', form));
      importRows = out.rows;
      importMessage = out.message;
      if (out.rows.some((r) => r.status === 'imported')) await load();
    } catch (err) {
      importRows = [];
      importMessage = errText(err, 'import failed');
    } finally {
      importing = false;
    }
  }

  async function remove(e: EndpointView) {
    deleteError = '';
    notice = '';
    if (!confirm(`Delete endpoint “${e.name}”? This cannot be undone.`)) return;
    try {
      await api.request('DELETE', `/api/endpoints/${encodeURIComponent(e.id)}`);
      notice = `Deleted “${e.name}”.`;
      if (editing !== null && editing !== 'new' && editing.id === e.id) editing = null;
      await load();
    } catch (err) {
      deleteError = `Could not delete “${e.name}”: ${errText(err, 'request failed')}`;
    }
  }

  async function onSaved() {
    notice = editing === 'new' ? 'Proxy added.' : 'Proxy saved.';
    editing = null;
    await load();
  }

  const countryLabel = (c: string) => (c === UNKNOWN_COUNTRY ? 'Unknown' : c.toUpperCase());

  onMount(() => {
    void load();
    void loadSlots();
    if (focusImport) {
      const el = document.querySelector<HTMLInputElement>('[data-testid="import-input"]');
      el?.scrollIntoView?.({ block: 'center' });
      el?.focus();
    }
  });
</script>

<ScreenHeader title="Endpoints" />
{#if loadError}<p class="form-error" role="alert">{loadError}</p>{/if}
<p class="hint" role="status">{notice}</p>
{#if deleteError}<p class="form-error" role="alert">{deleteError}</p>{/if}

<DropZone {onFiles} busy={importing} />
{#if importMessage}<p class="form-error" role="alert">{importMessage}</p>{/if}
<ImportResults rows={importRows} />

<section class="panel" aria-labelledby="wg-title">
  <div class="head">
    <h3 id="wg-title"><Icon name="route" /> WireGuard endpoints</h3>
    <label class="filter">
      Country
      <select bind:value={country}>
        <option value={ALL_COUNTRIES}>All</option>
        {#each countries as c (c)}<option value={c}>{countryLabel(c)}</option>{/each}
      </select>
    </label>
  </div>
  {#if wireguard.length === 0}
    <p class="hint">No WireGuard endpoints{country === ALL_COUNTRIES ? ' yet. Import WireGuard configs above.' : ' for this country.'}</p>
  {:else}
    <ul class="list" data-testid="wg-list">
      {#each wireguard as e (e.id)}
        <li>
          <div>
            <strong>{e.name}</strong>
            <span class="meta">{countryLabel((e.country ?? '').toLowerCase() || UNKNOWN_COUNTRY)} · {e.endpoint ?? ''}</span>
          </div>
          <div class="actions">
            <button type="button" class="secondary" onclick={() => (editingKeys = e)} aria-label={`Edit keys for ${e.name}`}>Edit keys</button>
            <button type="button" class="secondary" onclick={() => remove(e)} aria-label={`Delete ${e.name}`}>Delete</button>
          </div>
          {#if privateKeyHint(e)}<span class="meta warn">{privateKeyHint(e)}</span>{/if}
        </li>
      {/each}
    </ul>
  {/if}
  {#if editingKeys !== null}
    {#key editingKeys.id}
      <WgSecretsForm endpoint={editingKeys} onSaved={onKeysSaved} onCancel={closeKeys} />
    {/key}
  {/if}
</section>

<section class="panel" aria-labelledby="proxy-title">
  <div class="head">
    <h3 id="proxy-title"><Icon name="shield" /> Upstream proxies</h3>
    {#if editing === null}<button type="button" onclick={() => (editing = 'new')}>Add proxy</button>{/if}
  </div>
  {#if editing !== null}
    {#key editing}
      <ProxyForm endpoint={editing === 'new' ? null : editing} {onSaved} onCancel={() => (editing = null)} />
    {/key}
  {/if}
  {#if proxies.length === 0}
    <p class="hint">No upstream proxies.</p>
  {:else}
    <ul class="list">
      {#each proxies as p (p.id)}
        <li>
          <div>
            <strong>{p.name}</strong>
            <span class="meta">
              {p.kind === 'http' ? 'HTTP' : 'SOCKS5'} · {p.host}:{p.port}{p.username ? ` · user ${p.username}` : ''} ·
              password {passwordStatus(p.password_set === true)}{p.password_has_session ? ', contains {session}' : ''}{p.session?.current_id ? ` · session ID ${p.session.current_id}` : ''}
            </span>
          </div>
          <div class="actions">
            {#if p.session}
              <RotateDialog endpoint={p} affected={affectedSlots(p.id, slotConfigs)} onRotated={() => { void load(); void loadSlots(); }} />
            {/if}
            <button type="button" class="secondary" onclick={() => (editing = p)} aria-label={`Edit ${p.name}`}>Edit</button>
            <button type="button" class="secondary" onclick={() => remove(p)} aria-label={`Delete ${p.name}`}>Delete</button>
          </div>
        </li>
      {/each}
    </ul>
  {/if}
</section>

<style>
  .head {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    justify-content: space-between;
    gap: 0.5rem;
  }
  .filter {
    display: flex;
    align-items: center;
    gap: 0.5rem;
  }
  .list {
    margin: 0.5rem 0 0;
    padding: 0;
    list-style: none;
  }
  .list li {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    justify-content: space-between;
    gap: 0.5rem;
    padding: 0.5rem 0;
    border-bottom: 1px solid var(--border);
  }
  .list li:last-child {
    border-bottom: 0;
  }
  .list li > div:first-child {
    min-width: 0;
    overflow-wrap: anywhere;
  }
  .meta {
    display: block;
    color: var(--muted);
    font-size: 0.9rem;
  }
  .warn {
    flex-basis: 100%;
    color: var(--error);
    font-weight: 600;
  }
  .actions {
    display: flex;
    gap: 0.5rem;
  }
</style>
