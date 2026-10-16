<script lang="ts">
  import { formatRate } from '../lib/slotsStore';
  import type { DashboardStats } from '../lib/dashboardStats';
  import Icon from './Icon.svelte';
  let { stats }: { stats: DashboardStats } = $props();
</script>

<section class="panel" data-testid="stats" aria-labelledby="stats-title">
  <h3 id="stats-title"><Icon name="stats" /> Status</h3>
  <dl>
    <div><dt>Slots running</dt><dd>{stats.running}/{stats.total}</dd></div>
    <div><dt>Degraded</dt><dd>{stats.degraded}</dd></div>
    <div><dt>Total down</dt><dd>{formatRate(stats.down)}</dd></div>
    <div><dt>Total up</dt><dd>{formatRate(stats.up)}</dd></div>
  </dl>
</section>

<style>
  .panel {
    margin: 0;
    height: 100%;
    box-sizing: border-box;
  }
  dl {
    display: grid;
    gap: 0.6rem;
    margin: 0;
  }
  dl > div {
    display: flex;
    align-items: baseline;
    gap: 0.75rem;
    min-width: 0;
  }
  dt {
    order: 2;
    color: var(--muted);
    font-size: 0.75rem;
    letter-spacing: 0.08em;
    text-transform: uppercase;
  }
  dd {
    order: 1;
    margin: 0;
    min-width: 6.5rem;
    font-family: var(--font-mono);
    font-size: 1.4rem;
    font-weight: 600;
  }
</style>
