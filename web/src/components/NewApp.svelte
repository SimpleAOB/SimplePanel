<script>
  import { onMount } from 'svelte';
  import { api } from '../lib/api.js';
  import { ui, data, go, loadApps, showDeployment, setAdvanced, isAdmin } from '../lib/state.svelte.js';
  import { emptyForm, payload } from '../lib/appform.js';
  import AppFields from './AppFields.svelte';
  import Topbar from './Topbar.svelte';
  import Icon from './Icon.svelte';

  // A half-filled form survives a trip to another page (for example to
  // connect GitHub), for this browser tab only.
  const draftKey = `sp-newapp-${ui.route.projectId}`;
  function loadDraft() {
    try {
      return JSON.parse(sessionStorage.getItem(draftKey) || 'null');
    } catch {
      return null;
    }
  }
  const draft = loadDraft();
  let form = $state({ ...emptyForm(), ...(draft?.form || {}), gitToken: '' });
  let domain = $state(draft?.domain || '');
  let port = $state(draft?.port || '');
  let envOpen = $state(!!draft?.form?.env);
  let wildcard = $state('');
  let appSlug = $derived((form.name || '').toLowerCase().normalize('NFKD').replace(/[\u0300-\u036f]/g, '').replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '').slice(0, 30).replace(/-+$/, ''));
  $effect(() => {
    const snapshot = JSON.stringify({ form: { ...form, gitToken: '' }, domain, port });
    try {
      sessionStorage.setItem(draftKey, snapshot);
    } catch {
      /* storage unavailable */
    }
  });
  function clearDraft() {
    try {
      sessionStorage.removeItem(draftKey);
    } catch {
      /* storage unavailable */
    }
  }
  let error = $state('');
  let busy = $state(false);
  let project = $derived(data.projects.find((p) => p.id === ui.route.projectId));
  let kind = $state('app'); // app | database
  let engine = $state('postgres');
  let dbUser = $state('');
  let dbPassword = $state('');
  let showPw = $state(false);
  let dbImage = $state('');
  let engines = $state([]);
  let selected = $derived(engines.find((e) => e.key === engine));
  // Redis has only a password; older servers don't send customUser.
  let customUser = $derived(selected?.customUser ?? true);
  const engineHints = {
    postgres: 'Recommended for most apps',
    mysql: 'For apps built for MySQL',
    mariadb: 'A MySQL-compatible choice',
    mongo: 'Document database',
    redis: 'Cache, queues and sessions',
  };
  const versionExample = {
    postgres: 'postgres:16-alpine',
    mysql: 'mysql:8.0',
    mariadb: 'mariadb:10.11',
    mongo: 'mongo:7.0',
    redis: 'redis:7.4-alpine',
  };

  $effect(() => {
    kind = ui.route.tab === 'database' ? 'database' : 'app';
  });

  onMount(async () => {
    try {
      engines = await api.get('/engines');
    } catch {
      /* falls back to empty list */
    }
    try {
      wildcard = (await api.get('/system')).wildcardDomain || '';
    } catch {
      /* only used for a hint */
    }
  });
  let autoAddress = $derived(wildcard && form.name && project ? `${appSlug}-${project.name}.${wildcard}` : '');
  // "PostgreSQL 17" from the image tag, for people who don't read tags.
  function version(img) {
    const tag = (img || '').split(':')[1] || '';
    const m = tag.match(/^\d+(\.\d+)?/);
    return m ? m[0] : '';
  }

  async function submitDatabase() {
    const pid = ui.route.projectId;
    const r = await api.post(`/projects/${pid}/databases`, {
      name: appSlug,
      engine,
      image: ui.advanced ? dbImage.trim() : '',
      user: customUser ? dbUser.trim() : '',
      password: dbPassword,
    });
    await loadApps(pid);
    clearDraft();
    showDeployment(r.deployment.id);
    go(`/p/${pid}/a/${r.app.id}`);
  }

  async function submit(e) {
    e.preventDefault();
    error = '';
    busy = true;
    const pid = ui.route.projectId;
    try {
      if (kind === 'database') {
        await submitDatabase();
        return;
      }
      const app = await api.post(`/projects/${pid}/apps`, payload({ ...form, name: appSlug }));
      if (domain.trim()) {
        try {
          await api.post(`/apps/${app.id}/domains`, { host: domain.trim().toLowerCase(), port: Number(port) || 0, https: true });
        } catch (err) {
          error = `App created, but the domain was not added: ${err.message}`;
        }
      }
      const dep = await api.post(`/apps/${app.id}/deploy`);
      clearDraft();
      await loadApps(pid);
      showDeployment(dep.id);
      go(`/p/${pid}/a/${app.id}`);
    } catch (err) {
      error = err.message;
    } finally {
      busy = false;
    }
  }
</script>

<Topbar>New {kind === 'database' ? 'database' : 'app'}{project ? ` in ${project.name}` : ''}</Topbar>
<form class="page" onsubmit={submit}>
  <div class="segmented kind" role="radiogroup" aria-label="What to create">
    <button type="button" role="radio" aria-checked={kind === 'app'} class:active={kind === 'app'} onclick={() => (kind = 'app')}>App</button>
    <button type="button" role="radio" aria-checked={kind === 'database'} class:active={kind === 'database'} onclick={() => (kind = 'database')}>Database</button>
  </div>
  <label class="field">
    <span>{kind === 'database' ? 'Database name' : 'App name'}</span>
    <!-- svelte-ignore a11y_autofocus -->
    <input type="text" bind:value={form.name} placeholder={kind === 'database' ? 'my-db' : 'my-app'} required autofocus />
    <small>{#if appSlug && appSlug !== form.name}Will be created as <strong>{appSlug}</strong>.{:else}Used in addresses, so it is kept to lowercase letters, numbers and hyphens.{/if}</small>
  </label>

  {#if kind === 'database'}
    <div class="field">
      <span>Engine</span>
      <div class="engines">
        {#each engines as e (e.key)}
          <label class="card engine" class:selected={engine === e.key}>
            <input type="radio" name="engine" value={e.key} bind:group={engine} />
            <strong>{e.label} {version(e.image)}</strong>
            {#if ui.advanced}<span class="faint mono">{e.image}</span>{:else}<span class="faint">{engineHints[e.key] || ''}</span>{/if}
          </label>
        {/each}
      </div>
    </div>
    {#if ui.advanced}
      <label class="field">
        <span>Image override</span>
        <input type="text" bind:value={dbImage} placeholder={selected?.image} />
        <small>Use a different version, for example {versionExample[engine] || 'postgres:16-alpine'}.</small>
      </label>
    {/if}
    <div class="row">
      {#if customUser}
      <label class="field">
        <span>Username <span class="opt">(optional)</span></span>
        <input type="text" bind:value={dbUser} placeholder={(form.name || 'my-db').trim().toLowerCase().replace(/-/g, '_')} autocomplete="off" spellcheck="false" maxlength="32" />
      </label>
      {/if}
      <label class="field">
        <span>Password <span class="opt">(optional)</span></span>
        <div class="pw">
          <input type={showPw ? 'text' : 'password'} bind:value={dbPassword} placeholder="Leave empty to generate" autocomplete="new-password" maxlength="128" />
          <button type="button" class="btn ghost icon small" aria-label={showPw ? 'Hide password' : 'Show password'} title={showPw ? 'Hide password' : 'Show password'} onclick={() => (showPw = !showPw)}><Icon name="eye" size={16} /></button>
        </div>
      </label>
    </div>
    <small class="hint">
      {#if customUser}
        Leave these empty and SimplePanel generates a username (from the name) and a strong random password. You can copy both from the database's page afterwards.
      {:else}
        Leave it empty and SimplePanel generates a strong random password. You can copy it from the database's page afterwards.
      {/if}
    </small>
    {#if engine === 'mongo'}
      <small class="hint">This user is MongoDB's administrator. Apps connect with <code>authSource=admin</code>, which is already in the connection URL.</small>
    {:else if engine === 'redis'}
      <small class="hint">Every write is saved to disk (append-only file), so data survives restarts.</small>
    {/if}
    <p class="muted">
      Data is stored in a persistent volume. The database is only reachable from apps in this project.
    </p>
  {:else}
  <AppFields bind:value={form} />

  <div class="card domain">
    <h3>Environment variables <span class="faint">(optional)</span></h3>
    {#if envOpen}
      <textarea bind:value={form.env} rows="6" spellcheck="false" placeholder="DATABASE_URL=postgres://…&#10;SECRET_KEY=…"></textarea>
      <small class="faint">One <code>KEY=value</code> per line, or paste a <code>.env</code> file. You can change them later in the Environment tab.</small>
    {:else}
      <div><button type="button" class="btn small" onclick={() => (envOpen = true)}>Add variables</button> <small class="faint">Settings like API keys or a database URL that your app reads at start.</small></div>
    {/if}
  </div>

  <div class="card domain">
    <h3>Domain <span class="faint">(optional)</span></h3>
    <div class="row">
      <label class="field grow">
        <span>Domain</span>
        <input type="text" bind:value={domain} placeholder="app.example.com" />
      </label>
      <label class="field port">
        <span>App port</span>
        <input type="number" min="0" max="65535" bind:value={port} placeholder="auto" />
      </label>
    </div>
    <small class="faint">
      Add an A record for the domain pointing at this server first. HTTPS certificates are issued automatically.
      Leave the port empty and SimplePanel uses the port the app's image says it listens on (or 80).
    </small>
    {#if !domain.trim()}
      {#if autoAddress}
        <small class="hint">Without a domain, the app gets <strong>{autoAddress}</strong>.</small>
      {:else}
        <small class="hint">
          Without a domain, the app is only reachable by other apps in this project.
          {#if isAdmin()}<a href="#/settings">Set up automatic subdomains</a> to give every app an address.{/if}
        </small>
      {/if}
    {/if}
  </div>
  {#if !ui.advanced}
    <div><button type="button" class="linkish" onclick={() => setAdvanced(true)}>Show more options</button> <small class="faint">(access token, build path, replicas, limits, volumes, ports)</small></div>
  {/if}
  {/if}

  {#if error}<p class="error">{error}</p>{/if}
  <div class="actions">
    <button type="button" class="btn ghost" onclick={() => history.back()}>Cancel</button>
    <button class="btn primary" disabled={busy}>{busy ? 'Creating…' : 'Create and deploy'}</button>
  </div>
</form>

<style>
  .page {
    padding: var(--sp-space-5);
    display: flex;
    flex-direction: column;
    gap: var(--sp-space-4);
    max-width: 720px;
    width: 100%;
  }
  .kind {
    align-self: flex-start;
  }
  .opt {
    text-transform: none;
    letter-spacing: 0;
    font-weight: normal;
    color: var(--sp-text-faint);
  }
  .pw {
    display: flex;
    align-items: center;
    gap: var(--sp-space-2);
  }
  .pw input {
    flex: 1;
    min-width: 0;
  }
  .hint {
    margin-top: calc(-1 * var(--sp-space-2));
    color: var(--sp-text-faint);
    font-size: var(--sp-text-xs);
  }
  .engines {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(160px, 1fr));
    gap: var(--sp-space-2);
  }
  .engine {
    position: relative;
    display: flex;
    flex-direction: column;
    gap: var(--sp-space-1);
    cursor: pointer;
    padding: var(--sp-space-3);
  }
  .engine input {
    position: absolute;
    opacity: 0;
    pointer-events: none;
  }
  .engine.selected {
    border-color: var(--sp-accent);
    box-shadow: 0 0 0 1px var(--sp-accent);
  }
  .engine:focus-within {
    outline: 2px solid var(--sp-focus-ring);
  }
  .domain {
    display: flex;
    flex-direction: column;
    gap: var(--sp-space-3);
  }
  .grow {
    flex: 3 1 220px !important;
  }
  .port {
    flex: 1 1 100px !important;
  }
  .actions {
    display: flex;
    justify-content: flex-end;
    gap: var(--sp-space-2);
  }
</style>
