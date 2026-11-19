<script>
  import { ui, isAdmin, go } from '../lib/state.svelte.js';
  import { settingsSections } from '../lib/settings.js';
  import Topbar from './Topbar.svelte';
  import ConnectAI from './ConnectAI.svelte';
  import General from './settings/General.svelte';
  import Users from './settings/Users.svelte';
  import Backups from './settings/Backups.svelte';
  import Notifications from './settings/Notifications.svelte';
  import Monitoring from './settings/Monitoring.svelte';
  import Account from './settings/Account.svelte';
  import Git from './settings/Git.svelte';
  import Registries from './settings/Registries.svelte';
  import ApiCli from './settings/ApiCli.svelte';

  let sections = $derived(settingsSections(isAdmin()));
  let current = $derived(sections.find((s) => s[0] === ui.route.section) ? ui.route.section : sections[0][0]);
  let title = $derived(sections.find((s) => s[0] === current)?.[1]);
</script>

<Topbar>Settings · {title}</Topbar>
<nav class="mobile-tabs" aria-label="Settings sections">
  {#each sections as [key, label] (key)}
    <a href="#/settings/{key}" class:active={current === key}>{label}</a>
  {/each}
</nav>
<div class="page">
  {#if current === 'general'}
    <General />
  {:else if current === 'users'}
    <Users />
  {:else if current === 'backups'}
    <Backups />
  {:else if current === 'notifications'}
    <Notifications />
  {:else if current === 'monitoring'}
    <Monitoring />
  {:else if current === 'git'}
    <Git />
  {:else if current === 'registries'}
    <Registries />
  {:else if current === 'api'}
    <ApiCli />
  {:else if current === 'ai'}
    <ConnectAI />
  {:else}
    <Account />
  {/if}
</div>

<style>
  .page {
    padding: var(--sp-space-5);
    display: flex;
    flex-direction: column;
    gap: var(--sp-space-4);
    max-width: 860px;
    width: 100%;
  }
  .mobile-tabs {
    display: none;
    gap: var(--sp-space-1);
    padding: 0 var(--sp-space-4);
    border-bottom: 1px solid var(--sp-border);
    overflow-x: auto;
    flex: none;
  }
  .mobile-tabs a {
    padding: var(--sp-space-3);
    color: var(--sp-text-muted);
    font-size: var(--sp-text-sm);
    border-bottom: 2px solid transparent;
    white-space: nowrap;
    text-decoration: none;
  }
  .mobile-tabs a.active {
    color: var(--sp-text);
    border-bottom-color: var(--sp-accent);
  }
  .page :global(.card) {
    display: flex;
    flex-direction: column;
    gap: var(--sp-space-3);
  }
  .page :global(.ok) {
    color: var(--sp-success);
    font-size: var(--sp-text-sm);
  }
  .page :global(.between) {
    justify-content: space-between;
    align-items: center;
  }
  .page :global(.end) {
    justify-content: flex-end;
    align-items: center;
  }
  @media (max-width: 760px) {
    .mobile-tabs {
      display: flex;
    }
  }
</style>
