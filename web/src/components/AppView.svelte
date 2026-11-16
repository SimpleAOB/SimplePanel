<script>
  import { onMount } from 'svelte';
  import { api } from '../lib/api.js';
  import { ui, go, loadApps, setPanelOpen, showDeployment, timeAgo, appIcon } from '../lib/state.svelte.js';
  import { formFromApp, payload } from '../lib/appform.js';
  import Topbar from './Topbar.svelte';
  import Icon from './Icon.svelte';
  import StatusBadge from './StatusBadge.svelte';
  import AppFields from './AppFields.svelte';
  import ExportDialog from './ExportDialog.svelte';

  let exporting = $state(false);

  let detail = $state(null);
  let deployments = $state([]);
  let error = $state('');
  let notFound = $state(false);
  let busy = $state('');
  let form = $state(null);
  let env = $state('');
  let saved = $state('');
  let newDomain = $state({ host: '', port: 80, https: true });
  let domainErr = $state('');
  let confirmName = $state('');
  let reveal = $state(false);
  let copied = $state('');
  let isDb = $derived(!!detail?.database);

  const pid = ui.route.projectId;
  const aid = ui.route.appId;
  let tab = $derived(ui.route.tab);
  const tabs = [
    ['overview', 'Overview'],
    ['environment', 'Environment'],
    ['deployments', 'Deployments'],
    ['settings', 'Settings'],
  ];

  async function refresh(resetForm = false) {
    try {
      const [d, deps] = await Promise.all([api.get(`/apps/${aid}`), api.get(`/apps/${aid}/deployments`)]);
      detail = d;
      deployments = deps;
      if (resetForm || !form) {
        form = formFromApp(d.app);
        env = d.app.env;
      }
    } catch (err) {
      if (err.status === 404) notFound = true;
      else error = err.message;
    }
  }

  onMount(() => {
    refresh(true);
    const t = setInterval(() => refresh(false), 5000);
    return () => clearInterval(t);
  });

  async function action(name, fn) {
    busy = name;
    error = '';
    try {
      await fn();
      await refresh(false);
      loadApps(pid).catch(() => {});
    } catch (err) {
      error = err.message;
    } finally {
      busy = '';
    }
  }

  const deploy = () =>
    action('deploy', async () => {
      const d = await api.post(`/apps/${aid}/deploy`);
      showDeployment(d.id);
    });
  const restart = () => action('restart', () => api.post(`/apps/${aid}/restart`));
  const stop = () => action('stop', () => api.post(`/apps/${aid}/stop`));
  const start = () => action('start', () => api.post(`/apps/${aid}/start`));

  async function saveApp(fields, deployAfter) {
    saved = '';
    await action('save', async () => {
      await api.put(`/apps/${aid}`, payload(fields));
      saved = deployAfter ? 'Saved. Deploying…' : 'Saved. Deploy to apply the changes.';
      if (deployAfter) {
        const d = await api.post(`/apps/${aid}/deploy`);
        showDeployment(d.id);
      }
    });
    await refresh(true);
  }

  function saveEnv(e) {
    e.preventDefault();
    saveApp({ ...formFromApp(detail.app), env }, true);
  }

  function saveSettings(e) {
    e.preventDefault();
    saveApp(form, false);
  }

  async function addDomain(e) {
    e.preventDefault();
    domainErr = '';
    try {
      await api.post(`/apps/${aid}/domains`, {
        host: newDomain.host.trim().toLowerCase(),
        port: Number(newDomain.port) || 80,
        https: newDomain.https,
      });
      newDomain = { host: '', port: newDomain.port, https: true };
      await refresh(false);
    } catch (err) {
      domainErr = err.message;
    }
  }

  async function removeDomain(id) {
    domainErr = '';
    try {
      await api.del(`/domains/${id}`);
      await refresh(false);
    } catch (err) {
      domainErr = err.message;
    }
  }

  async function rotateWebhook() {
    await action('webhook', () => api.post(`/apps/${aid}/webhook/rotate`));
  }

  async function removeApp() {
    await action('delete', async () => {
      await api.del(`/apps/${aid}`);
      await loadApps(pid);
      go(`/p/${pid}`);
    });
  }

  async function copy(text, key = '') {
    try {
      await navigator.clipboard.writeText(text);
      copied = key;
      setTimeout(() => (copied = ''), 1500);
    } catch {
      /* clipboard unavailable (non-HTTPS) */
    }
  }

  function masked(s) {
    return reveal ? s : '•'.repeat(Math.min(s?.length || 0, 16));
  }
</script>

{#if notFound}
  <Topbar>App</Topbar>
  <p class="page muted">This app no longer exists.</p>
{:else if !detail}
  <Topbar>Loading…</Topbar>
{:else}
  <Topbar>
    {detail.app.name}
    <StatusBadge status={detail.status} />
    {#snippet actions()}
      <button class="btn primary" disabled={!!busy} onclick={deploy}><Icon name="refresh" size={16} /> Deploy</button>
      <button
        class="btn icon"
        class:active={ui.panelOpen}
        title={ui.panelOpen ? 'Hide logs panel' : 'Show logs panel'}
        aria-label="Toggle logs panel"
        onclick={() => setPanelOpen(!ui.panelOpen)}><Icon name="panel" size={18} /></button>
    {/snippet}
  </Topbar>

  <nav class="tabs" aria-label="App sections">
    {#each tabs as [key, label]}
      <a href="#/p/{pid}/a/{aid}/{key}" class:active={tab === key}>{label}</a>
    {/each}
  </nav>

  <div class="page">
    {#if error}<p class="error">{error}</p>{/if}
    {#if detail.status.message && detail.status.state === 'failing'}
      <p class="error">The app is failing to start: {detail.status.message}</p>
    {/if}

    {#if tab === 'overview'}
      <section class="card">
        <h3>{isDb ? 'Database' : 'Source'}</h3>
        <p class="mono src">
          <Icon name={appIcon(detail.app)} size={16} />
          {detail.app.source === 'git' ? detail.app.gitUrl : detail.app.image}
          {#if detail.app.source === 'git' && detail.app.gitBranch}<span class="faint">@ {detail.app.gitBranch}</span>{/if}
        </p>
        <div class="row buttons">
          {#if detail.status.state === 'stopped'}
            <button class="btn" disabled={!!busy} onclick={start}>Start</button>
          {:else if detail.status.state !== 'not_deployed'}
            <button class="btn" disabled={!!busy} onclick={restart}>Restart</button>
            <button class="btn" disabled={!!busy} onclick={stop}>Stop</button>
          {/if}
        </div>
      </section>

      {#if isDb}
        {@const db = detail.database}
        <section class="card">
          <div class="row between">
            <h3>Connection</h3>
            <button class="btn ghost small" onclick={() => (reveal = !reveal)}><Icon name="eye" size={16} /> {reveal ? 'Hide' : 'Show'} secrets</button>
          </div>
          <p class="muted">Apps in this project can reach the database at this address. It is not exposed to the internet.</p>
          <dl class="conn">
            {#each [['Host', db.host, false], ['Port', String(db.port), false], ['Database', db.database, false], ['User', db.user, false], ['Password', db.password, true], ...(db.rootPassword ? [['Root password', db.rootPassword, true]] : [])] as [label, val, secret]}
              <dt>{label}</dt>
              <dd>
                <code>{secret ? masked(val) : val}</code>
                <button class="btn ghost icon small" title="Copy {label}" aria-label="Copy {label}" onclick={() => copy(val, label)}><Icon name="copy" size={14} /></button>
                {#if copied === label}<span class="ok">Copied</span>{/if}
              </dd>
            {/each}
          </dl>
          <div class="field">
            <span>Connection URL</span>
            <div class="row tight">
              <input type="text" readonly class="mono" value={reveal ? db.url : db.url.replace(`:${encodeURIComponent(db.password)}@`, ':••••••@')} aria-label="Connection URL" />
              <button class="btn" onclick={() => copy(db.url, 'url')}>{copied === 'url' ? 'Copied' : 'Copy'}</button>
            </div>
            <small class="faint">Use it as DATABASE_URL in your app's Environment tab. Changing the password variables after the first start does not change the database's password.</small>
          </div>
        </section>
      {:else}
      <section class="card">
        <h3>Domains</h3>
        {#if detail.domains.length === 0}
          <p class="muted">No domains yet. Add one to make this app reachable over HTTPS.</p>
        {/if}
        <ul class="domains">
          {#each detail.domains as d (d.id)}
            <li>
              <Icon name="globe" size={16} />
              <a href="{d.https ? 'https' : 'http'}://{d.host}" target="_blank" rel="noopener noreferrer">{d.host}</a>
              <span class="faint">→ port {d.port}{d.https ? '' : ' (HTTP only)'}</span>
              <button class="btn ghost small" onclick={() => removeDomain(d.id)}>Remove</button>
            </li>
          {/each}
        </ul>
        <form class="row" onsubmit={addDomain}>
          <label class="field host">
            <span>Domain</span>
            <input type="text" bind:value={newDomain.host} placeholder="app.example.com" required />
          </label>
          <label class="field port">
            <span>App port</span>
            <input type="number" min="1" max="65535" bind:value={newDomain.port} />
          </label>
          {#if ui.advanced}
            <label class="check"><input type="checkbox" bind:checked={newDomain.https} /> HTTPS</label>
          {/if}
          <button class="btn">Add domain</button>
        </form>
        {#if domainErr}<p class="error">{domainErr}</p>{/if}
        <small class="faint">Point the domain's DNS (A record) at this server. Certificates are issued automatically.</small>
      </section>

      {#if ui.advanced}
        <section class="card">
          <h3>Deploy webhook</h3>
          <p class="muted">POST to this URL (for example from a GitHub webhook) to redeploy. Keep it secret.</p>
          <div class="row tight">
            <input type="text" readonly value={location.origin + detail.webhookPath} class="mono" aria-label="Webhook URL" />
            <button class="btn" onclick={() => copy(location.origin + detail.webhookPath)}>Copy</button>
            <button class="btn" disabled={!!busy} onclick={rotateWebhook}>Regenerate</button>
          </div>
          <small class="faint">Swarm service: <code>{detail.serviceName}</code></small>
        </section>
      {/if}
      {/if}
    {:else if tab === 'environment'}
      <form class="card" onsubmit={saveEnv}>
        <h3>Environment variables</h3>
        <p class="muted">One <code>KEY=value</code> per line. Lines starting with # are ignored.</p>
        <textarea bind:value={env} rows="12" spellcheck="false" placeholder="DATABASE_URL=postgres://…&#10;NODE_ENV=production"></textarea>
        <div class="row end">
          {#if saved}<span class="ok">{saved}</span>{/if}
          <button class="btn primary" disabled={!!busy}>Save and deploy</button>
        </div>
      </form>
    {:else if tab === 'deployments'}
      <section class="card">
        <h3>Deployments</h3>
        {#if deployments.length === 0}
          <p class="muted">No deployments yet.</p>
        {/if}
        <ul class="deps">
          {#each deployments as d (d.id)}
            <li>
              <button class="dep" onclick={() => showDeployment(d.id)}>
                <span class="dot {d.status}"></span>
                <span class="grow">#{d.id} · {d.status}</span>
                <span class="faint">{d.trigger}</span>
                <span class="faint">{timeAgo(d.createdAt)}</span>
              </button>
            </li>
          {/each}
        </ul>
      </section>
    {:else if tab === 'settings' && form}
      <form class="card" onsubmit={saveSettings}>
        <h3>App settings</h3>
        <AppFields bind:value={form} hasToken={detail.app.hasGitToken} database={isDb} />
        {#if !ui.advanced}
          <small class="faint">Turn on Advanced view (bottom left) for branch, tokens, replicas, limits, volumes and ports.</small>
        {/if}
        <div class="row end">
          {#if saved}<span class="ok">{saved}</span>{/if}
          <button class="btn primary" disabled={!!busy}>Save</button>
        </div>
      </form>

      <section class="card">
        <h3>Export</h3>
        <p class="muted">Copy this {isDb ? 'database' : 'app'}'s configuration as JSON to recreate it in another project or on another server.</p>
        <div><button class="btn" onclick={() => (exporting = true)}>Export as JSON</button></div>
      </section>
      {#if exporting}
        <ExportDialog title="Export {detail.app.name}" path="/apps/{aid}/export" filename={detail.app.name} onclose={() => (exporting = false)} />
      {/if}

      <section class="card danger-zone">
        <h3>Delete {isDb ? 'database' : 'app'}</h3>
        <p class="muted">{isDb ? 'Stops the database. Its data volume is kept on the server.' : 'Stops the app and removes its domains. Volumes are kept.'}</p>
        <div class="row">
          <label class="field">
            <span>Type the app name to confirm</span>
            <input type="text" bind:value={confirmName} placeholder={detail.app.name} />
          </label>
          <button class="btn danger" disabled={confirmName !== detail.app.name || !!busy} onclick={removeApp}>Delete app</button>
        </div>
      </section>
    {/if}
  </div>
{/if}

<style>
  .tabs {
    display: flex;
    gap: var(--sp-space-1);
    padding: 0 var(--sp-space-4);
    border-bottom: 1px solid var(--sp-border);
    overflow-x: auto;
    flex: none;
  }
  .tabs a {
    padding: var(--sp-space-3) var(--sp-space-3);
    color: var(--sp-text-muted);
    font-size: var(--sp-text-sm);
    font-weight: var(--sp-weight-medium);
    border-bottom: 2px solid transparent;
    white-space: nowrap;
    text-decoration: none;
  }
  .tabs a:hover {
    color: var(--sp-text);
  }
  .tabs a.active {
    color: var(--sp-text);
    border-bottom-color: var(--sp-accent);
  }
  .page {
    padding: var(--sp-space-5);
    display: flex;
    flex-direction: column;
    gap: var(--sp-space-4);
    max-width: 860px;
    width: 100%;
  }
  .card {
    display: flex;
    flex-direction: column;
    gap: var(--sp-space-3);
  }
  .src {
    display: flex;
    align-items: center;
    gap: var(--sp-space-2);
    overflow-wrap: anywhere;
  }
  .buttons:empty {
    display: none;
  }
  .domains,
  .deps {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: var(--sp-space-1);
  }
  .domains li {
    display: flex;
    align-items: center;
    gap: var(--sp-space-2);
    flex-wrap: wrap;
  }
  .domains li a {
    font-weight: var(--sp-weight-medium);
    overflow-wrap: anywhere;
  }
  .domains li .btn {
    margin-left: auto;
  }
  .host {
    flex: 3 1 200px !important;
  }
  .port {
    flex: 1 1 90px !important;
  }
  .check {
    display: flex;
    align-items: center;
    gap: var(--sp-space-2);
    height: 34px;
    font-size: var(--sp-text-sm);
  }
  .row.tight {
    flex-wrap: nowrap;
    align-items: center;
  }
  .row.tight input {
    flex: 1;
  }
  .end {
    justify-content: flex-end;
    align-items: center;
  }
  .ok {
    color: var(--sp-success);
    font-size: var(--sp-text-sm);
  }
  .dep {
    width: 100%;
    display: flex;
    align-items: center;
    gap: var(--sp-space-3);
    padding: var(--sp-space-2) var(--sp-space-3);
    border: 1px solid var(--sp-border);
    border-radius: var(--sp-radius-md);
    background: var(--sp-bg-main);
    color: var(--sp-text);
    font: inherit;
    font-size: var(--sp-text-sm);
    cursor: pointer;
    text-align: left;
  }
  .dep:hover {
    background: var(--sp-bg-hover);
  }
  .grow {
    flex: 1;
  }
  .between {
    justify-content: space-between;
    align-items: center;
  }
  .conn {
    display: grid;
    grid-template-columns: max-content minmax(0, 1fr);
    gap: var(--sp-space-2) var(--sp-space-4);
    margin: 0;
    align-items: center;
  }
  .conn dt {
    color: var(--sp-text-muted);
    font-size: var(--sp-text-sm);
  }
  .conn dd {
    margin: 0;
    display: flex;
    align-items: center;
    gap: var(--sp-space-2);
    min-width: 0;
  }
  .conn code {
    overflow-wrap: anywhere;
  }
  .danger-zone {
    border-color: var(--sp-danger);
  }
  :global(.btn.icon.active) {
    background: var(--sp-bg-active);
  }
  @media (max-width: 560px) {
    .row.tight {
      flex-wrap: wrap;
    }
  }
</style>
