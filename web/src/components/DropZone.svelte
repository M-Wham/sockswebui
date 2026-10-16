<script lang="ts">
  // Labelled drop region for .conf/.zip files with a visible file picker as the
  // keyboard/button path. Filtering and size checks happen in importFiles.ts.
  import Icon from './Icon.svelte';
  let { onFiles, busy = false }: { onFiles: (files: File[]) => void; busy?: boolean } = $props();

  let over = $state(false);
  let input: HTMLInputElement | undefined = $state();

  function take(list: FileList | null | undefined) {
    const files = list ? Array.from(list) : [];
    if (files.length > 0 && !busy) onFiles(files);
  }

  function onDrop(e: DragEvent) {
    e.preventDefault();
    over = false;
    take(e.dataTransfer?.files);
  }

  function onDragOver(e: DragEvent) {
    e.preventDefault();
    if (e.dataTransfer) e.dataTransfer.dropEffect = busy ? 'none' : 'copy';
    over = true;
  }

  function onPick() {
    take(input?.files);
    if (input) input.value = ''; // allow re-picking the same file
  }
</script>

<section
  class="panel zone"
  class:over
  aria-labelledby="dropzone-title"
  aria-describedby="dropzone-hint"
  aria-busy={busy}
  ondragover={onDragOver}
  ondragleave={() => (over = false)}
  ondrop={onDrop}
>
  <h3 id="dropzone-title"><Icon name="endpoints" /> Import WireGuard configs</h3>
  <p id="dropzone-hint" class="hint">
    Drop WireGuard <code>.conf</code> files (any provider) or a <code>.zip</code> here (up to 5 MB per upload), or choose files:
  </p>
  <label class="picker">
    <span>Choose .conf / .zip files</span>
    <input
      bind:this={input}
      type="file"
      multiple
      accept=".conf,.zip"
      data-testid="import-input"
      disabled={busy}
      onchange={onPick}
    />
  </label>
  {#if busy}<p class="hint" role="status">Importing…</p>{/if}
</section>

<style>
  .zone {
    border: 2px dashed var(--border);
    border-radius: 0.5rem;
    background: var(--surface);
  }
  .zone.over {
    border-color: var(--accent);
    border-style: solid;
  }
  .picker {
    display: grid;
    gap: 0.25rem;
    font-weight: 600;
  }
  .picker input {
    font-weight: normal;
  }
</style>
