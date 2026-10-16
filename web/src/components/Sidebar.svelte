<script lang="ts">
  import { APP_NAME } from '../appName';
  import { hrefFor, type Route } from '../lib/router';
  import { versionLabel } from '../lib/systemInfo';
  import Icon from './Icon.svelte';
  import RestartAll from './RestartAll.svelte';
  import SystemFooter from './SystemFooter.svelte';

  let {
    route,
    serviceOk,
    appVersion,
    onLogout,
  }: { route: Route; serviceOk: boolean | null; appVersion: string | undefined; onLogout: () => void } = $props();

  const NAV: { label: string; icon: string; route: Route; names: Route['name'][] }[] = [
    { label: 'Slots', icon: 'slots', route: { name: 'dashboard' }, names: ['dashboard', 'slot'] },
    { label: 'Endpoints', icon: 'endpoints', route: { name: 'endpoints' }, names: ['endpoints', 'import'] },
    { label: 'Logs', icon: 'logs', route: { name: 'logs' }, names: ['logs'] },
    { label: 'Settings', icon: 'settings', route: { name: 'settings' }, names: ['settings'] },
  ];
</script>

<aside class="sidebar">
  <h1><a href="#/">{APP_NAME}</a></h1>
  <nav aria-label="Main">
    <ul>
      {#each NAV as item (item.label)}
        <li>
          <a href={hrefFor(item.route)} aria-current={item.names.includes(route.name) ? 'page' : undefined}
            ><Icon name={item.icon} />{item.label}</a
          >
        </li>
      {/each}
    </ul>
  </nav>
  <div class="side-foot">
    <SystemFooter ok={serviceOk} />
    <RestartAll />
    <button type="button" class="secondary logout" data-testid="logout" onclick={onLogout}
      ><Icon name="logout" />Log out</button
    >
    <p class="app-version" data-testid="app-version">{versionLabel(appVersion)}</p>
  </div>
</aside>

<style>
  .sidebar {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 0.25rem 0.75rem;
    width: 100%;
    padding: 0.5rem 0.75rem;
    background: var(--surface);
    border-bottom: 1px solid var(--border);
  }
  h1 {
    order: 1;
    flex: 1;
    margin: 0;
    font-size: 1.1rem;
  }
  h1 a {
    color: inherit;
    text-decoration: none;
  }
  /* Under 900 px the foot dissolves into the bar: row 1 brand + log out, row 2 tabs, row 3 status. */
  .side-foot {
    display: contents;
  }
  .logout {
    order: 2;
  }
  .side-foot > :global(.system-status) {
    order: 4;
  }
  .side-foot > :global(.restart-all) {
    order: 5;
  }
  .app-version {
    order: 6;
  }
  nav {
    order: 3;
    width: 100%;
    overflow-x: auto;
  }
  nav ul {
    display: flex;
    flex-wrap: nowrap;
    margin: 0;
    padding: 0;
    list-style: none;
  }
  nav a {
    display: inline-flex;
    align-items: center;
    gap: 0.3rem;
    min-height: 2.75rem;
    padding: 0 0.5rem;
    white-space: nowrap;
    border-bottom: 3px solid transparent;
    color: var(--fg);
    text-decoration: none;
    font-size: 0.72rem;
    letter-spacing: 0.08em;
    text-transform: uppercase;
  }
  nav a:hover {
    border-bottom-color: var(--border);
  }
  nav a[aria-current='page'] {
    border-bottom-color: var(--accent);
    font-weight: 700;
  }
  .logout {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    gap: 0.4rem;
    font-size: 0.78rem;
    letter-spacing: 0.1em;
    text-transform: uppercase;
  }
  .app-version {
    margin: 0;
    font-size: 0.72rem;
    color: var(--muted);
  }
  @media (max-width: 26rem) {
    nav a {
      gap: 0.2rem;
      padding: 0 0.3rem;
      font-size: 0.68rem;
      letter-spacing: 0.04em;
    }
  }
  @media (min-width: 56.25rem) {
    .sidebar {
      position: sticky;
      top: 0;
      flex-direction: column;
      flex-wrap: nowrap;
      align-items: stretch;
      flex: none;
      gap: 1rem;
      width: 11.875rem;
      height: 100vh;
      padding: 1rem 0;
      border-right: 1px solid var(--border);
      border-bottom: 0;
      overflow-y: auto;
    }
    h1 {
      flex: none;
      padding: 0 1rem;
    }
    nav {
      order: 2;
      overflow-x: visible;
    }
    nav ul {
      flex-direction: column;
    }
    nav a {
      min-height: 2.25rem;
      padding: 0 1rem;
      font-size: 0.78rem;
      letter-spacing: 0.14em;
      border-bottom: 0;
      border-left: 3px solid transparent;
    }
    nav a:hover {
      border-left-color: var(--border);
    }
    nav a[aria-current='page'] {
      border-left-color: var(--accent);
    }
    .logout,
    .side-foot > :global(.system-status),
    .side-foot > :global(.restart-all),
    .app-version {
      order: 0;
    }
    .side-foot {
      order: 3;
      display: grid;
      flex: none;
      margin-top: auto;
      padding: 0 1rem;
      gap: 0.5rem;
    }
  }
</style>
