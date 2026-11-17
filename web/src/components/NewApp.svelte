<script>
  import { onMount } from 'svelte';
  import { api } from '../lib/api.js';
  import { ui, data, go, loadApps, showDeployment } from '../lib/state.svelte.js';
  import { emptyForm, payload } from '../lib/appform.js';
  import AppFields from './AppFields.svelte';
  import Topbar from './Topbar.svelte';
  import Icon from './Icon.svelte';

  let form = $state(emptyForm());
  let domain = $state('');
  let port = $state(80);
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

  $effect(() => {
    kind = ui.route.tab === 'database' ? 'database' : 'app';
  });

  onMount(async () => {
    try {
      engines = await api.get('/engines');
    } catch {
      /* falls back to empty list */
    }
  });

  async function submitDatabase() {
    const pid = ui.route.projectId;
    const r = await api.post(`/projects/${pid}/databases`, {
      name: form.name.trim().toLowerCase(),
      engine,
      image: ui.advanced ? dbImage.trim() : '',
      user: dbUser.trim(),
      password: dbPassword,
    });
    await loadApps(pid);
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
      const app = await api.post(`/projects/${pid}/apps`, payload({ ...form, name: form.name.trim().toLowerCase() }));
      if (domain.trim()) {
        try {
          await api.post(`/apps/${app.id}/domains`, { host: domain.trim().toLowerCase(), port: Number(port) || 80, https: true });
        } catch (err) {
          error = `App created, but the domain was not added: ${err.message}`;
        }
      }
      const dep = await api.post(`/apps/${app.id}/deploy`);
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
    <small>Lowercase letters, numbers and hyphens.</small>
  </label>

  {#if kind === 'database'}
    <div class="field">
      <span>Engine</span>
      <div class="engines">
        {#each engines as e (e.key)}
          <label class="card engine" class:selected={engine === e.key}>
            <input type="radio" name="engine" value={e.key} bind:group={engine} />
            <strong>{e.label}</strong>
            <span class="faint mono">{e.image}</span>
          </label>
        {/each}
      </div>
    </div>
    {#if ui.advanced}
      <label class="field">
        <span>Image override</span>
        <input type="text" bind:value={dbImage} placeholder={engines.find((e) => e.key === engine)?.image} />
        <small>Use a different version, for example postgres:16-alpine.</small>
      </label>
    {/if}
    <div class="row">
      <label class="field">
        <span>Username <span class="opt">(optional)</span></span>
        <input type="text" bind:value={dbUser} placeholder={(form.name || 'my-db').trim().toLowerCase().replace(/-/g, '_')} autocomplete="off" spellcheck="false" maxlength="32" />
      </label>
      <label class="field">
        <span>Password <span class="opt">(optional)</span></span>
        <div class="pw">
          <input type={showPw ? 'text' : 'password'} bind:value={dbPassword} placeholder="Leave empty to generate" autocomplete="new-password" maxlength="128" />
          <button type="button" class="btn ghost icon small" aria-label={showPw ? 'Hide password' : 'Show password'} title={showPw ? 'Hide password' : 'Show password'} onclick={() => (showPw = !showPw)}><Icon name="eye" size={16} /></button>
        </div>
      </label>
    </div>
    <small class="hint">Leave these empty and SimplePanel generates a username (from the name) and a strong random password. You can copy both from the database's page afterwards.</small>
    <p class="muted">
      Data is stored in a persistent volume. The database is only reachable from apps in this project.
    </p>
  {:else}
  <AppFields bind:value={form} />

  <div class="card domain">
    <h3>Domain <span class="faint">(optional)</span></h3>
    <div class="row">
      <label class="field grow">
        <span>Domain</span>
        <input type="text" bind:value={domain} placeholder="app.example.com" />
      </label>
      <label class="field port">
        <span>App port</span>
        <input type="number" min="1" max="65535" bind:value={port} />
      </label>
    </div>
    <small class="faint">Point the domain's DNS at this server. HTTPS certificates are issued automatically.</small>
  </div>
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
