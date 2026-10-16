<script lang="ts">
  import { serviceLine } from '../lib/systemInfo';
  let { ok }: { ok: boolean | null } = $props();
</script>

<p class="system-status" data-testid="system-status">{serviceLine(ok)}</p>

<style>
  .system-status {
    margin: 0;
    font-size: 0.72rem;
    letter-spacing: 0.08em;
    text-transform: uppercase;
    color: var(--muted);
  }
</style>
