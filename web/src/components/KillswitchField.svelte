<script lang="ts">
  // Kill-switch policy. "block" is the fail-closed default. Choosing
  // allow-direct only opens a red confirm; the value stays "block" until confirmed.
  import { tick } from 'svelte';
  import {
    cancelAllowDirect,
    confirmAllowDirect,
    requestKillswitch,
    type Killswitch,
    type KillswitchState,
  } from '../lib/slotForm';
  import Icon from './Icon.svelte';

  let { ks = $bindable(), error = '' }: { ks: KillswitchState; error?: string } = $props();

  const uid = Math.random().toString(36).slice(2, 8);
  const id = (f: string) => `ks-${uid}-${f}`;
  let blockInput: HTMLInputElement | undefined = $state();
  let allowInput: HTMLInputElement | undefined = $state();
  let confirmBox: HTMLElement | undefined = $state();

  async function onChange(requested: Killswitch) {
    ks = requestKillswitch(ks, requested);
    syncRadios();
    if (ks.pending) {
      await tick();
      confirmBox?.focus();
    }
  }

  // The browser has already checked the clicked radio; put it back to the real value.
  function syncRadios() {
    if (blockInput) blockInput.checked = ks.value === 'block';
    if (allowInput) allowInput.checked = ks.value === 'allow-direct';
  }

  function confirm() {
    ks = confirmAllowDirect(ks);
    syncRadios();
    allowInput?.focus();
  }

  function cancel() {
    ks = cancelAllowDirect(ks);
    syncRadios();
    blockInput?.focus();
  }
</script>

<fieldset class="panel wide ks" aria-describedby={error ? id('err') : undefined}>
  <legend class="visually-hidden">Kill switch</legend>
  <h3><Icon name="shield" /> Kill switch</h3>
  <div class="cards">
    <label class="opt">
      <input bind:this={blockInput} type="radio" name={id('ks')} value="block" checked={ks.value === 'block'}
        onchange={() => onChange('block')} />
      <span><strong>Block · recommended</strong>
        <span class="expl">If the route fails, traffic stops. Nothing leaks via the device.</span></span>
    </label>
    <label class="opt allow">
      <input bind:this={allowInput} type="radio" name={id('ks')} value="allow-direct"
        checked={ks.value === 'allow-direct'} onchange={() => onChange('allow-direct')} />
      <span><strong>Allow direct</strong>
        <span class="expl">Fall back to the device's own connection when the route is down.</span></span>
    </label>
  </div>

  {#if ks.pending}
    <div class="danger-box" role="alertdialog" aria-labelledby={id('ct')} aria-describedby={id('cd')}
      tabindex="-1" bind:this={confirmBox}>
      <p id={id('ct')}><strong>Allow direct connections?</strong></p>
      <p id={id('cd')}>
        When this slot is DEGRADED, new connections go out through the device's own route, with no VPN or
        proxy, and your real IP address is exposed. Every fallback connection is logged.
      </p>
      <div class="actions">
        <button type="button" class="danger" onclick={confirm}>Yes, allow direct fallback</button>
        <button type="button" class="secondary" onclick={cancel}>Keep block</button>
      </div>
    </div>
  {:else if ks.value === 'allow-direct'}
    <p class="danger-box" data-testid="allow-direct-warning">
      <strong>allow-direct is on.</strong> When this slot is DEGRADED it sends traffic through the device's
      own route (no VPN), and every fallback is logged.
    </p>
  {/if}
  {#if error}<p class="field-error" id={id('err')}>{error}</p>{/if}
</fieldset>

<style>
  .ks {
    max-width: none;
    min-width: 0;
  }
  h3 {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    margin: 0 0 0.75rem;
  }
  .cards {
    display: grid;
    gap: 0.75rem;
  }
  @media (min-width: 48rem) {
    .cards {
      grid-template-columns: 1fr 1fr;
    }
  }
  .opt {
    display: flex;
    gap: 0.5rem;
    align-items: flex-start;
    padding: 0.6rem 0.75rem;
    border: 1px solid var(--border);
    border-radius: 2px;
    background: var(--surface);
    cursor: pointer;
  }
  .opt:has(input:checked) {
    border: 2px solid var(--accent);
    padding: calc(0.6rem - 1px) calc(0.75rem - 1px);
  }
  .opt.allow {
    border-left: 4px solid var(--error);
  }
  .opt.allow:has(input:checked) {
    border-left: 4px solid var(--error);
  }
  .opt input {
    width: auto;
    min-height: 1.25rem;
    margin-top: 0.2rem;
  }
  .expl {
    display: block;
    color: var(--muted);
    font-size: 0.85rem;
  }
  .danger-box {
    border: 2px solid var(--error);
    background: var(--danger-bg);
    color: var(--danger-fg);
    border-radius: 2px;
    padding: 0.75rem;
    margin: 0.5rem 0 0;
  }
  .danger-box p {
    margin: 0 0 0.5rem;
  }
  .actions {
    display: flex;
    flex-wrap: wrap;
    gap: 0.5rem;
  }
  .danger {
    background: var(--danger-fg);
    color: var(--danger-bg);
    border-color: var(--danger-fg);
    font-weight: 600;
  }
  .danger-box .secondary {
    color: var(--danger-fg);
    border-color: var(--danger-fg);
  }
</style>
