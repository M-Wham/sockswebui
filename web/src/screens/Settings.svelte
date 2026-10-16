<script lang="ts">
  import { onMount } from 'svelte';
  import { api, ApiError, type Settings } from '../lib/api';
  import { bindFlagNote, bindInUse, healthRows, mapSettingsErrors, saveSettings, showConfigError, START_ON_ENABLE_HELP } from '../lib/settingsForm';
  import ExportDialog from '../components/ExportDialog.svelte';
  import ImportDialog from '../components/ImportDialog.svelte';
  import ThemePicker from '../theme/ThemePicker.svelte';
  import ScreenHeader from '../components/ScreenHeader.svelte';
  import Icon from '../components/Icon.svelte';
  import ChangePasswordForm from '../components/ChangePasswordForm.svelte';

  let loaded = $state<Settings | null>(null);
  let bindAddr = $state('');
  let startOnEnable = $state(true);
  let fields = $state<Record<string, string>>({});
  let formError = $state('');
  let loadError = $state('');
  let notice = $state('');
  let busy = $state(false);

  onMount(async () => {
    try {
      loaded = await api.getSettings();
      bindAddr = loaded.ui_bind_addr;
      startOnEnable = loaded.start_on_enable ?? true;
    } catch (err) {
      loadError = err instanceof ApiError ? err.message : 'could not load settings';
    }
  });

  async function save(e: SubmitEvent) {
    e.preventDefault();
    busy = true;
    notice = '';
    try {
      loaded = await saveSettings(api.request, bindAddr, startOnEnable);
      bindAddr = loaded.ui_bind_addr;
      startOnEnable = loaded.start_on_enable ?? startOnEnable;
      fields = {};
      formError = '';
      notice = 'Saved. A changed bind address applies after the next restart.';
    } catch (err) {
      const m = mapSettingsErrors(err);
      fields = m.fields;
      formError = m.form;
    } finally {
      busy = false;
    }
  }
</script>

<ScreenHeader title="Settings" />
{#if loadError}<p class="form-error" role="alert">{loadError}</p>{/if}
{#if loaded}
  {#if showConfigError(loaded)}
    <p class="form-error" role="alert">Configuration error: {showConfigError(loaded)}</p>
  {/if}

  <div class="panels">
  <form class="panel" onsubmit={save} aria-labelledby="bind-title" novalidate>
    <h3 id="bind-title"><Icon name="settings" /> UI bind address</h3>
    <div class="field">
      <label for="bind-addr">Address (host:port)</label>
      <input id="bind-addr" type="text" autocomplete="off" bind:value={bindAddr}
        aria-invalid={fields.ui_bind_addr ? 'true' : undefined} aria-describedby={bindFlagNote(loaded) ? 'bind-err bind-note' : 'bind-err'} />
      {#if bindFlagNote(loaded)}
        <p id="bind-note" class="hint">
          {bindFlagNote(loaded)}{#if bindInUse(loaded)}. <strong>{bindInUse(loaded)}</strong>{/if}
        </p>
      {/if}
      <p id="bind-err" class="field-error">{fields.ui_bind_addr ?? ''}</p>
    </div>
    <h3 id="soe-title"><Icon name="slots" /> Slots</h3>
    <div class="field">
      <label for="start-on-enable"><input id="start-on-enable" type="checkbox" bind:checked={startOnEnable} aria-describedby="soe-help" /> Start slot when enabled</label>
      <p id="soe-help" class="hint">{START_ON_ENABLE_HELP}</p>
    </div>
    <div aria-live="polite">
      {#if formError}<p class="form-error">{formError}</p>{/if}
      {#if notice}<p class="hint" role="status">{notice}</p>{/if}
    </div>
    <button type="submit" disabled={busy}>{busy ? 'Saving…' : 'Save'}</button>
  </form>

  <section class="panel" aria-labelledby="health-title">
    <h3 id="health-title"><Icon name="stats" /> Health defaults</h3>
    <p class="hint">Read-only. Each slot can override these in its editor.</p>
    <dl>
      {#each healthRows(loaded.health_defaults) as row (row.label)}
        <dt>{row.label}</dt>
        <dd>{row.value}</dd>
      {/each}
    </dl>
  </section>

  <section class="panel" aria-labelledby="xfer-title">
    <h3 id="xfer-title"><Icon name="restart" /> Backup and restore</h3>
    <p class="hint">Move this device's endpoints and slots to another device. Export asks for your admin password.</p>
    <div class="row"><ExportDialog /><ImportDialog /></div>
  </section>

  <section class="panel" aria-labelledby="theme-title">
    <h3 id="theme-title"><Icon name="chart" /> Appearance</h3>
    <ThemePicker />
    <p class="hint">Saved in this browser only.</p>
  </section>

  <ChangePasswordForm bindAddr={loaded.ui_bind_addr} />
  </div>
{/if}

<style>
  .row {
    display: flex;
    flex-wrap: wrap;
    gap: 0.5rem;
    align-items: center;
  }
  dl {
    display: grid;
    grid-template-columns: auto 1fr;
    gap: 0.25rem 1rem;
    margin: 0;
  }
  dt {
    font-weight: 600;
  }
  dd {
    margin: 0;
    overflow-wrap: anywhere;
  }
</style>
