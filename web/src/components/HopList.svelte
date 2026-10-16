<script module lang="ts">
  export const HOP_MIME = 'application/x-sockswebui-hop';
</script>

<script lang="ts">
  // The ordered hop list: native drag-and-drop plus labelled Move/Remove buttons.
  // Edits are reported through onapply; the parent owns the route and the announce region.
  import type { EndpointView } from '../lib/endpointFilter';
  import { endpointLocation, endpointTypeLabel } from '../lib/endpointSummary';
  import { drop, moveDown, moveUp, nameOf, remove, type EditResult } from '../lib/routeEditor';

  let {
    route,
    endpoints,
    perHop,
    serverHops = {},
    onapply,
  }: {
    route: string[];
    endpoints: EndpointView[];
    perHop: Map<number, string[]>;
    serverHops?: Record<number, string>;
    onapply: (r: EditResult) => void;
  } = $props();

  let dropAt = $state<number | null>(null);

  const label = (id: string) => nameOf(id, endpoints);
  function subtitle(id: string): string {
    const e = endpoints.find((x) => x.id === id);
    return e ? `${endpointTypeLabel(e)} · ${endpointLocation(e)}` : 'unknown endpoint';
  }

  function onDragStart(e: DragEvent, id: string, from: number) {
    if (!e.dataTransfer) return;
    e.dataTransfer.setData(HOP_MIME, JSON.stringify({ id, from }));
    e.dataTransfer.setData('text/plain', label(id));
    e.dataTransfer.effectAllowed = 'move';
  }

  function onDragOver(e: DragEvent, index: number) {
    if (!e.dataTransfer?.types.includes(HOP_MIME)) return;
    e.preventDefault();
    dropAt = index;
  }

  function onDrop(e: DragEvent, index: number) {
    e.preventDefault();
    dropAt = null;
    const raw = e.dataTransfer?.getData(HOP_MIME);
    if (!raw) return;
    try {
      const d = JSON.parse(raw) as { id?: unknown; from?: unknown };
      if (typeof d.id !== 'string') return;
      const from = typeof d.from === 'number' ? d.from : undefined;
      onapply(drop(route, d.id, index, endpoints, from));
    } catch {
      // not ours
    }
  }

  function hopMessages(i: number): string[] {
    const out = [...(perHop.get(i) ?? [])];
    if (serverHops[i]) out.push(serverHops[i]);
    return out;
  }
</script>

<ol class="items route" aria-label="Route hops"
  ondragover={(ev) => onDragOver(ev, route.length)} ondrop={(ev) => onDrop(ev, route.length)}
  ondragleave={() => (dropAt = null)}>
  {#each route as id, i (`${i}:${id}`)}
    {@const msgs = hopMessages(i)}
    <li draggable="true" class:invalid={msgs.length > 0} class:drop-before={dropAt === i}
      ondragstart={(ev) => onDragStart(ev, id, i)}
      ondragover={(ev) => { ev.stopPropagation(); onDragOver(ev, i); }}
      ondrop={(ev) => { ev.stopPropagation(); onDrop(ev, i); }}
      aria-describedby={msgs.length ? `rb-hop-err-${i}` : undefined}>
      <span class="name"><span class="pos">{i + 1}</span> <strong>{label(id)}</strong><span class="sub">{subtitle(id)}</span></span>
      <span class="btns">
        <button type="button" class="secondary" disabled={i === 0} aria-label={`Move ${label(id)} up`}
          onclick={() => onapply(moveUp(route, i, endpoints))}>↑</button>
        <button type="button" class="secondary" disabled={i === route.length - 1}
          aria-label={`Move ${label(id)} down`} onclick={() => onapply(moveDown(route, i, endpoints))}>↓</button>
        <button type="button" class="secondary" aria-label={`Remove ${label(id)}`}
          onclick={() => onapply(remove(route, i, endpoints))}>✕</button>
      </span>
      {#if msgs.length}<p class="field-error hop-err" id={`rb-hop-err-${i}`}>{msgs.join('; ')}</p>{/if}
    </li>
  {/each}
  <li class="drop-end" class:drop-before={dropAt === route.length} aria-hidden="true">
    {route.length === 0 ? 'Drop endpoints here' : 'Drop here to add at the end'}
  </li>
</ol>
<p class="exit" data-testid="route-exit"><span aria-hidden="true">↓</span> Internet</p>

<style>
  .items {
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .items li {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 0.5rem;
    padding: 0.4rem 0.5rem;
    margin-bottom: 0.4rem;
    border: 1px solid var(--border);
    border-radius: 2px;
    background: var(--surface);
    cursor: grab;
  }
  .items li.invalid {
    border: 2px solid var(--error);
  }
  .items li.drop-before {
    box-shadow: 0 -3px 0 var(--focus);
  }
  .pos {
    margin-right: 0.25rem;
    vertical-align: middle;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    min-width: 1.75rem;
    height: 1.75rem;
    border: 1px solid var(--border);
    color: var(--muted);
    font-size: 0.85rem;
  }
  .name {
    flex: 1 1 8rem;
    min-width: 0;
    overflow-wrap: anywhere;
  }
  .sub {
    display: block;
    margin-left: 2.25rem;
    color: var(--muted);
    font-size: 0.8rem;
  }
  .btns {
    display: flex;
    gap: 0.25rem;
  }
  .btns button {
    padding: 0.25rem 0.6rem;
  }
  .hop-err {
    flex-basis: 100%;
    margin: 0;
  }
  .items li.drop-end {
    justify-content: center;
    border-style: dashed;
    background: transparent;
    color: var(--muted);
    cursor: default;
  }
  .exit {
    margin: 0 0 0.75rem;
    padding: 0.3rem 0.5rem;
    border: 1px solid var(--border);
    color: var(--muted);
    text-transform: uppercase;
    letter-spacing: 0.1em;
    font-size: 0.8rem;
  }
</style>
