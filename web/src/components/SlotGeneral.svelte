<script lang="ts">
  // General panel of the slot editor: name, listener and SOCKS authentication (write-only password).
  import { passwordStatus } from '../lib/proxyForm';
  import { BLOCK_LAN_LABEL, blockLanToggleId, type SlotErrors, type SlotForm } from '../lib/slotForm';
  import Icon from './Icon.svelte';

  let {
    id,
    form = $bindable(),
    errors,
    openWarnings,
    showBlockLan,
    onBlockLan,
  }: {
    id: number;
    form: SlotForm;
    errors: SlotErrors;
    openWarnings: string[];
    showBlockLan: boolean;
    onBlockLan: (e: MouseEvent) => void;
  } = $props();

  const fid = (f: string) => `slot-${id}-${f.replace(/\W/g, '-')}`;
</script>

{#snippet err(f: string)}
  {#if errors.fields[f]}<p class="field-error" id={fid(f) + '-err'}>{errors.fields[f]}</p>{/if}
{/snippet}

<fieldset class="panel wide">
  <legend class="visually-hidden">General</legend>
  <h3><Icon name="general" /> General</h3>
  <div class="field">
    <label for={fid('name')}>Name</label>
    <input id={fid('name')} bind:value={form.name} aria-invalid={!!errors.fields.name}
      aria-describedby={errors.fields.name ? fid('name') + '-err' : undefined} />
    {@render err('name')}
  </div>
  <div class="row">
    <div class="field grow">
      <label for={fid('bind_addr')}>Bind address</label>
      <input id={fid('bind_addr')} bind:value={form.bindAddr} autocomplete="off" inputmode="decimal"
        aria-invalid={!!errors.fields.bind_addr}
        aria-describedby={errors.fields.bind_addr ? fid('bind_addr') + '-err' : fid('bind-hint')} />
      <span class="hint" id={fid('bind-hint')}>127.0.0.1 = this device only; 0.0.0.0 = whole network.</span>
      {@render err('bind_addr')}
      {#each openWarnings as w}
        <p class="warn" data-testid="open-proxy-warning">Warning: {w}
          {#if showBlockLan}<a href={'#' + blockLanToggleId(id)} onclick={onBlockLan}
              data-testid="block-lan-link">Go to "{BLOCK_LAN_LABEL}"</a>{/if}</p>
      {/each}
    </div>
    <div class="field port">
      <label for={fid('port')}>Port</label>
      <input id={fid('port')} bind:value={form.port} inputmode="numeric" aria-invalid={!!errors.fields.port}
        aria-describedby={errors.fields.port ? fid('port') + '-err' : undefined} />
      {@render err('port')}
    </div>
    {#if form.authEnabled}
      <div class="field grow">
        <label for={fid('socks_auth.username')}>Username</label>
        <input id={fid('socks_auth.username')} bind:value={form.authUsername} autocomplete="off"
          aria-invalid={!!errors.fields['socks_auth.username']}
          aria-describedby={errors.fields['socks_auth.username'] ? fid('socks_auth.username') + '-err' : undefined} />
        {@render err('socks_auth.username')}
      </div>
      <div class="field grow">
        <label for={fid('socks_auth.password')}>
          Password <span class="hint">(currently {passwordStatus(form.authPasswordSet)})</span>
        </label>
        <input id={fid('socks_auth.password')} type="password" bind:value={form.authPassword}
          autocomplete="new-password" placeholder={form.authPasswordSet ? 'leave empty to keep' : ''}
          aria-invalid={!!errors.fields['socks_auth.password']}
          aria-describedby={errors.fields['socks_auth.password'] ? fid('socks_auth.password') + '-err' : undefined} />
        {@render err('socks_auth.password')}
      </div>
    {/if}
  </div>
  <label class="check"><input type="checkbox" bind:checked={form.authEnabled} /> Require username and password</label>
</fieldset>

<style>
  fieldset {
    max-width: none;
    min-width: 0;
  }
  h3 {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    margin: 0 0 0.75rem;
  }
  .warn {
    border-left: 4px solid var(--error);
    padding: 0.25rem 0.5rem;
    margin: 0.25rem 0 0;
    font-weight: 600;
  }
  .row {
    display: flex;
    flex-wrap: wrap;
    align-items: flex-start;
    gap: 0 0.75rem;
  }
  .grow {
    flex: 1 1 12rem;
  }
  .port {
    flex: 0 1 7rem;
  }
  .check {
    display: flex;
    align-items: center;
    gap: 0.5rem;
  }
  .check input {
    width: auto;
    min-height: 1.25rem;
  }
</style>
