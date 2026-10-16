<script lang="ts">
  import type { EndpointView } from '../lib/endpointFilter';
  import type { SlotConfig, SlotsState } from '../lib/slotsStore';
  import Icon from './Icon.svelte';
  import SlotRow from './SlotRow.svelte';

  let {
    configs,
    slots,
    endpoints,
    onStatus,
    onRotated,
  }: {
    configs: SlotConfig[];
    slots: SlotsState;
    endpoints: EndpointView[];
    onStatus: (body: unknown, seenSeq: number) => void;
    onRotated: () => void;
  } = $props();
</script>

<section class="panel" aria-labelledby="slots-title">
  <h3 id="slots-title"><Icon name="slots" /> Proxy slots</h3>
  <div class="wrap">
    <table>
      <thead>
        <tr>
          <th scope="col">Slot</th>
          <th scope="col">State</th>
          <th scope="col">Route</th>
          <th scope="col">Exit</th>
          <th scope="col">Latency</th>
          <th scope="col">Down / Up</th>
          <th scope="col"><span class="visually-hidden">Actions</span></th>
        </tr>
      </thead>
      <tbody>
        {#each configs as config (config.id)}
          <SlotRow {config} live={slots[config.id]} {onStatus} {endpoints} slots={configs} {onRotated} />
        {/each}
      </tbody>
    </table>
  </div>
</section>

<style>
  .panel {
    max-width: none;
  }
  .wrap {
    overflow-x: auto;
  }
  table {
    width: 100%;
    border-collapse: collapse;
  }
  thead th {
    text-align: left;
    padding: 0.3rem 0.6rem;
    border-bottom: 1px solid var(--border);
    color: var(--muted);
    font-size: 0.7rem;
    font-weight: 400;
    letter-spacing: 0.08em;
    text-transform: uppercase;
  }
  :global(tbody tr[data-slot] > *) {
    padding: 0.3rem 0.6rem;
    vertical-align: middle;
    border-bottom: 1px solid var(--border);
  }
  :global(tbody tr[data-slot] > td.actions) {
    text-align: right;
  }
  @media (max-width: 56.1875rem) {
    thead {
      position: absolute;
      width: 1px;
      height: 1px;
      overflow: hidden;
      clip: rect(0 0 0 0);
    }
    :global(tbody tr[data-slot]) {
      display: grid;
      gap: 0.25rem;
      padding: 0.5rem 0;
      border-bottom: 1px solid var(--border);
    }
    :global(tbody tr[data-slot] > *) {
      border-bottom: 0;
      padding: 0.15rem 0.25rem;
      min-width: 0;
    }
    :global(tbody tr[data-slot] > td[data-label]::before) {
      content: attr(data-label);
      display: block;
      color: var(--muted);
      font-size: 0.7rem;
      letter-spacing: 0.08em;
      text-transform: uppercase;
    }
  }
</style>
