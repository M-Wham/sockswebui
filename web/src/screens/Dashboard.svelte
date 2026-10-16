<script module lang="ts">
  import { record, type Sample } from '../lib/throughputHistory';
  // Lives at module level so in-app navigation keeps it; a reload clears it.
  let history: Sample[] = [];
</script>

<script lang="ts">
  import { onMount } from 'svelte';
  import { api, ApiError } from '../lib/api';
  import { SLOT_COUNT } from '../lib/router';
  import { live as liveStream } from '../lib/liveStream';
  import { applyStatus, initialSlots, reduce, seedFromList, type SlotConfig } from '../lib/slotsStore';
  import type { EndpointView } from '../lib/endpointFilter';
  import SlotsTable from '../components/SlotsTable.svelte';
  import EndpointsSummary from '../components/EndpointsSummary.svelte';
  import ScreenHeader from '../components/ScreenHeader.svelte';
  import StatsPanel from '../components/StatsPanel.svelte';
  import ThroughputChart from '../components/ThroughputChart.svelte';
  import { dashboardStats } from '../lib/dashboardStats';
  import { buckets } from '../lib/throughputHistory';

  // Cached stream state is shown only while the stream is connected; never stale-as-live.
  let slots = $state(liveStream.connected() ? liveStream.slots() : initialSlots());
  let configs = $state<SlotConfig[]>([]);
  let endpoints = $state<EndpointView[]>([]);
  let loadError = $state('');
  let live = $state(liveStream.connected());
  let samples = $state<Sample[]>(history);
  let now = $state(Date.now());
  let stats = $derived(dashboardStats(slots, configs));
  let bars = $derived(buckets(samples, now));
  let configured = $derived(configs.filter((c) => c.configured).length);

  async function loadConfigs() {
    try {
      const out = await api.request<{ slots?: SlotConfig[] }>('GET', '/api/slots');
      configs = (out?.slots ?? []).filter((c) => c.id >= 1 && c.id <= SLOT_COUNT);
      slots = seedFromList(slots, out?.slots);
      loadError = '';
    } catch (err) {
      loadError = err instanceof ApiError ? err.message : 'could not load slots';
    }
  }

  async function loadEndpoints() {
    try {
      const out = await api.request<{ endpoints?: EndpointView[] }>('GET', '/api/endpoints');
      endpoints = out?.endpoints ?? [];
    } catch {
      endpoints = []; // New IP buttons simply stay hidden
    }
  }

  function onRotated() {
    void loadEndpoints();
    void loadConfigs();
  }

  function onStatus(body: unknown, seenSeq: number) {
    slots = applyStatus(slots, body, seenSeq);
    void loadConfigs(); // enabled flag changed server-side
  }

  onMount(() => {
    void loadConfigs();
    void loadEndpoints();
    const tick = setInterval(() => (now = Date.now()), 5000);
    const stop = liveStream.subscribe({
      onChange: () => (live = liveStream.connected()),
      onFrame(event, data) {
        slots = reduce(slots, event, data);
        if (event === 'throughput') {
          const t = dashboardStats(slots, configs);
          history = record(history, { t: Date.now(), down: t.down, up: t.up });
          samples = history;
          now = Date.now();
        }
      },
      onClosed() {
        live = false;
        // A closed stream is often an expired session: any 401 here routes to login.
        void loadConfigs();
      },
    }, { fresh: true });
    return () => {
      clearInterval(tick);
      stop();
    };
  });
</script>

<ScreenHeader title="Slots">
  {#snippet tag()}{configured}/{SLOT_COUNT}{/snippet}
  {#snippet actions()}<a class="button secondary" href="#/import">Import configs</a>{/snippet}
</ScreenHeader>
<p class="hint" aria-live="polite">
  {live ? 'Live: updates stream from the device.' : 'Connecting to live updates…'}
</p>
{#if loadError}<p class="form-error" role="alert">{loadError}</p>{/if}

<div class="overview">
  <StatsPanel {stats} />
  <ThroughputChart {bars} />
</div>

<SlotsTable {configs} {slots} {endpoints} {onStatus} {onRotated} />
<EndpointsSummary {endpoints} {configs} />
<style>
  .overview {
    display: grid;
    grid-template-columns: minmax(0, 1fr);
    gap: 1rem;
    margin: 0 0 1rem;
  }
  @media (min-width: 56.25rem) {
    .overview {
      grid-template-columns: 2fr 3fr;
    }
  }
  .button {
    display: inline-flex;
    align-items: center;
    padding: 0.35rem 0.75rem;
    border: 1px solid var(--border);
    border-radius: 2px;
    text-decoration: none;
    font-size: 0.8rem;
    letter-spacing: 0.06em;
    text-transform: uppercase;
  }
</style>
