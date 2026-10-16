<script lang="ts">
  import { api, ApiError, type FieldError } from '../lib/api';
  import { killswitchBadge, stateBadge } from '../lib/badges';
  import { hrefFor } from '../lib/router';
  import { formatRate, type SlotConfig, type SlotLive } from '../lib/slotsStore';
  import StateBadge from './StateBadge.svelte';
  import RotateDialog from './RotateDialog.svelte';
  import type { EndpointView } from '../lib/endpointFilter';
  import { affectedSlots, lastTemplateHop } from '../lib/rotate';
  import { routeSummary } from '../lib/endpointSummary';

  let {
    config,
    live,
    onStatus,
    endpoints = [],
    slots = [],
    onRotated = () => {},
  }: {
    config: SlotConfig;
    live: SlotLive;
    onStatus: (body: unknown, seenSeq: number) => void;
    endpoints?: EndpointView[];
    slots?: SlotConfig[];
    onRotated?: () => void;
  } = $props();

  // The last template hop of the route is the one New IP rotates.
  let template = $derived(lastTemplateHop(config.route ?? [], endpoints));

  let busy = $state(false);
  let error = $state('');
  let fieldErrors = $state<FieldError[]>([]);

  let running = $derived(live.state === 'STARTING' || live.state === 'UP' || live.state === 'DEGRADED');
  let sBadge = $derived(stateBadge(live.state, config.killswitch));
  let kBadge = $derived(killswitchBadge(config.killswitch));
  // One line only: the slot's last error when it is failing, else the status message. Full text in title.
  let note = $derived(
    (live.state === 'DEGRADED' || live.state === 'ERROR') && live.last_error ? live.last_error : live.msg,
  );
  let title = $derived(config.name || live.name || `Slot ${config.id}`);

  // The badge only changes from the API response or an SSE frame, never optimistically.
  async function act(action: 'start' | 'stop') {
    busy = true;
    error = '';
    fieldErrors = [];
    const seenSeq = live.seq; // SSE frames arriving during the POST win over its response
    try {
      onStatus(await api.request('POST', `/api/slots/${config.id}/${action}`), seenSeq);
    } catch (err) {
      error = err instanceof ApiError ? err.message : `${action} failed`;
      fieldErrors = err instanceof ApiError ? err.fieldErrors : [];
    } finally {
      busy = false;
    }
  }
</script>

<tr data-slot={config.id} class:unconfigured={!config.configured}>
  {#if !config.configured}
    <th scope="row" data-label="Slot"><span class="slot-name">{config.id} · Not configured</span></th>
    <td class="empty" data-label="State">—</td>
    <td class="empty" data-label="Route">—</td>
    <td class="empty" data-label="Exit">—</td>
    <td class="empty" data-label="Latency">—</td>
    <td class="empty" data-label="Down / Up">—</td>
    <td class="actions"><div class="actions-in"><a href={hrefFor({ name: 'slot', id: config.id })}>Configure</a></div></td>
  {:else}
    <th scope="row" data-label="Slot">
      <span class="slot-name">{config.id} · {title}</span>
      <span class="addr"><span class="visually-hidden">SOCKS5 </span><code>{config.bind_addr}:{config.port}</code></span>
    </th>
    <td data-label="State">
      <div class="badges">
        <StateBadge badge={sBadge} kind="state" value={live.state ?? 'UNKNOWN'} />
        <StateBadge badge={kBadge} kind="killswitch" value={config.killswitch || 'block'} />
      </div>
      {#if note}<p class="msg" title={note}>{note}</p>{/if}
    </td>
    <td data-label="Route">{routeSummary(config.route ?? [], endpoints)}</td>
    <td data-label="Exit">{live.exit_ip ? `${live.exit_ip}${live.country ? ` · ${live.country.toUpperCase()}` : ''}` : '—'}</td>
    <td data-label="Latency">{live.latency_ms === null ? '—' : `${live.latency_ms} ms`}</td>
    <td data-label="Down / Up"
      ><span aria-label="download">↓ {formatRate(live.rx_rate)}</span> · <span aria-label="upload">↑ {formatRate(live.tx_rate)}</span></td
    >
    <td class="actions"><div class="actions-in">
      <a href={hrefFor({ name: 'slot', id: config.id })}>Edit</a>
      {#if template}<RotateDialog endpoint={template} affected={affectedSlots(template.id, slots)} {onRotated} />{/if}
      {#if running}
        <button type="button" class="secondary" disabled={busy} onclick={() => act('stop')}
          >{busy ? 'Stopping…' : 'Stop'}<span class="visually-hidden"> {title}</span></button
        >
      {:else}
        <button type="button" disabled={busy} onclick={() => act('start')}
          >{busy ? 'Starting…' : 'Start'}<span class="visually-hidden"> {title}</span></button
        >
      {/if}
      <div aria-live="polite">
        {#if error}<p class="form-error">{error}</p>{/if}
        {#each fieldErrors as fe (fe.field + fe.message)}
          <p class="field-error">{fe.field}: {fe.message}</p>
        {/each}
      </div>
    </div></td>
  {/if}
</tr>

<style>
  th {
    text-align: left;
    font-weight: 600;
  }
  .slot-name {
    display: block;
    overflow-wrap: anywhere;
  }
  .addr code {
    font-family: var(--font-mono);
    font-weight: 400;
    color: var(--muted);
  }
  .badges {
    display: flex;
    flex-wrap: wrap;
    gap: 0.25rem 0.4rem;
  }
  p {
    margin: 0.15rem 0 0;
  }
  .msg {
    font-size: 0.78rem;
    color: var(--muted);
    max-width: 24rem;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  tr.unconfigured .slot-name,
  td[data-label='Down / Up'] {
    white-space: nowrap;
  }
  .actions-in {
    display: flex;
    flex-wrap: nowrap;
    align-items: center;
    justify-content: flex-end;
    gap: 0.4rem 0.75rem;
  }
  .actions-in a {
    display: inline-flex;
    align-items: center;
    min-height: 2rem;
  }
  .actions-in div {
    flex-basis: 100%;
  }
  @media (max-width: 56.1875rem) {
    tr.unconfigured .slot-name,
    td[data-label='Down / Up'] {
      white-space: normal;
    }
    .actions-in {
      flex-wrap: wrap;
    }
    .msg {
      max-width: none;
      white-space: normal;
      overflow-wrap: anywhere;
    }
    .actions-in {
      justify-content: flex-start;
    }
    .actions-in a {
      min-height: 2.75rem;
    }
    tr.unconfigured {
      grid-template-columns: minmax(0, 1fr) auto;
      align-items: center;
    }
    td.empty {
      display: none;
    }
  }
</style>
