<script lang="ts">
  import type { Snippet } from 'svelte';
  let {
    title,
    back,
    backLabel = 'Back',
    tag,
    actions,
  }: { title: string; back?: string; backLabel?: string; tag?: Snippet; actions?: Snippet } = $props();
</script>

<header class="screen-header">
  {#if back}<a class="back" href={back} aria-label={backLabel}><span aria-hidden="true">←</span> Back</a>{/if}
  <div class="row">
    <h2>{title}</h2>
    {#if tag}<span class="tag">{@render tag()}</span>{/if}
    {#if actions}<div class="actions">{@render actions()}</div>{/if}
  </div>
</header>

<style>
  .screen-header {
    margin: 0 0 0.75rem;
  }
  .back {
    display: inline-flex;
    align-items: center;
    gap: 0.4rem;
    min-width: 2rem;
    min-height: 2rem;
    margin-bottom: 0.25rem;
    font-size: 0.8rem;
  }
  .row {
    display: flex;
    flex-wrap: wrap;
    align-items: baseline;
    gap: 0.5rem 0.75rem;
  }
  h2 {
    margin: 0;
  }
  .tag {
    color: var(--muted);
    font-family: var(--font-mono);
  }
  .actions {
    margin-left: auto;
    display: flex;
    gap: 0.5rem;
  }
</style>
