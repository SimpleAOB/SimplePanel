<script>
  // Picks a repository from a connected GitHub App or GitLab account, or
  // falls back to a plain repository URL. Binds gitProviderId, gitRepo,
  // gitUrl and gitBranch on `value`.
  import { onMount } from 'svelte';
  import { api } from '../lib/api.js';
  import { ui, isAdmin } from '../lib/state.svelte.js';

  let { value = $bindable() } = $props();

  let providers = $state([]);
  let repos = $state([]);
  let branches = $state([]);
  let loadingRepos = $state(false);
  let err = $state('');
  let filter = $state('');
  // 'url' or a provider id
  let mode = $state(value.gitProviderId || 'url');

  let current = $derived(providers.find((p) => p.id === mode));
  let shown = $derived(
    repos.filter((r) => !filter || r.fullName.toLowerCase().includes(filter.toLowerCase())).slice(0, 200),
  );

  onMount(async () => {
    try {
      providers = await api.get('/git/providers');
      if (!value.gitProviderId && providers.length && !value.gitUrl) mode = providers[0].id;
      if (mode !== 'url') await loadRepos();
    } catch {
      providers = [];
    }
  });

  async function loadRepos() {
    err = '';
    repos = [];
    loadingRepos = true;
    try {
      repos = await api.get(`/git/providers/${mode}/repos`);
      if (value.gitRepo) await loadBranches();
    } catch (e) {
      err = e.message;
    } finally {
      loadingRepos = false;
    }
  }

  async function loadBranches() {
    branches = [];
    if (!value.gitRepo || mode === 'url') return;
    try {
      branches = await api.get(`/git/providers/${mode}/branches?repo=${encodeURIComponent(value.gitRepo)}`);
    } catch {
      branches = [];
    }
  }

  async function pickMode(m) {
    mode = m;
    if (m === 'url') {
      value.gitProviderId = 0;
      value.gitRepo = '';
      return;
    }
    value.gitProviderId = m;
    value.gitRepo = '';
    await loadRepos();
  }

  async function pickRepo(r) {
    value.gitRepo = r.fullName;
    value.gitUrl = r.cloneUrl;
    value.gitBranch = '';
    await loadBranches();
  }

  const kindLabel = { github: 'GitHub', gitlab: 'GitLab' };
</script>

{#if providers.length}
  <div class="field">
    <span>Repository</span>
    <div class="segmented" role="radiogroup" aria-label="Where the code is">
      {#each providers as p (p.id)}
        <button type="button" role="radio" aria-checked={mode === p.id} class:active={mode === p.id} onclick={() => pickMode(p.id)}>
          {kindLabel[p.kind]}{providers.filter((x) => x.kind === p.kind).length > 1 ? ` · ${p.owner || p.name}` : ''}
        </button>
      {/each}
      <button type="button" role="radio" aria-checked={mode === 'url'} class:active={mode === 'url'} onclick={() => pickMode('url')}>Repository URL</button>
    </div>
  </div>
{/if}

{#if mode === 'url' || !providers.length}
  <label class="field">
    <span>Repository URL</span>
    <input type="text" bind:value={value.gitUrl} placeholder="https://github.com/user/repo" required spellcheck="false" />
    {#if !providers.length}
      <small>
        Public repositories work as they are. {#if isAdmin()}<a href="#/settings/git">Connect GitHub or GitLab</a> to pick private repositories and deploy on every push.{/if}
      </small>
    {:else if ui.advanced}
      <small>For private repositories, add an access token below, or use a <code>git@</code> URL with a deploy key (create it in the app's Git section).</small>
    {/if}
  </label>
{:else}
  <div class="field">
    {#if loadingRepos}
      <p class="faint small">Loading repositories…</p>
    {:else if err}
      <p class="error">{err}</p>
    {:else}
      {#if value.gitRepo}
        <div class="picked">
          <span class="mono">{value.gitRepo}</span>
          <button type="button" class="btn ghost small" onclick={() => { value.gitRepo = ''; branches = []; }}>Change</button>
        </div>
      {:else}
        <input type="search" bind:value={filter} placeholder="Search {repos.length} repositories" aria-label="Search repositories" />
        <ul class="repos" role="listbox" aria-label="Repositories">
          {#each shown as r (r.fullName)}
            <li>
              <button type="button" role="option" aria-selected="false" onclick={() => pickRepo(r)}>
                <span class="mono">{r.fullName}</span>
                {#if r.private}<span class="tag">private</span>{/if}
                <span class="faint small">{r.defaultBranch}</span>
              </button>
            </li>
          {:else}
            <li class="faint small none">No repositories{filter ? ' match' : ''}.</li>
          {/each}
        </ul>
        <small>
          Missing one?
          {#if current?.kind === 'github' && current.installUrl}
            <a href={current.installUrl} target="_blank" rel="noopener noreferrer">Give the GitHub App access to more repositories</a>.
          {:else if current?.kind === 'github'}
            Ask an admin to give the GitHub App access to it.
          {:else}
            The GitLab token needs the Maintainer role on the project.
          {/if}
        </small>
      {/if}
    {/if}
  </div>
  {#if value.gitRepo}
    <label class="field">
      <span>Branch</span>
      <select bind:value={value.gitBranch}>
        <option value="">Default branch ({repos.find((r) => r.fullName === value.gitRepo)?.defaultBranch || 'main'})</option>
        {#each branches as b}<option value={b}>{b}</option>{/each}
        {#if value.gitBranch && !branches.includes(value.gitBranch)}<option value={value.gitBranch}>{value.gitBranch}</option>{/if}
      </select>
      <small>Every push to this branch deploys. Pull requests into it can get their own preview.</small>
    </label>
  {/if}
{/if}

<style>
  .segmented {
    align-self: flex-start;
    flex-wrap: wrap;
  }
  .repos {
    list-style: none;
    margin: 0;
    padding: 0;
    max-height: 240px;
    overflow-y: auto;
    border: 1px solid var(--sp-border);
    border-radius: var(--sp-radius-md);
  }
  .repos li + li {
    border-top: 1px solid var(--sp-border);
  }
  .repos button {
    width: 100%;
    display: flex;
    gap: var(--sp-space-2);
    align-items: center;
    padding: var(--sp-space-2) var(--sp-space-3);
    border: 0;
    background: none;
    color: var(--sp-text);
    font: inherit;
    font-size: var(--sp-text-sm);
    text-align: left;
    cursor: pointer;
  }
  .repos button:hover {
    background: var(--sp-bg-hover);
  }
  .repos .mono {
    flex: 1;
    min-width: 0;
    overflow-wrap: anywhere;
  }
  .none {
    padding: var(--sp-space-2) var(--sp-space-3);
  }
  .tag {
    font-size: var(--sp-text-xs);
    padding: 1px var(--sp-space-2);
    border-radius: var(--sp-radius-round);
    border: 1px solid var(--sp-border);
    color: var(--sp-text-muted);
  }
  .picked {
    display: flex;
    align-items: center;
    gap: var(--sp-space-2);
    padding: var(--sp-space-2) var(--sp-space-3);
    border: 1px solid var(--sp-border);
    border-radius: var(--sp-radius-md);
    background: var(--sp-bg-main);
  }
  .picked .mono {
    flex: 1;
  }
  .small {
    font-size: var(--sp-text-sm);
  }
</style>
