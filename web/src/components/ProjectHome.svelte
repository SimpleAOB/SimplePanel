<script>
  import { api } from '../lib/api.js';
  import { ui, data, go, loadProjects, appIcon } from '../lib/state.svelte.js';
  import Topbar from './Topbar.svelte';
  import Icon from './Icon.svelte';
  import StatusBadge from './StatusBadge.svelte';

  let project = $derived(data.projects.find((p) => p.id === ui.route.projectId));
  let apps = $derived(data.appsFor === ui.route.projectId ? data.apps : []);
  let error = $state('');
  let confirmName = $state('');

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
      <button class="btn primary" onclick={() => go(`/p/${project.id}/new`)}><Icon name="plus" size={16} /> New app</button>
    {/snippet}
  </Topbar>
  <div class="page">
    {#if apps.length === 0}
      <div class="card empty">
        <h2>Add your first app</h2>
        <p class="muted">Deploy from a Git repository with a Dockerfile, run any Docker image, or create a Postgres or MySQL database.</p>
        <div><button class="btn primary" onclick={() => go(`/p/${project.id}/new`)}>New app</button></div>
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
          </button>
        {/each}
      </div>
    {/if}

    {#if ui.advanced}
      <section class="card danger-zone">
        <h3>Delete project</h3>
        <p class="muted">Removes every app in this project and its domains. Volumes are kept.</p>
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
  .empty {
    display: flex;
    flex-direction: column;
    gap: var(--sp-space-3);
    padding: var(--sp-space-6);
  }
  .danger-zone {
    display: flex;
    flex-direction: column;
    gap: var(--sp-space-2);
    border-color: var(--sp-danger);
  }
</style>
