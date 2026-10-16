<script lang="ts">
  // Inline stroke icons, 24x24. Decorative: the text next to them carries the meaning.
  const PATHS: Record<string, string> = {
    slots: 'M4 5h16v4H4zM4 11h16v4H4zM4 17h16v3H4z',
    endpoints: 'M5 12h14M13 6l6 6-6 6M5 6v12',
    logs: 'M5 4h14v16H5zM8 9h8M8 13h8M8 17h5',
    settings: 'M4 7h10M18 7h2M4 17h2M10 17h10M14 4v6M6 14v6',
    restart: 'M20 12a8 8 0 1 1-2.5-5.8M20 4v5h-5',
    logout: 'M10 4H5v16h5M15 8l4 4-4 4M19 12H9',
    general: 'M12 8v4l3 2M12 3a9 9 0 1 0 0 18 9 9 0 0 0 0-18z',
    route: 'M5 6a2 2 0 1 0 0 .01M19 18a2 2 0 1 0 0 .01M7 6h6a4 4 0 0 1 0 8h-2a4 4 0 0 0 0 4h6',
    shield: 'M12 3l7 3v5c0 5-3 8-7 10-4-2-7-5-7-10V6z',
    stats: 'M5 20V10M12 20V4M19 20v-7',
    chart: 'M4 4v16h16M8 15l3-4 3 2 4-6',
  };
  let { name, size = 16 }: { name: string; size?: number } = $props();
</script>

<svg
  width={size}
  height={size}
  viewBox="0 0 24 24"
  fill="none"
  stroke="currentColor"
  stroke-width="2"
  stroke-linecap="round"
  stroke-linejoin="round"
  aria-hidden="true"
  focusable="false"><path d={PATHS[name] ?? ''} /></svg
>

<style>
  svg {
    flex: none;
  }
</style>
