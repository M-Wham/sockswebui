<script lang="ts">
  import type { Badge } from '../lib/badges';

  // kind="state" sets data-state; kind="killswitch" sets data-killswitch (smoke-test hooks).
  let {
    badge,
    kind,
    value,
    showExplain = false,
  }: { badge: Badge; kind: 'state' | 'killswitch'; value: string; showExplain?: boolean } = $props();
</script>

<span class="badge-wrap" aria-live="polite">
  <span
    class="badge tag {badge.cls}"
    data-state={kind === 'state' ? value : undefined}
    data-killswitch={kind === 'killswitch' ? value : undefined}
    title={badge.explain}>{badge.label}</span
  >
  {#if showExplain || badge.cls === 'danger'}
    <span class="explain" class:danger-text={badge.cls === 'danger'}>{badge.explain}</span>
  {/if}
</span>

<style>
  .badge-wrap {
    display: inline-flex;
    flex-direction: column;
    gap: 0.15rem;
  }
  .badge {
    align-self: flex-start;
    padding: 0.15rem 0.5rem;
    border: 1px dashed var(--fg);
    border-radius: 0;
    background: transparent;
    color: var(--fg);
    font-family: var(--font-mono);
    font-size: 0.78rem;
    overflow-wrap: anywhere;
  }
  .badge.ok {
    border: 1px solid var(--tag-up);
    background: transparent;
    color: var(--tag-up);
  }
  .badge.danger {
    border: 2px solid var(--error);
    background: var(--danger-bg);
    color: var(--danger-fg);
    font-weight: 700;
  }
  .explain {
    font-size: 0.85rem;
    color: var(--muted);
  }
  .explain.danger-text {
    color: var(--error);
    font-weight: 600;
  }
</style>
