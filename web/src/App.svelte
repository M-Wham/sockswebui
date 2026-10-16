<script lang="ts">
  import { onMount } from 'svelte';
  import { APP_NAME } from './appName';
  import { api, ApiError } from './lib/api';
  import { navigate, parseRoute, type Route } from './lib/router';
  import { session } from './lib/session';
  import { live } from './lib/liveStream';
  import ConfigErrorBanner from './components/ConfigErrorBanner.svelte';
  import Sidebar from './components/Sidebar.svelte';
  import Dashboard from './screens/Dashboard.svelte';
  import Endpoints from './screens/Endpoints.svelte';
  import Login from './screens/Login.svelte';
  import Logs from './screens/Logs.svelte';
  import Settings from './screens/Settings.svelte';
  import Setup from './screens/Setup.svelte';
  import SlotEditor from './screens/SlotEditor.svelte';

  let route = $state<Route>(parseRoute(location.hash));
  let configError = $state('');
  let loggedIn = $state(session.token() !== null);
  let serviceOk = $state<boolean | null>(null);
  let appVersion = $state<string | undefined>(undefined);
  const showSidebar = $derived(loggedIn && !isPublic(route));

  function isPublic(r: Route): boolean {
    return r.name === 'login' || r.name === 'setup';
  }

  function sync() {
    route = parseRoute(location.hash);
    loggedIn = session.token() !== null;
    if (!loggedIn && !isPublic(route)) navigate('#/login');
  }

  async function loadSettings() {
    try {
      const s = await api.getSettings();
      configError = typeof s.config_error === 'string' ? s.config_error : '';
      appVersion = s.app_version;
      serviceOk ??= true; // first check only; the event stream owns it after that
    } catch (e) {
      // 401/403 already routed to login; other failures leave the banner state as is.
      if (!(e instanceof ApiError && (e.status === 401 || e.status === 403))) serviceOk = false;
    }
  }

  function onLoggedIn() {
    loggedIn = true;
    serviceOk = null; // a stream error during logout must not stick
    navigate('#/');
    void loadSettings();
  }

  // Footer follows the shared event stream while signed in: connected is running, a drop is not.
  $effect(() => {
    if (!loggedIn) return;
    return live.subscribe({
      onChange: () => (serviceOk = live.connected()),
      onClosed: () => void loadSettings(), // a closed stream is often an expired session: 401 routes to login
    });
  });

  async function logout() {
    try {
      await api.logout();
    } catch {
      // token is cleared and we are routed to login either way
    }
    configError = '';
    serviceOk = null;
    appVersion = undefined;
  }

  onMount(() => {
    window.addEventListener('hashchange', sync);
    sync();
    if (loggedIn) void loadSettings();
    return () => window.removeEventListener('hashchange', sync);
  });
</script>

<ConfigErrorBanner message={configError} />

<div class="shell" class:with-sidebar={showSidebar}>
  {#if showSidebar}
    <Sidebar {route} {serviceOk} {appVersion} onLogout={logout} />
  {:else}
    <header>
      <h1><a href="#/">{APP_NAME}</a></h1>
    </header>
  {/if}

<main>
  {#if route.name === 'setup'}
    <Setup {onLoggedIn} />
  {:else if route.name === 'login' || !loggedIn}
    <Login {onLoggedIn} />
  {:else if route.name === 'dashboard'}
    <Dashboard />
  {:else if route.name === 'endpoints'}
    <Endpoints routeId={route.id} />
  {:else if route.name === 'import'}
    <Endpoints focusImport />
  {:else if route.name === 'slot'}
    {#key route.id}<SlotEditor id={route.id} />{/key}
  {:else if route.name === 'logs'}
    <Logs />
  {:else if route.name === 'settings'}
    <Settings />
  {/if}
</main></div>

<style>
  .shell {
    display: flex;
    flex-direction: column;
    min-height: 100vh;
  }
  header {
    padding: 0.5rem 0.75rem;
    border-bottom: 1px solid var(--border);
  }
  h1 {
    margin: 0;
    font-size: 1.1rem;
  }
  h1 a {
    color: inherit;
    text-decoration: none;
  }
  @media (min-width: 56.25rem) {
    .shell.with-sidebar {
      flex-direction: row;
      align-items: flex-start;
    }
    .shell > :global(main) {
      flex: 1;
      min-width: 0;
    }
    header {
      padding: 0.5rem 2rem;
    }
  }
</style>
