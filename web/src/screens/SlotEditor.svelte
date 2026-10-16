<script lang="ts">
  // Slot editor: bind settings, SOCKS auth (write-only password), kill switch,
  // route builder and per-slot health. Save is disabled while client-side route errors
  // exist; the server still validates (400 field errors, 409 WG endpoint in use).
  import { onMount } from 'svelte';
  import { live } from '../lib/liveStream';
  import { api } from '../lib/api';
  import type { EndpointView } from '../lib/endpointFilter';
  import { stateBadge } from '../lib/badges';
  import { validateRoute } from '../lib/routeRules';
  import {
    BLOCK_LAN_HELP,
    BLOCK_LAN_LABEL,
    blockLanToggleId,
    buildSlotBody,
    formFromSlot,
    initialKillswitch,
    mapSlotErrors,
    openProxyWarning,
    showBlockLanToggle,
    slotFormWarnings,
    type Health,
    type KillswitchState,
    type SlotErrors,
    type SlotForm,
    type SlotView,
  } from '../lib/slotForm';
  import KillswitchField from '../components/KillswitchField.svelte';
  import RouteBuilder from '../components/RouteBuilder.svelte';
  import Icon from '../components/Icon.svelte';
  import ScreenHeader from '../components/ScreenHeader.svelte';
  import SlotGeneral from '../components/SlotGeneral.svelte';
  import StateBadge from '../components/StateBadge.svelte';

  let { id }: { id: number } = $props();

  let form = $state<SlotForm | null>(null);
  let ks = $state<KillswitchState>(initialKillswitch('block'));
  let endpoints = $state<EndpointView[]>([]);
  let defaults = $state<Health | undefined>(undefined);
  let loadError = $state('');
  let errors = $state<SlotErrors>({ fields: {}, hops: {}, form: '' });
  let submittedRoute = $state('');
  let saving = $state(false);
  let notice = $state('');
  // State as loaded/saved; the live stream (below) takes over once it reports this slot.
  let loadedState = $state<string | null>(null);
  let liveState = $state<string | null>(null);
  const slotState = $derived(liveState ?? loadedState);
  // Cached stream state counts only while the stream is connected.
  const readLive = () => (live.connected() ? (live.slots()[id]?.state ?? null) : null);
  // Kill switch as last saved/loaded; the header badge reports this, never the unsaved radio choice.
  let savedKs = $state('block');

  let clientErrors = $derived(form ? validateRoute(form.route, endpoints).errors.length : 0);
  // Server hop errors only apply to the route that was submitted.
  let serverHops = $derived(form && JSON.stringify(form.route) === submittedRoute ? errors.hops : {});

  function parseDefaults(v: unknown): Health | undefined {
    if (typeof v !== 'object' || v === null) return undefined;
    const o = v as Record<string, unknown>;
    if (typeof o.interval_s !== 'number' || typeof o.url !== 'string' || typeof o.fail_threshold !== 'number') {
      return undefined;
    }
    return { interval_s: o.interval_s, url: o.url, fail_threshold: o.fail_threshold };
  }

  function setView(v: SlotView) {
    form = formFromSlot(v);
    ks = initialKillswitch(v.killswitch);
    loadedState = v.status?.state ?? null;
    savedKs = ks.value;
  }

  async function load() {
    try {
      const [slot, eps, settings] = await Promise.all([
        api.request<SlotView>('GET', `/api/slots/${id}`),
        api.request<{ endpoints?: EndpointView[] }>('GET', '/api/endpoints'),
        api.getSettings().catch(() => undefined),
      ]);
      endpoints = eps?.endpoints ?? [];
      defaults = parseDefaults(settings?.health_defaults);
      setView(slot);
      loadError = '';
    } catch (err) {
      loadError = mapSlotErrors(err).form || 'could not load the slot';
    }
  }

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    if (!form || clientErrors > 0 || ks.pending) return;
    saving = true;
    notice = '';
    errors = { fields: {}, hops: {}, form: '' };
    form.killswitch = ks.value;
    submittedRoute = JSON.stringify(form.route);
    try {
      const saved = await api.request<SlotView>('PUT', `/api/slots/${id}`, buildSlotBody(form, defaults));
      setView(saved);
      notice = saved.enabled ? 'Slot saved. It is restarting with the new settings.' : 'Slot saved.';
    } catch (err) {
      errors = mapSlotErrors(err);
    } finally {
      saving = false;
    }
  }

  const warnings = $derived(form ? slotFormWarnings(form) : []);
  const openWarnings = $derived(form ? openProxyWarning(form.bindAddr, form.authEnabled) : []);
  const showBlockLan = $derived(form ? showBlockLanToggle(form.route, ks.value) : false);
  function goToBlockLan(e: MouseEvent) {
    // Hash routing: do not let the anchor change the route; move focus to the toggle instead.
    e.preventDefault();
    const el = document.getElementById(blockLanToggleId(id));
    el?.scrollIntoView({ block: 'center' });
    el?.focus();
  }
  const fid = (f: string) => `slot-${id}-${f.replace(/\W/g, '-')}`;
  const ph = (v: number | string | undefined) => (v === undefined ? '' : `default ${v}`);

  onMount(() => {
    void load();
    liveState = readLive();
    return live.subscribe(
      { onChange: () => (liveState = readLive()) },
      { fresh: true },
    );
  });
</script>

<ScreenHeader title={`Edit slot ${id}`} back="#/" backLabel="Back to slots">
  {#snippet tag()}
    {#if form}
      {@const b = stateBadge(slotState as Parameters<typeof stateBadge>[0], savedKs)}
      <StateBadge badge={b} kind="state" value={slotState ?? 'UNKNOWN'} />
    {/if}
  {/snippet}
  {#snippet actions()}
    <a class="button secondary" href="#/">Cancel</a>
    {#if form}
      <button type="submit" form={`slot-form-${id}`} disabled={saving || clientErrors > 0 || ks.pending}
        aria-describedby={fid('save-hint')}>{saving ? 'Saving…' : 'Save slot'}</button>
    {/if}
  {/snippet}
</ScreenHeader>
{#if loadError}<p class="form-error" role="alert">{loadError}</p>{/if}

{#snippet err(f: string)}
  {#if errors.fields[f]}<p class="field-error" id={fid(f) + '-err'}>{errors.fields[f]}</p>{/if}
{/snippet}

{#if form}
  <form id={`slot-form-${id}`} onsubmit={submit} novalidate aria-label={`Edit slot ${id}`}>
    <SlotGeneral {id} bind:form {errors} {openWarnings} {showBlockLan} onBlockLan={goToBlockLan} />

    <RouteBuilder bind:route={form.route} {endpoints} {serverHops} serverError={errors.fields.route} />

    <KillswitchField bind:ks error={errors.fields.killswitch} />

    {#if showBlockLan}
      <fieldset class="panel wide" data-testid="block-lan-toggle">
        <legend class="visually-hidden">LAN access</legend>
        <h3><Icon name="shield" /> LAN access</h3>
        <label class="check"><input type="checkbox" id={blockLanToggleId(id)} bind:checked={form.blockLanTargets}
            aria-describedby={fid('block-lan-hint')} /> {BLOCK_LAN_LABEL}</label>
        <p class="hint" id={fid('block-lan-hint')}>{BLOCK_LAN_HELP}</p>
      </fieldset>
    {/if}

    <fieldset class="panel wide">
      <legend class="visually-hidden">Health check</legend>
      <h3><Icon name="stats" /> Health check</h3>
      <p class="hint">Probed through the full route. Leave a field empty to use the global default.</p>
      <div class="row">
        <div class="field small">
          <label for={fid('health.interval_s')}>Interval (s)</label>
          <input id={fid('health.interval_s')} bind:value={form.healthInterval} inputmode="numeric"
            placeholder={ph(defaults?.interval_s)} aria-invalid={!!errors.fields['health.interval_s']} />
          {@render err('health.interval_s')}
        </div>
        <div class="field small">
          <label for={fid('health.fail_threshold')}>Fail threshold</label>
          <input id={fid('health.fail_threshold')} bind:value={form.healthThreshold} inputmode="numeric"
            placeholder={ph(defaults?.fail_threshold)} aria-invalid={!!errors.fields['health.fail_threshold']} />
          {@render err('health.fail_threshold')}
        </div>
      </div>
      <div class="field">
        <label for={fid('health.url')}>Probe URL</label>
        <input id={fid('health.url')} bind:value={form.healthUrl} inputmode="url" autocomplete="off"
          placeholder={defaults?.url ?? ''} aria-invalid={!!errors.fields['health.url']} />
        {@render err('health.url')}
        {#each warnings as w}<p class="warn" data-testid="health-url-warning">Warning: {w}</p>{/each}
      </div>
    </fieldset>

    {#if errors.form}<p class="form-error" role="alert">{errors.form}</p>{/if}
    {#if clientErrors > 0}<p class="form-error">Fix the route errors above before saving.</p>{/if}
    <p class="hint" role="status">{notice}</p>
    <p class="hint" id={fid('save-hint')}>Saving a running slot restarts it (stop, rebuild, start).</p>
  </form>
{:else if !loadError}
  <p class="hint">Loading…</p>
{/if}

<style>
  .warn {
    border-left: 4px solid var(--error);
    padding: 0.25rem 0.5rem;
    margin: 0.25rem 0 0;
    font-weight: 600;
  }
  fieldset {
    max-width: none;
    min-width: 0;
  }
  h3 {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    margin: 0 0 0.75rem;
  }
  .row {
    display: flex;
    flex-wrap: wrap;
    gap: 0 0.75rem;
  }
  .small {
    flex: 1 1 8rem;
  }
  .check {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    margin-bottom: 0.75rem;
  }
  .check input {
    width: auto;
    min-height: 1.25rem;
  }
  :global(.screen-header a.button) {
    text-decoration: none;
  }
</style>
