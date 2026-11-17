<script>
  import { api } from '../lib/api.js';
  import { ui, data, go, initials, loadProjects, toggleTheme, isAdmin } from '../lib/state.svelte.js';
  import Logo from './Logo.svelte';
  import Icon from './Icon.svelte';

  let adding = $state(false);
  let name = $state('');
  let error = $state('');

  async function create(e) {
    e.preventDefault();
    error = '';
    try {
      const p = await api.post('/projects', { name: name.trim().toLowerCase() });
      await loadProjects();
      adding = false;
      name = '';
      go(`/p/${p.id}`);
    } catch (err) {
      error = err.message;
    }
  }
</script>

<nav class="rail" aria-label="Projects">
  <button class="item" class:active={ui.route.view === 'home'} title="Overview" onclick={() => go('/')}>
    <span class="pill"></span>
    <span class="icon logo"><Logo size={48} /></span>
  </button>
  <div class="sep"></div>

  <div class="projects">
    {#each data.projects as p (p.id)}
      <button class="item" class:active={ui.route.projectId === p.id} title={p.name} onclick={() => go(`/p/${p.id}`)}>
        <span class="pill"></span>
        <span class="icon">{initials(p.name)}</span>
      </button>
    {/each}
    {#if isAdmin()}
      <button class="item" title="New project" onclick={() => { adding = !adding; error = ''; }}>
        <span class="pill"></span>
        <span class="icon add"><Icon name="plus" /></span>
      </button>
    {/if}
  </div>

  <div class="bottom">
    <button class="item" title={ui.theme === 'dark' ? 'Switch to light mode' : 'Switch to dark mode'} onclick={toggleTheme}>
      <span class="pill"></span>
      <span class="icon small"><Icon name={ui.theme === 'dark' ? 'sun' : 'moon'} /></span>
    </button>
    <button class="item" class:active={ui.route.view === 'settings'} title="Settings" onclick={() => go('/settings')}>
      <span class="pill"></span>
      <span class="icon small"><Icon name="gear" /></span>
    </button>
  </div>
</nav>

{#if adding}
  <div class="popover card">
    <form onsubmit={create}>
      <h3>New project</h3>
      <p class="muted">Projects group related apps, like a website and its API.</p>
      <label class="field">
        <span>Name</span>
        <!-- svelte-ignore a11y_autofocus -->
        <input type="text" bind:value={name} placeholder="my-project" autofocus required />
        <small>Lowercase letters, numbers and hyphens.</small>
      </label>
      {#if error}<p class="error">{error}</p>{/if}
      <div class="actions">
        <button type="button" class="btn ghost" onclick={() => (adding = false)}>Cancel</button>
        <button class="btn primary">Create</button>
      </div>
    </form>
  </div>
{/if}

<style>
  .rail {
    width: var(--sp-rail-width);
    height: 100%;
    background: var(--sp-bg-rail);
    display: flex;
    flex-direction: column;
    align-items: center;
    padding: var(--sp-space-3) 0;
    gap: var(--sp-space-2);
    flex: none;
  }
  .projects {
    flex: 1;
    min-height: 0;
    overflow-y: auto;
    display: flex;
    flex-direction: column;
    gap: var(--sp-space-2);
    width: 100%;
    align-items: center;
    scrollbar-width: none;
  }
  .bottom {
    display: flex;
    flex-direction: column;
    gap: var(--sp-space-2);
  }
  .sep {
    width: 32px;
    height: 2px;
    border-radius: 1px;
    background: var(--sp-border-strong);
    flex: none;
  }
  .item {
    position: relative;
    width: var(--sp-rail-width);
    display: flex;
    justify-content: center;
    border: 0;
    padding: 0;
    background: none;
    cursor: pointer;
    color: var(--sp-text);
  }
  .icon {
    width: 48px;
    height: 48px;
    display: grid;
    place-items: center;
    border-radius: var(--sp-rail-icon-radius);
    background: var(--sp-bg-main);
    font-weight: var(--sp-weight-bold);
    font-size: var(--sp-text-md);
    overflow: hidden;
    transition: border-radius var(--sp-transition), background var(--sp-transition), color var(--sp-transition);
  }
  .icon.small {
    width: 40px;
    height: 40px;
    color: var(--sp-text-muted);
    background: transparent;
  }
  .icon.add {
    color: var(--sp-success);
  }
  .icon.logo {
    background: none;
  }
  .item:hover .icon,
  .item.active .icon {
    border-radius: var(--sp-rail-icon-radius-active);
  }
  .item:hover .icon:not(.logo):not(.small),
  .item.active .icon:not(.logo):not(.small) {
    background: var(--sp-accent);
    color: var(--sp-accent-contrast);
  }
  .item:hover .icon.small,
  .item.active .icon.small {
    background: var(--sp-bg-hover);
    color: var(--sp-text);
  }
  .item:hover .icon.add {
    background: var(--sp-success);
    color: var(--sp-accent-contrast);
  }
  .pill {
    position: absolute;
    left: 0;
    top: 50%;
    width: 4px;
    height: 0;
    border-radius: 0 4px 4px 0;
    background: var(--sp-text);
    transform: translateY(-50%);
    transition: height var(--sp-transition);
  }
  .item:hover .pill {
    height: 20px;
  }
  .item.active .pill {
    height: 40px;
  }
  .popover {
    position: fixed;
    left: calc(var(--sp-rail-width) + var(--sp-space-2));
    top: var(--sp-space-6);
    z-index: 30;
    width: min(340px, calc(100vw - var(--sp-rail-width) - var(--sp-space-4)));
    background: var(--sp-bg-elevated);
    box-shadow: var(--sp-shadow);
  }
  .popover form {
    display: flex;
    flex-direction: column;
    gap: var(--sp-space-3);
  }
  .actions {
    display: flex;
    justify-content: flex-end;
    gap: var(--sp-space-2);
  }
</style>
