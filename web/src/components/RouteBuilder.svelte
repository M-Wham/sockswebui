<script lang="ts">
  // Route builder: endpoint library → ordered hop list. Native HTML5
  // drag-and-drop, with labelled Add / Move up / Move down / Remove buttons as the keyboard
  // path. Changes are announced via an aria-live region; route errors render inline per hop.
  import {
    add,
    hopErrors,
    library,
    nameOf,
    routeLevelErrors,
    type EditResult,
  } from '../lib/routeEditor';
  import type { EndpointView } from '../lib/endpointFilter';
  import HopList, { HOP_MIME } from './HopList.svelte';
  import Icon from './Icon.svelte';
  import { MAX_ROUTE_HOPS, validateRoute } from '../lib/routeRules';

  let {
    route = $bindable(),
    endpoints,
    serverHops = {},
    serverError = '',
  }: {
    route: string[];
    endpoints: EndpointView[];
    serverHops?: Record<number, string>;
    serverError?: string;
  } = $props();

  let announce = $state('');
  let refused = $state('');
  let pick = $state('');

  let lib = $derived(library(endpoints));
  let validation = $derived(validateRoute(route, endpoints));
  let perHop = $derived(hopErrors(validation));
  let routeErrors = $derived(routeLevelErrors(validation));
  let full = $derived(route.length >= MAX_ROUTE_HOPS);

  const label = (id: string) => nameOf(id, endpoints);
  const kindLabel = (k: string) => (k === 'wireguard' ? 'WireGuard' : k === 'http' ? 'HTTP' : 'SOCKS5');

  function apply(r: EditResult) {
    route = r.route;
    refused = r.refused ?? '';
    announce = r.announce;
  }

  function onDragStart(e: DragEvent, id: string) {
    if (!e.dataTransfer) return;
    e.dataTransfer.setData(HOP_MIME, JSON.stringify({ id }));
    e.dataTransfer.setData('text/plain', label(id));
    e.dataTransfer.effectAllowed = 'copy';
  }

  function addPicked() {
    if (!pick) return;
    apply(add(route, pick, endpoints));
    pick = '';
  }
</script>
<fieldset class="panel wide rb">
  <legend class="visually-hidden">Route</legend>
  <div class="head">
    <h3><Icon name="route" /> Route</h3>
    <span class="note">Entry at top · exit at bottom</span>
  </div>
  <p class="hint">
    Hops run in order from the device outwards. WireGuard hops must come first, then optional upstream
    proxies; at most {MAX_ROUTE_HOPS} hops. Drag endpoints into the route, or use the buttons.
  </p>
  <p class="visually-hidden" aria-live="polite" data-testid="route-announce">{announce}</p>
  {#if refused}<p class="form-error" role="alert">{refused}</p>{/if}

  <h4 id="rb-route">Route ({route.length}/{MAX_ROUTE_HOPS})</h4>
  {#each validation.warnings as w (w)}<p class="warn" data-testid="route-warning">{w}</p>{/each}
  {#each routeErrors as m (m)}<p class="field-error">{m}</p>{/each}
  {#if serverError}<p class="field-error">{serverError}</p>{/if}
  <HopList {route} {endpoints} {perHop} {serverHops} onapply={apply} />

  <div class="add">
    <label for="rb-pick">Add a hop</label>
    <select id="rb-pick" bind:value={pick} disabled={full}>
      <option value="">Choose an endpoint…</option>
      {#each lib as e (e.id)}<option value={e.id}>{e.name || e.id} ({kindLabel(e.kind)})</option>{/each}
    </select>
    <button type="button" class="secondary" disabled={!pick || full} onclick={addPicked}>Add hop</button>
  </div>

  <section aria-labelledby="rb-lib">
    <h4 id="rb-lib">Endpoints</h4>
    {#if lib.length === 0}
      <p class="hint">No endpoints yet. Import WireGuard configs or add a proxy on the <a href="#/endpoints">Endpoints</a> page.</p>
    {:else}
      <ul class="items lib">
        {#each lib as e (e.id)}
          <li draggable="true" ondragstart={(ev) => onDragStart(ev, e.id)}>
            <span class="name"><strong>{e.name || e.id}</strong><span class="kind">{kindLabel(e.kind)}</span></span>
            <button type="button" class="secondary" disabled={full} aria-label={`Add ${e.name || e.id} to route`}
              onclick={() => apply(add(route, e.id, endpoints))}>Add</button>
          </li>
        {/each}
      </ul>
    {/if}
  </section>
</fieldset>

<style>
  .rb {
    min-width: 0;
    max-width: none;
  }
  .head {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 0.25rem 0.5rem;
    margin: 0 0 0.5rem;
  }
  h3 {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    margin: 0;
  }
  .note {
    margin-left: auto;
    color: var(--muted);
    font-weight: 400;
    font-size: 0.75rem;
    letter-spacing: 0.05em;
    text-transform: none;
  }
  h4 {
    margin: 0.5rem 0;
    font-size: 0.85rem;
    text-transform: uppercase;
    letter-spacing: 0.1em;
  }
  .add {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 0.5rem;
    margin-bottom: 1rem;
  }
  .add select {
    flex: 1 1 12rem;
    min-width: 0;
  }
  .lib {
    margin: 0;
    padding: 0;
    list-style: none;
    display: grid;
    gap: 0.4rem;
  }
  @media (min-width: 48rem) {
    .lib {
      grid-template-columns: 1fr 1fr;
    }
  }
  .lib li {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 0.5rem;
    padding: 0.3rem 0.5rem;
    border: 1px solid var(--border);
    border-radius: 2px;
    background: var(--surface);
    cursor: grab;
  }
  .name {
    min-width: 0;
    overflow-wrap: anywhere;
  }
  .kind {
    display: block;
    color: var(--muted);
    font-size: 0.8rem;
  }
  .warn {
    border-left: 4px solid var(--error);
    padding: 0.25rem 0.5rem;
    margin: 0 0 0.5rem;
    font-weight: 600;
  }
</style>
