<script lang="ts">
  // Persistent banner: the config file was corrupt at boot, slots are stopped.
  let { message }: { message: string } = $props();
</script>

{#if message}
  <div class="banner" role="alert">
    <strong>Configuration could not be loaded.</strong>
    All slots are stopped. The corrupt file was kept as <code>config.json.bad-&lt;timestamp&gt;</code>.
    <span class="detail">Error: {message}</span>
  </div>
{/if}

<style>
  .banner {
    padding: 0.75rem 1rem;
    background: var(--danger-bg);
    color: var(--danger-fg);
    overflow-wrap: anywhere;
  }
  .detail {
    display: block;
    font-family: ui-monospace, monospace;
    font-size: 0.9rem;
  }
  code {
    font-family: ui-monospace, monospace;
  }
</style>
