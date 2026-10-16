<script lang="ts">
  import { formatRate } from '../lib/slotsStore';
  import { BUCKETS, peak, type Bar } from '../lib/throughputHistory';
  import Icon from './Icon.svelte';
  let { bars }: { bars: Bar[] } = $props();

  const W = 300;
  const H = 100;
  const STEP = W / BUCKETS;
  const BW = STEP * 0.4;
  let max = $derived(peak(bars));
  let empty = $derived(bars.every((b) => b.down === null && b.up === null));
  const h = (v: number | null, m: number) => (v === null || m <= 0 ? 0 : (v / m) * H);
</script>

<figure class="panel" data-testid="throughput-chart">
  <figcaption><Icon name="chart" /> Throughput · last 5 min</figcaption>
  <svg
    viewBox="0 0 {W} {H}"
    preserveAspectRatio="none"
    role="img"
    aria-label="Throughput over the last 5 minutes, peak {formatRate(max)}"
  >
    {#each bars as b, i (b.start)}
      <rect class="down" x={i * STEP + STEP * 0.1} y={H - h(b.down, max)} width={BW} height={h(b.down, max)} />
      <rect class="up" x={i * STEP + STEP * 0.1 + BW} y={H - h(b.up, max)} width={BW} height={h(b.up, max)} />
    {/each}
  </svg>
  <div class="axis" aria-hidden="true"><span>-5 min</span><span>-2.5 min</span><span>now</span></div>
  <ul class="legend">
    <li><span class="sw down"></span>Down</li>
    <li><span class="sw up"></span>Up</li>
  </ul>
  {#if empty}<p class="hint">No data yet. Bars fill in while this page is open.</p>{/if}
</figure>

<style>
  .panel {
    margin: 0;
    height: 100%;
    box-sizing: border-box;
    min-width: 0;
  }
  figcaption {
    margin: 0 0 0.5rem;
    color: var(--muted);
    font-size: 0.75rem;
    letter-spacing: 0.08em;
    text-transform: uppercase;
  }
  svg {
    display: block;
    width: 100%;
    height: 7rem;
    border-bottom: 1px solid var(--border);
  }
  .down {
    fill: var(--fg);
  }
  .up {
    fill: var(--accent);
  }
  .axis {
    display: flex;
    justify-content: space-between;
    margin-top: 0.25rem;
    color: var(--muted);
    font-size: 0.7rem;
  }
  .legend {
    display: flex;
    gap: 1rem;
    margin: 0.5rem 0 0;
    padding: 0;
    list-style: none;
    font-size: 0.75rem;
  }
  .sw {
    display: inline-block;
    width: 0.7rem;
    height: 0.7rem;
    margin-right: 0.35rem;
    vertical-align: middle;
  }
  .sw.down {
    background: var(--fg);
  }
  .sw.up {
    background: var(--accent);
  }
  .hint {
    margin: 0.5rem 0 0;
  }
</style>
