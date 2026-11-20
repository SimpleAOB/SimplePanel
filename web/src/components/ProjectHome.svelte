<script>
  import { onMount } from 'svelte';
  import { api } from '../lib/api.js';
  import { ui, data, go, loadProjects, appIcon, projectLevel, isAdmin } from '../lib/state.svelte.js';
  import { bytes, pct } from '../lib/format.js';
  import Topbar from './Topbar.svelte';
  import Icon from './Icon.svelte';
  import StatusBadge from './StatusBadge.svelte';
  import ExportDialog from './ExportDialog.svelte';
  import ImportDialog from './ImportDialog.svelte';

  let dialog = $state(''); // '' | 'export' | 'import'

  let project = $derived(data.projects.find((p) => p.id === ui.route.projectId));
  let apps = $derived(data.appsFor === ui.route.projectId ? data.apps : []);
  let error = $state('');
  let confirmName = $state('');
  let level = $derived(projectLevel(ui.route.projectId));
  let usage = $state({});

  onMount(() => {
    const load = () => api.get(`/projects/${ui.route.projectId}/metrics`).then((u) => (usage = u)).catch(() => {});
    load();
    const t = setInterval(load, 30000);
    return () => clearInterval(t);
  });

  async function remove() {
    error = '';
    try {
      await api.del(`/projects/${project.id}`);
      await loadProjects();
      go('/');
    } catch (err) {
      error = err.message;
    }
  }
</script>

{#if project}
  <Topbar>
    {project.name}
    {#snippet actions()}
      {#if level >= 2}
        <button class="btn primary" onclick={() => go(`/p/${project.id}/new`)}><Icon name="plus" size={16} /> New</button>
      {/if}
    {/snippet}
  </Topbar>
  <div class="page">
    {#if apps.length === 0}
      <div class="card empty">
        {#if level >= 2}
          <h2>Add your first app</h2>
          <p class="muted">Deploy a website or app from a Git repository or a Docker image. If it needs a database, create one here too: apps and databases in the same project can talk to each other privately.</p>
          <div class="row">
            <button class="btn primary" onclick={() => go(`/p/${project.id}/new`)}>New app</button>
            <button class="btn" onclick={() => go(`/p/${project.id}/new/database`)}>New database</button>
          </div>
          <small class="faint">Moving from another server? <button class="linkish" onclick={() => (dialog = 'import')}>Import a service</button> from its JSON export.</small>
        {:else}
          <h2>Nothing here yet</h2>
          <p class="muted">This project has no apps or databases.</p>
        {/if}
      </div>
    {:else}
      <div class="grid">
        {#each apps as a (a.id)}
          <button class="card app" onclick={() => go(`/p/${project.id}/a/${a.id}`)}>
            <div class="head">
              <strong>{a.name}</strong>
              <StatusBadge status={a.status} />
            </div>
            <span class="src faint mono">
              <Icon name={appIcon(a)} size={14} />
              {a.source === 'git' ? a.gitUrl.replace('https://', '') : a.image}
            </span>
            {#if usage[a.id]}
              {@const u = usage[a.id]}
              <span class="usage faint">
                <span title="CPU, % of the server">CPU {pct(u.cpu)}</span>
                <span title="Memory">{bytes(u.mem)}{u.memLimit ? ` of ${bytes(u.memLimit, 0)}` : ''}</span>
                {#if u.memLimit}<span class="meter" aria-hidden="true"><span style="width:{Math.min(100, (u.mem / u.memLimit) * 100)}%"></span></span>{/if}
              </span>
            {/if}
          </button>
        {/each}
      </div>
    {/if}

    {#if level >= 2 && apps.length > 0}
    <section class="card transfer">
      <div>
        <h3>Export and import</h3>
        <p class="muted">Copy this project as JSON to recreate it elsewhere, or paste a service exported from another project.</p>
      </div>
      <div class="row">
        <button class="btn" onclick={() => (dialog = 'export')}>Export project</button>
        <button class="btn" onclick={() => (dialog = 'import')}>Import service</button>
      </div>
    </section>
    {/if}

    {#if isAdmin()}
      <section class="card danger-zone">
        <h3>Delete project</h3>
        <p class="muted">Removes every app and database in this project, and their domains. Their data stays in Docker volumes on the server's disk.</p>
        <div class="row">
          <label class="field">
            <span>Type the project name to confirm</span>
            <input type="text" bind:value={confirmName} placeholder={project.name} />
          </label>
          <button class="btn danger" disabled={confirmName !== project.name} onclick={remove}>Delete project</button>
        </div>
        {#if error}<p class="error">{error}</p>{/if}
      </section>
    {/if}
  </div>
  {#if dialog === 'export'}
    <ExportDialog title="Export {project.name}" path="/projects/{project.id}/export" filename={project.name} onclose={() => (dialog = '')} />
  {:else if dialog === 'import'}
    <ImportDialog projectId={project.id} onclose={() => (dialog = '')} />
  {/if}
{:else}
  <Topbar>Project</Topbar>
  <p class="page muted">Project not found.</p>
{/if}

<style>
  .page {
    padding: var(--sp-space-5);
    display: flex;
    flex-direction: column;
    gap: var(--sp-space-5);
    max-width: 960px;
    width: 100%;
  }
  .grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(260px, 1fr));
    gap: var(--sp-space-3);
  }
  .app {
    display: flex;
    flex-direction: column;
    gap: var(--sp-space-2);
    text-align: left;
    font: inherit;
    color: var(--sp-text);
    cursor: pointer;
    min-width: 0;
  }
  .app:hover {
    border-color: var(--sp-accent);
  }
  .head {
    display: flex;
    justify-content: space-between;
    align-items: center;
    gap: var(--sp-space-2);
  }
  .src {
    display: flex;
    align-items: center;
    gap: var(--sp-space-1);
    overflow: hidden;
    white-space: nowrap;
    text-overflow: ellipsis;
  }
  .usage {
    display: flex;
    align-items: center;
    gap: var(--sp-space-3);
    font-size: var(--sp-text-xs);
    font-variant-numeric: tabular-nums;
  }
  .meter {
    flex: 1;
    height: 4px;
    border-radius: var(--sp-radius-round);
    background: var(--sp-bg-active);
    overflow: hidden;
    min-width: 40px;
  }
  .meter span {
    display: block;
    height: 100%;
    background: var(--sp-series-1);
    border-radius: var(--sp-radius-round);
  }
  .empty {
    display: flex;
    flex-direction: column;
    gap: var(--sp-space-3);
    padding: var(--sp-space-6);
  }
  @media (max-width: 560px) {
    :global(.hide-sm) {
      display: none !important;
    }
  }
  .transfer {
    display: flex;
    flex-direction: column;
    gap: var(--sp-space-3);
  }
  .danger-zone {
    display: flex;
    flex-direction: column;
    gap: var(--sp-space-2);
    border-color: var(--sp-danger);
  }
</style>
