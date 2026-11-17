<script>
  import { api } from '../lib/api.js';
  import { ui, data, go, setAdvanced, appIcon, isAdmin, projectLevel, LEVEL, roleLabels } from '../lib/state.svelte.js';
  import { settingsSections } from '../lib/settings.js';
  import Icon from './Icon.svelte';

  let project = $derived(data.projects.find((p) => p.id === ui.route.projectId));
  let all = $derived(data.appsFor === ui.route.projectId ? data.apps : []);
  let apps = $derived(all.filter((a) => !a.kind || a.kind === 'app'));
  let dbs = $derived(all.filter((a) => a.kind && a.kind !== 'app'));
  let canAdd = $derived(project && projectLevel(project.id) >= LEVEL.deploy);
  let sections = $derived(settingsSections(isAdmin()));

  async function logout() {
    try {
      await api.post('/logout');
    } finally {
      location.hash = '';
      location.reload();
    }
  }
</script>

<aside class="sidebar">
  <header>
    <h2 class="title">{project ? project.name : ui.route.view === 'settings' ? 'Settings' : 'SimplePanel'}</h2>
  </header>

  <div class="scroll">
    {#if project}
      <button class="link" class:active={ui.route.view === 'project'} onclick={() => go(`/p/${project.id}`)}>
        <Icon name="box" size={18} /> Project overview
      </button>
      <div class="section">
        <span>Apps</span>
        {#if canAdd}
          <button class="btn ghost icon small" title="New app" aria-label="New app" onclick={() => go(`/p/${project.id}/new`)}>
            <Icon name="plus" size={16} />
          </button>
        {/if}
      </div>
      {#each apps as a (a.id)}
        <button class="link" class:active={ui.route.appId === a.id} onclick={() => go(`/p/${project.id}/a/${a.id}`)}>
          <span class="dot {a.status?.state}"></span>
          <span class="name">{a.name}</span>
          <span class="kind faint"><Icon name={appIcon(a)} size={14} /></span>
        </button>
      {:else}
        <p class="empty faint">No apps yet.</p>
      {/each}
      <div class="section">
        <span>Databases</span>
        {#if canAdd}
          <button class="btn ghost icon small" title="New database" aria-label="New database" onclick={() => go(`/p/${project.id}/new/database`)}>
            <Icon name="plus" size={16} />
          </button>
        {/if}
      </div>
      {#each dbs as a (a.id)}
        <button class="link" class:active={ui.route.appId === a.id} onclick={() => go(`/p/${project.id}/a/${a.id}`)}>
          <span class="dot {a.status?.state}"></span>
          <span class="name">{a.name}</span>
          <span class="kind faint"><Icon name="db" size={14} /></span>
        </button>
      {:else}
        <p class="empty faint">No databases yet.</p>
      {/each}
      {#if canAdd}
        <button class="link add" class:active={ui.route.view === 'new-app'} onclick={() => go(`/p/${project.id}/new`)}>
          <Icon name="plus" size={18} /> New app or database
        </button>
      {/if}
    {:else if ui.route.view === 'settings'}
      <button class="link" onclick={() => go('/')}>
        <Icon name="box" size={18} /> Overview
      </button>
      <div class="section"><span>Settings</span></div>
      {#each sections as [key, label, icon] (key)}
        <button class="link" class:active={(ui.route.section || sections[0][0]) === key} onclick={() => go(`/settings/${key}`)}>
          <Icon name={icon} size={18} /> {label}
        </button>
      {/each}
    {:else}
      <button class="link" class:active={ui.route.view === 'home'} onclick={() => go('/')}>
        <Icon name="box" size={18} /> Overview
      </button>
      <button class="link" class:active={ui.route.view === 'settings'} onclick={() => go('/settings')}>
        <Icon name="gear" size={18} /> Settings
      </button>
      <div class="section"><span>Projects</span></div>
      {#each data.projects as p (p.id)}
        <button class="link" onclick={() => go(`/p/${p.id}`)}>
          <span class="name">{p.name}</span>
        </button>
      {:else}
        <p class="empty faint">Create a project with the + in the left rail.</p>
      {/each}
    {/if}
  </div>

  <footer>
    <label class="toggle" title="Show advanced options in forms">
      <input type="checkbox" checked={ui.advanced} onchange={(e) => setAdvanced(e.currentTarget.checked)} />
      <span class="track"><span class="thumb"></span></span>
      Advanced view
    </label>
    <div class="user">
      <span class="avatar">{data.user?.username?.[0]?.toUpperCase()}</span>
      <span class="who">
        <span class="name">{data.user?.username}</span>
        <span class="role faint">{roleLabels[data.user?.role] || ''}</span>
      </span>
      <button class="btn ghost small" onclick={logout}>Sign out</button>
    </div>
  </footer>
</aside>

<style>
  .sidebar {
    width: var(--sp-sidebar-width);
    height: 100%;
    background: var(--sp-bg-sidebar);
    display: flex;
    flex-direction: column;
    flex: none;
    min-height: 0;
  }
  header {
    height: var(--sp-header-height);
    display: flex;
    align-items: center;
    padding: 0 var(--sp-space-4);
    border-bottom: 1px solid var(--sp-border);
    flex: none;
  }
  .title {
    font-size: var(--sp-text-md);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .scroll {
    flex: 1;
    overflow-y: auto;
    padding: var(--sp-space-2);
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  .section {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: var(--sp-space-4) var(--sp-space-2) var(--sp-space-1);
    font-size: var(--sp-text-xs);
    font-weight: var(--sp-weight-bold);
    text-transform: uppercase;
    letter-spacing: 0.04em;
    color: var(--sp-text-muted);
  }
  .link {
    display: flex;
    align-items: center;
    gap: var(--sp-space-2);
    width: 100%;
    padding: 6px var(--sp-space-2);
    border: 0;
    border-radius: var(--sp-radius-sm);
    background: none;
    color: var(--sp-text-muted);
    font: inherit;
    font-size: var(--sp-text-md);
    text-align: left;
    cursor: pointer;
  }
  .link:hover {
    background: var(--sp-bg-hover);
    color: var(--sp-text);
  }
  .link.active {
    background: var(--sp-bg-active);
    color: var(--sp-text);
  }
  .link .name {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .link .kind {
    display: flex;
  }
  .link.add {
    color: var(--sp-text-faint);
  }
  .empty {
    padding: var(--sp-space-1) var(--sp-space-2);
    font-size: var(--sp-text-sm);
  }
  footer {
    flex: none;
    padding: var(--sp-space-2) var(--sp-space-3);
    background: var(--sp-bg-rail);
    display: flex;
    flex-direction: column;
    gap: var(--sp-space-2);
  }
  .user {
    display: flex;
    align-items: center;
    gap: var(--sp-space-2);
  }
  .who {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
    line-height: 1.2;
  }
  .role {
    font-size: var(--sp-text-xs);
  }
  .user .name {
    font-size: var(--sp-text-sm);
    font-weight: var(--sp-weight-medium);
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .avatar {
    width: 32px;
    height: 32px;
    border-radius: var(--sp-radius-round);
    background: var(--sp-accent);
    color: var(--sp-accent-contrast);
    display: grid;
    place-items: center;
    font-weight: var(--sp-weight-bold);
    font-size: var(--sp-text-sm);
  }
  .toggle {
    display: flex;
    align-items: center;
    gap: var(--sp-space-2);
    font-size: var(--sp-text-sm);
    color: var(--sp-text-muted);
    cursor: pointer;
  }
  .toggle input {
    position: absolute;
    opacity: 0;
    pointer-events: none;
  }
  .track {
    width: 34px;
    height: 20px;
    border-radius: var(--sp-radius-round);
    background: var(--sp-border-strong);
    position: relative;
    transition: background var(--sp-transition);
  }
  .thumb {
    position: absolute;
    top: 3px;
    left: 3px;
    width: 14px;
    height: 14px;
    border-radius: var(--sp-radius-round);
    background: var(--sp-text);
    transition: transform var(--sp-transition);
  }
  .toggle input:checked + .track {
    background: var(--sp-accent);
  }
  .toggle input:checked + .track .thumb {
    transform: translateX(14px);
    background: var(--sp-accent-contrast);
  }
  .toggle input:focus-visible + .track {
    outline: 2px solid var(--sp-focus-ring);
    outline-offset: 2px;
  }
</style>
