<script>
  import { onMount } from 'svelte';
  import { api } from '../lib/api.js';
  import { ui, data, go, initials, loadProjects } from '../lib/state.svelte.js';
  import Topbar from './Topbar.svelte';
  import ImportDialog from './ImportDialog.svelte';
  import Metrics from './Metrics.svelte';
  import { isAdmin } from '../lib/state.svelte.js';

  let importing = $state(false);

  let sys = $state(null);
  let appCount = $state(null);
  let hasBackups = $state(null);
  let unscheduled = $state([]); // databases with no backup schedule
  let dismissed = $state(load());
  function load() {
    try {
      return localStorage.getItem('sp-getting-started') === 'hidden';
    } catch {
      return false;
    }
  }
  function dismiss() {
    dismissed = true;
    try {
      localStorage.setItem('sp-getting-started', 'hidden');
    } catch {
      /* storage unavailable */
    }
  }

  onMount(async () => {
    try {
      sys = await api.get('/system');
    } catch {
      /* shown as unknown */
    }
    if (!isAdmin()) return;
    const dbs = [];
    try {
      let n = 0;
      for (const p of data.projects.slice(0, 20)) {
        const apps = await api.get(`/projects/${p.id}/apps`);
        n += apps.filter((a) => !a.kind || a.kind === 'app').length;
        for (const a of apps) if (a.kind && a.kind !== 'app') dbs.push({ pid: p.id, id: a.id, name: a.name });
      }
      appCount = n;
    } catch {
      appCount = null;
    }
    try {
      hasBackups = (await api.get('/backups/destinations')).length > 0;
    } catch {
      hasBackups = null;
    }
    // A destination alone backs nothing up: each database needs a schedule.
    if (hasBackups) {
      const missing = [];
      for (const d of dbs.slice(0, 30)) {
        try {
          const o = await api.get(`/apps/${d.id}/backups`);
          if (!o.jobs?.some((j) => j.enabled)) missing.push(d);
        } catch {
          /* skip */
        }
      }
      unscheduled = missing;
    }
  });

  // Inline "create a project" for the first step.
  let projName = $state('');
  let newOpen = $state(false);
  // The form can open below the fold: bring it into view and focus it.
  function focusIn(node) {
    node.scrollIntoView({ block: 'center', behavior: 'smooth' });
    node.focus({ preventScroll: true });
  }
  let projErr = $state('');
  let projSlug = $derived(projName.toLowerCase().normalize('NFKD').replace(/[\u0300-\u036f]/g, '').replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '').slice(0, 30).replace(/-+$/, ''));
  async function createProject(e) {
    e.preventDefault();
    projErr = '';
    try {
      const p = await api.post('/projects', { name: projSlug });
      await loadProjects();
      go(`/p/${p.id}/new`);
    } catch (err) {
      projErr = err.message;
    }
  }

  let steps = $derived([
    { key: 'project', done: data.projects.length > 0, title: 'Create a project', text: 'A project groups the apps and databases that belong together, like a website and its database.' },
    { key: 'app', done: (appCount ?? 0) > 0, title: 'Deploy your first app', text: 'From a Git repository or a Docker image. You can add a database in the same project.' },
    { key: 'domain', done: !!sys?.panelDomain, title: 'Open this panel on your own domain', text: 'So you sign in over HTTPS instead of http://…:3000.' },
    { key: 'backups', done: !!hasBackups && unscheduled.length === 0, title: 'Set up backups', text: 'Keep copies of your databases on S3-compatible storage such as Cloudflare R2 or Backblaze B2.' },
  ]);
  let showSteps = $derived(isAdmin() && !dismissed && appCount !== null && steps.some((x) => !x.done));
  let next = $derived(steps.find((x) => !x.done)?.key);
</script>

<Topbar>
  Overview
  {#snippet actions()}
    {#if isAdmin()}
      <button class="btn" onclick={() => (importing = true)}><span>Import<span class="wide">&nbsp;project</span></span></button>
      <button class="btn primary" onclick={() => { newOpen = true; }}>New project</button>
    {/if}
  {/snippet}
</Topbar>
{#if importing}<ImportDialog onclose={() => (importing = false)} />{/if}
<div class="page">
  {#if ui.flash}<p class="notice flash">{ui.flash} <button class="btn ghost small" onclick={() => (ui.flash = '')}>Dismiss</button></p>{/if}
  <section class="hero">
    <h1>Welcome to SimplePanel</h1>
    <p class="muted">Deploy apps from a Git repository or a Docker image, with domains and automatic HTTPS.</p>
  </section>
  {#if sys?.dockerError}<p class="error">SimplePanel can't reach Docker: {sys.dockerError}</p>{/if}
  {#if sys && sys.swarm && sys.swarm !== 'active'}<p class="error">Docker Swarm is {sys.swarm}. Run the install command again to set it up.</p>{/if}

  {#if showSteps}
    <section class="card start">
      <div class="row between">
        <h2>Getting started</h2>
        <button class="btn ghost small" onclick={dismiss}>Hide</button>
      </div>
      <ol class="steps">
        {#each steps as st, i (st.key)}
          <li class:done={st.done} class:next={next === st.key}>
            <span class="num">{st.done ? '✓' : i + 1}</span>
            <div class="body">
              <strong>{st.title}</strong>
              {#if !st.done && !(st.key === 'backups' && hasBackups)}<span class="muted">{st.text}</span>{/if}
              {#if next === st.key}
                {#if st.key === 'project'}
                  <form class="row" onsubmit={createProject}>
                    <input type="text" bind:value={projName} placeholder="my-website" aria-label="Project name" required />
                    <button class="btn primary">Create project</button>
                  </form>
                  {#if projSlug && projSlug !== projName}<small class="faint">Will be created as <strong>{projSlug}</strong>.</small>{/if}
                  {#if projErr}<p class="error">{projErr}</p>{/if}
                {:else if st.key === 'app'}
                  <div><button class="btn primary" onclick={() => go(`/p/${data.projects[0].id}/new`)}>New app</button></div>
                {:else if st.key === 'domain'}
                  <div><a class="btn primary" href="#/settings">Set a panel domain</a></div>
                {:else if st.key === 'backups' && hasBackups}
                  <span>
                    Destination added. {unscheduled.length === 1 ? 'This database has' : 'These databases have'} no backup schedule yet:
                    {#each unscheduled as d, j (d.id)}{j ? ', ' : ''}<a href="#/p/{d.pid}/a/{d.id}/backups">{d.name}</a>{/each}.
                  </span>
                {:else if st.key === 'backups'}
                  <div><a class="btn primary" href="#/settings/backups">Add a backup destination</a></div>
                {/if}
              {:else if !st.done && st.key === 'domain'}
                <div><a class="linkish" href="#/settings">Set a panel domain</a></div>
              {:else if !st.done && st.key === 'backups' && hasBackups}
                <span>
                  Destination added. {unscheduled.length === 1 ? 'This database has' : 'These databases have'} no backup schedule yet:
                  {#each unscheduled as d, j (d.id)}{j ? ', ' : ''}<a href="#/p/{d.pid}/a/{d.id}/backups">{d.name}</a>{/each}.
                </span>
              {:else if !st.done && st.key === 'backups'}
                <div><a class="linkish" href="#/settings/backups">Add a backup destination</a></div>
              {/if}
            </div>
          </li>
        {/each}
      </ol>
    </section>
  {/if}

  <section>
    <h2>Projects</h2>
    {#if newOpen}
      <form class="card row newproj" onsubmit={createProject}>
        <input type="text" bind:value={projName} placeholder="my-website" aria-label="Project name" use:focusIn required />
        <button class="btn primary">Create project</button>
        <button type="button" class="btn ghost" onclick={() => (newOpen = false)}>Cancel</button>
        {#if projSlug && projSlug !== projName}<small class="faint">Will be created as <strong>{projSlug}</strong>.</small>{/if}
        {#if projErr}<p class="error">{projErr}</p>{/if}
      </form>
    {/if}
    {#if data.projects.length === 0}
      <div class="card empty">
        {#if isAdmin()}
          <p>You don't have any projects yet.</p>
          <p class="muted">Create one in Getting started above, or with the <strong>+</strong> in the left rail. Have an export from another server? Use <strong>Import project</strong>.</p>
        {:else}
          <p>You don't have access to any projects yet.</p>
          <p class="muted">Ask an admin to give you access to a project.</p>
        {/if}
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

  <section>
    <h2>Server</h2>
    <Metrics path="/metrics/server" server />
  </section>

  {#if sys}
    <section class="stats">
      <div class="card stat"><span class="faint">SimplePanel version</span><strong>{sys.version}</strong></div>
      <div class="card stat"><span class="faint">Docker version</span><strong>{sys.docker || 'unreachable'}</strong></div>
      <div class="card stat">
        <span class="faint">Swarm</span>
        <strong class:bad={sys.swarm !== 'active'}>{sys.swarm || 'unknown'}</strong>
      </div>
    </section>
  {/if}
</div>

<style>
  .flash {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--sp-space-3);
    margin: 0;
  }
  .page {
    padding: var(--sp-space-5);
    display: flex;
    flex-direction: column;
    gap: var(--sp-space-5);
    max-width: 1100px;
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
  .newproj {
    margin-bottom: var(--sp-space-3);
    align-items: center;
  }
  .newproj input {
    flex: 1 1 200px;
  }
  .start {
    display: flex;
    flex-direction: column;
    gap: var(--sp-space-3);
    border-color: var(--sp-accent);
  }
  .between {
    justify-content: space-between;
    align-items: center;
  }
  .steps {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: var(--sp-space-3);
  }
  .steps li {
    display: flex;
    gap: var(--sp-space-3);
    align-items: flex-start;
  }
  .steps .num {
    width: 26px;
    height: 26px;
    border-radius: 50%;
    display: grid;
    place-items: center;
    flex: none;
    font-size: var(--sp-text-sm);
    font-weight: var(--sp-weight-bold);
    border: 1px solid var(--sp-border-strong);
    color: var(--sp-text-muted);
  }
  .steps li.next .num {
    background: var(--sp-accent);
    border-color: var(--sp-accent);
    color: var(--sp-accent-contrast);
  }
  .steps li.done .num {
    border-color: var(--sp-success);
    color: var(--sp-success);
  }
  .steps li.done strong {
    color: var(--sp-text-muted);
    font-weight: var(--sp-weight-medium);
  }
  .steps .body {
    display: flex;
    flex-direction: column;
    gap: var(--sp-space-2);
    min-width: 0;
    flex: 1;
  }
  .steps .row input {
    flex: 1 1 200px;
  }
  .empty {
    display: flex;
    flex-direction: column;
    gap: var(--sp-space-2);
  }
  @media (max-width: 560px) {
    .wide {
      display: none;
    }
  }
</style>
