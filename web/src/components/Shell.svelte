<script>
  import { onMount } from 'svelte';
  import { ui, data, loadProjects, loadApps, loadDocker } from '../lib/state.svelte.js';
  import Rail from './Rail.svelte';
  import Sidebar from './Sidebar.svelte';
  import Home from './Home.svelte';
  import ProjectHome from './ProjectHome.svelte';
  import NewApp from './NewApp.svelte';
  import AppView from './AppView.svelte';
  import Settings from './Settings.svelte';
  import DetailPanel from './DetailPanel.svelte';
  import UpdateBanner from './UpdateBanner.svelte';
  import DockerBanner from './DockerBanner.svelte';
  import MemoryPrompt from './MemoryPrompt.svelte';
  import UpdateDialog from './UpdateDialog.svelte';
  import { isAdmin } from '../lib/state.svelte.js';
  import { upd, loadUpdate, follow } from '../lib/update.svelte.js';

  // Docker's health: checked every 10 s (a cheap request that never waits
  // on Docker), so a stuck Docker shows a banner instead of empty lists.
  onMount(() => {
    loadDocker();
    const t = setInterval(loadDocker, 10000);
    return () => clearInterval(t);
  });

  onMount(() => {
    loadProjects();
    if (!isAdmin()) return;
    // The server checks GitHub itself every few hours; this only reads
    // what it found.
    loadUpdate().then((st) => {
      // Reopened the page mid-update: keep following it.
      const j = st?.job;
      if (j && (j.state === 'running' || j.state === 'restarting') && !upd.progress) {
        upd.progress = { target: j.to, phase: 'working', step: j.step, error: '' };
        follow(j.to);
      }
    });
    const t = setInterval(loadUpdate, 30 * 60 * 1000);
    return () => clearInterval(t);
  });

  // Reload the sidebar's app list whenever the project changes, and poll
  // it so status dots stay current.
  $effect(() => {
    const pid = ui.route.projectId;
    loadApps(pid).catch(() => {});
    if (!pid) return;
    const t = setInterval(() => loadApps(pid).catch(() => {}), 10000);
    return () => clearInterval(t);
  });

  let showPanel = $derived(ui.route.view === 'app' && ui.panelOpen);
</script>

<div
  class="shell"
  class:with-panel={showPanel}
  class:nav-open={ui.navOpen}
  style={ui.panelWidth ? `--sp-panel-width: ${ui.panelWidth}px` : undefined}
>
  <div class="nav">
    <Rail />
    <Sidebar />
  </div>
  {#if ui.navOpen}
    <button class="scrim" aria-label="Close menu" onclick={() => (ui.navOpen = false)}></button>
  {/if}
  <main class="main">
    <DockerBanner />
    <UpdateBanner />
    {#if ui.route.view === 'home'}
      <Home />
    {:else if ui.route.view === 'settings'}
      <Settings />
    {:else if ui.route.view === 'project'}
      {#key ui.route.projectId}<ProjectHome />{/key}
    {:else if ui.route.view === 'new-app'}
      {#key ui.route.projectId}<NewApp />{/key}
    {:else if ui.route.view === 'app'}
      {#key ui.route.appId}<AppView />{/key}
    {/if}
  </main>
  {#if showPanel}
    {#key ui.route.appId}<DetailPanel />{/key}
  {/if}
</div>
<MemoryPrompt />
{#if upd.open && (upd.status?.check?.latest || upd.progress)}
  <UpdateDialog />
{/if}

<style>
  .shell {
    height: 100%;
    display: grid;
    grid-template-columns: auto minmax(0, 1fr);
    background: var(--sp-bg-main);
  }
  .shell.with-panel {
    grid-template-columns: auto minmax(0, 1fr) var(--sp-panel-width);
  }
  .nav {
    display: flex;
    height: 100%;
    min-height: 0;
  }
  .main {
    min-width: 0;
    min-height: 0;
    overflow: auto;
    display: flex;
    flex-direction: column;
  }
  .scrim {
    display: none;
  }

  @media (max-width: 1100px) {
    .shell.with-panel {
      grid-template-columns: auto minmax(0, 1fr);
    }
  }

  @media (max-width: 760px) {
    .shell,
    .shell.with-panel {
      grid-template-columns: minmax(0, 1fr);
    }
    .nav {
      position: fixed;
      inset: 0 auto 0 0;
      z-index: 20;
      transform: translateX(-100%);
      transition: transform var(--sp-transition);
      box-shadow: var(--sp-shadow);
    }
    .nav-open .nav {
      transform: none;
    }
    .scrim {
      display: block;
      position: fixed;
      inset: 0;
      z-index: 15;
      border: 0;
      background: var(--sp-overlay);
    }
  }
</style>
