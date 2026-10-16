<script lang="ts">
  import { THEMES, THEME_LABELS, applyTheme, loadTheme, type ThemeName } from "./theme";

  let current: ThemeName = $state(loadTheme());

  function pick(e: Event) {
    current = (e.currentTarget as HTMLSelectElement).value as ThemeName;
    applyTheme(current);
  }
</script>

<label for="theme-picker">Theme</label>
<select id="theme-picker" value={current} onchange={pick}>
  {#each THEMES as t (t)}
    <option value={t}>{THEME_LABELS[t]}</option>
  {/each}
</select>
