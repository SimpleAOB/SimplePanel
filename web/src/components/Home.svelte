<script>
  import { onMount } from 'svelte';
  import { api } from '../lib/api.js';
  import { data, go, initials } from '../lib/state.svelte.js';
  import Topbar from './Topbar.svelte';

  let sys = $state(null);
  onMount(async () => {
    try {
      sys = await api.get('/system');
    } catch {
      /* shown as unknown */
    }
  });
</script>

<Topbar>Overview</Topbar>
<div class="page">
  <section class="hero">
    <h1>Welcome to SimplePanel</h1>
    <p class="muted">Deploy apps from a Git repository or a Docker image, with domains and automatic HTTPS.</p>
  </section>

  {#if sys}
    <section class="stats">
      <div class="card stat"><span class="faint">SimplePanel</span><strong>{sys.version}</strong></div>
      <div class="card stat"><span class="faint">Docker</span><strong>{sys.docker || 'unreachable'}</strong></div>
      <div class="card stat">
        <span class="faint">Swarm</span>
        <strong class:bad={sys.swarm !== 'active'}>{sys.swarm || 'unknown'}</strong>
      </div>
    </section>
    {#if sys.dockerError}<p class="error">Docker: {sys.dockerError}</p>{/if}
  {/if}

  <section>
    <h2>Projects</h2>
    {#if data.projects.length === 0}
      <div class="card empty">
        <p>You don't have any projects yet.</p>
        <p class="muted">Click the <strong>+</strong> in the left rail to create one, then add your first app.</p>
      </div>
    {:else}
      <div class="grid">
        {#each data.projects as p (p.id)}
          <button class="card project" onclick={() => go(`/p/${p.id}`)}>
            <span class="badge">{initials(p.name)}</span>
            <span>{p.name}</span>
          </button>
        {/each}
      </div>
    {/if}
  </section>
</div>

<style>
  .page {
    padding: var(--sp-space-5);
    display: flex;
    flex-direction: column;
    gap: var(--sp-space-5);
    max-width: 960px;
    width: 100%;
  }
  .hero {
    display: flex;
    flex-direction: column;
    gap: var(--sp-space-2);
  }
  .stats {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(160px, 1fr));
    gap: var(--sp-space-3);
  }
  .stat {
    display: flex;
    flex-direction: column;
    gap: var(--sp-space-1);
  }
  .stat .faint {
    font-size: var(--sp-text-xs);
    text-transform: uppercase;
    letter-spacing: 0.04em;
  }
  .bad {
    color: var(--sp-danger);
  }
  section > h2 {
    margin-bottom: var(--sp-space-3);
  }
  .grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(200px, 1fr));
    gap: var(--sp-space-3);
  }
  .project {
    display: flex;
    align-items: center;
    gap: var(--sp-space-3);
    font: inherit;
    color: var(--sp-text);
    cursor: pointer;
    text-align: left;
  }
  .project:hover {
    border-color: var(--sp-accent);
  }
  .badge {
    width: 40px;
    height: 40px;
    border-radius: var(--sp-rail-icon-radius-active);
    background: var(--sp-accent);
    color: var(--sp-accent-contrast);
    display: grid;
    place-items: center;
    font-weight: var(--sp-weight-bold);
    flex: none;
  }
  .empty {
    display: flex;
    flex-direction: column;
    gap: var(--sp-space-2);
  }
</style>
