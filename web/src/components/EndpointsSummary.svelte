<script lang="ts">
  import type { EndpointView } from '../lib/endpointFilter';
  import type { SlotConfig } from '../lib/slotsStore';
  import { endpointLocation, endpointTypeLabel, summaryRows, usedBy } from '../lib/endpointSummary';
  import Icon from './Icon.svelte';

  let { endpoints, configs }: { endpoints: EndpointView[]; configs: SlotConfig[] } = $props();
  let summary = $derived(summaryRows(endpoints, configs));
</script>

<section class="panel" data-testid="endpoints-summary" aria-labelledby="eps-title">
  <div class="head">
    <h3 id="eps-title"><Icon name="endpoints" /> Endpoints</h3>
    <span class="total">{summary.total} total</span>
  </div>
  <div class="wrap">
    <table>
      <thead>
        <tr><th scope="col">Name</th><th scope="col">Type</th><th scope="col">Location</th><th scope="col">Used by</th></tr>
      </thead>
      <tbody>
        {#each summary.rows as e (e.id)}
          {@const users = usedBy(e.id, configs)}
          <tr>
            <th scope="row">{e.name || e.id}</th>
            <td data-label="Type">{endpointTypeLabel(e)}</td>
            <td data-label="Location">{endpointLocation(e)}</td>
            <td data-label="Used by">{users.length ? users.map((n) => `slot ${n}`).join(', ') : '—'}</td>
          </tr>
        {/each}
      </tbody>
    </table>
  </div>
  {#if summary.total > summary.rows.length}
    <p><a href="#/endpoints">View all {summary.total} endpoints</a></p>
  {/if}
</section>

<style>
  .panel {
    max-width: none;
  }
  .head {
    display: flex;
    justify-content: space-between;
    align-items: baseline;
    gap: 0.5rem;
  }
  .head h3 {
    margin: 0 0 0.5rem;
  }
  .total {
    color: var(--muted);
    font-family: var(--font-mono);
    font-size: 0.8rem;
  }
  .wrap {
    overflow-x: auto;
  }
  table {
    width: 100%;
    border-collapse: collapse;
  }
  th,
  td {
    padding: 0.3rem 0.6rem;
    text-align: left;
    border-bottom: 1px solid var(--border);
    white-space: nowrap;
  }
  thead th {
    color: var(--muted);
    font-size: 0.7rem;
    font-weight: 400;
    letter-spacing: 0.08em;
    text-transform: uppercase;
  }
  tbody th {
    font-weight: 600;
  }
  p {
    margin: 0.5rem 0 0;
  }
  @media (max-width: 39.9375rem) {
    thead {
      position: absolute;
      width: 1px;
      height: 1px;
      overflow: hidden;
      clip: rect(0 0 0 0);
    }
    tbody tr {
      display: grid;
      gap: 0.1rem;
      padding: 0.4rem 0;
      border-bottom: 1px solid var(--border);
    }
    th,
    td {
      min-width: 0;
      padding: 0.1rem 0.25rem;
      border-bottom: 0;
      white-space: normal;
      overflow-wrap: anywhere;
    }
    td[data-label]::before {
      content: attr(data-label) ' ';
      color: var(--muted);
      font-size: 0.7rem;
      letter-spacing: 0.08em;
      text-transform: uppercase;
    }
  }
</style>
