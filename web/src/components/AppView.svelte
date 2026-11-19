<script>
  import { copyText } from '../lib/clipboard.js';
  import { onMount } from 'svelte';
  import { api } from '../lib/api.js';
  import { ui, data, go, loadApps, setPanelOpen, showDeployment, showRun, timeAgo, appIcon, isAdmin, depLabel, depDot, setLeaveGuard } from '../lib/state.svelte.js';
  import { explainFailure, DOCKERFILE_HELP } from '../lib/explain.js';
  import DomainStatus from './DomainStatus.svelte';
  import { formFromApp, payload, withDbUrl } from '../lib/appform.js';
  import Topbar from './Topbar.svelte';
  import Icon from './Icon.svelte';
  import StatusBadge from './StatusBadge.svelte';
  import AppFields from './AppFields.svelte';
  import ExportDialog from './ExportDialog.svelte';
  import Metrics from './Metrics.svelte';
  import BackupsView from './BackupsView.svelte';
  import GitCard from './GitCard.svelte';
  import CronJobs from './CronJobs.svelte';
  import QueryView from './QueryView.svelte';

  let exporting = $state(false);

  let detail = $state(null);
  let deployments = $state([]);
  let error = $state('');
  let notFound = $state(false);
  let busy = $state('');
  let form = $state(null);
  let env = $state('');
  let saved = $state('');
  let newDomain = $state({ host: '', port: '', https: true });
  let domainErr = $state('');
  let confirmName = $state('');
  let reveal = $state(false);
  let copied = $state('');
  let isDb = $derived(!!detail?.database);
  // The variable name apps usually read the connection URL from.
  const urlVars = { mongo: 'MONGODB_URI', redis: 'REDIS_URL' };
  let urlVar = $derived(urlVars[detail?.database?.engine] || 'DATABASE_URL');

  const pid = ui.route.projectId;
  const aid = ui.route.appId;
  let tab = $derived(ui.route.tab);
  let level = $derived(detail?.level ?? 0);
  let canDeploy = $derived(level >= 2);
  let canManage = $derived(level >= 3);
  let isPreview = $derived(!!detail?.app?.parentAppId);
  let tabs = $derived([
    ['overview', 'Overview'],
    ['metrics', 'Metrics'],
    ...(detail?.canQuery ? [['query', 'Query']] : []),
    ...(detail?.backupKind && !isPreview ? [['backups', 'Backups']] : []),
    // Databases rarely need these, so they only show in Advanced view.
    ...(canDeploy && !isPreview && (!isDb || ui.advanced) ? [['environment', 'Environment']] : []),
    ...(!isDb || ui.advanced ? [['deployments', 'Deployments']] : []),
    ...(!isPreview && (!isDb || ui.advanced) ? [['cron', 'Cron jobs']] : []),
    ['settings', 'Settings'],
  ]);
  // Internal addresses: every service joins its project's private network
  // under its short name, and its Swarm name works from that network too.
  let internalPort = $derived.by(() => {
    if (!detail) return 0;
    if (isDb) return detail.database?.port || 0;
    const a = detail.app;
    if (a.httpPort > 0 || detail.domains.length || a.imagePort > 0) return detail.autoPort;
    if (a.buildType === 'static') return 80;
    return 0;
  });
  let internalRoutes = $derived.by(() => {
    if (!detail) return [];
    const scheme = isDb ? '' : 'http://';
    const tail = internalPort ? `:${internalPort}` : '';
    const end = isDb ? '' : '/';
    return [
      ['Short name', `${scheme}${detail.app.name}${tail}${end}`, 'From services in this project'],
      ['Full name', `${scheme}${detail.serviceName}${tail}${end}`, 'The Swarm service name'],
    ];
  });
  // The last deploy failed and nothing newer replaced it.
  let failed = $derived(detail?.lastDeployment?.status === 'failed' ? detail.lastDeployment : null);
  let failure = $derived(failed ? explainFailure(failed.reason || '') : null);
  let badge = $derived(detail?.status);
  // The port a domain sends traffic to: its own, or the image's (automatic).
  const routePort = (d) => d.port || detail?.app?.imagePort || 80;
  let tokenFix = $state('');

  async function saveToken(e) {
    e.preventDefault();
    if (await saveApp({ ...formFromApp(detail.app), gitToken: tokenFix.trim() }, true)) tokenFix = '';
  }
  let imageFix = $state('');
  async function saveImage(e) {
    e.preventDefault();
    await saveApp({ ...formFromApp(detail.app), image: imageFix.trim() }, true);
  }
  let replaceVar = $state(false);

  async function setDomainPort(d, port) {
    domainErr = '';
    try {
      await api.put(`/domains/${d.id}`, { port: Number(port) || 0 });
      editPort = 0;
      await refresh(false);
    } catch (err) {
      domainErr = err.message;
    }
  }
  let editPort = $state(0);
  let editPortValue = $state('');

  // "Use in an app": put this database's URL into another app's variables.
  let useOpen = $state(false);
  let useApp = $state(0);
  let useVar = $state('');
  let useMsg = $state('');
  let otherApps = $derived(data.apps.filter((x) => (!x.kind || x.kind === 'app') && !x.parentAppId));

  async function useInApp(e) {
    e.preventDefault();
    useMsg = '';
    const target = otherApps.find((x) => x.id === Number(useApp));
    const name = (useVar || urlVar).trim();
    const has = (target?.env || '').split('\n').some((l) => l.trim().startsWith(name + '='));
    if (has && !replaceVar) {
      replaceVar = true;
      return;
    }
    replaceVar = false;
    await action('use', async () => {
      const r = await api.patch(`/apps/${useApp}/env?deploy=1`, { set: { [name]: detail.database.url } });
      if (r.deployment) showDeployment(r.deployment.id);
      useMsg = `Added ${name} to ${target?.name || 'the app'} and started a deploy.`;
      useOpen = false;
    });
  }

  // The failure card's log tail starts at its last line, where the error is.
  function scrollEnd(node) {
    const toEnd = () => (node.scrollTop = node.scrollHeight);
    toEnd();
    return { update: toEnd };
  }

  // Environment tab: insert another database's connection URL.
  async function insertDb(id) {
    if (!id) return;
    try {
      env = await withDbUrl(env, id);
    } catch (err) {
      error = err.message;
    }
  }
  let dbs = $derived(data.apps.filter((x) => x.kind && x.kind !== 'app'));
  let serverIp = $state('');
  onMount(async () => {
    if (!data.apps.length || data.appsFor !== pid) loadApps(pid).catch(() => {});
    try {
      const s = await api.get('/system');
      serverIp = s.serverIp || '';
    } catch {
      /* not needed */
    }
    if (!serverIp && /^[\d.]+$|:/.test(location.hostname) && location.hostname !== 'localhost') serverIp = location.hostname;
  });

  let autoBusy = $state(false);
  let autoPort = $state(0);

  async function setAuto(on, port) {
    autoBusy = true;
    error = '';
    try {
      await api.put(`/apps/${aid}`, { ...payload(formFromApp(detail.app)), autoDomain: on, httpPort: Number(port) || 0 });
      await refresh(false);
    } catch (err) {
      error = err.message;
    } finally {
      autoBusy = false;
    }
  }

  async function refresh(resetForm = false) {
    try {
      const [d, deps] = await Promise.all([api.get(`/apps/${aid}`), api.get(`/apps/${aid}/deployments`)]);
      const first = !detail;
      detail = d;
      deployments = deps;
      // An app that isn't up yet has no app logs: show its deploy instead.
      // On a phone the panel would cover the page, which already says what
      // went wrong, so leave it closed there.
      if (first && d.lastDeployment && ['deploy_failed', 'deploying'].includes(d.status?.state) && window.innerWidth > 1100) showDeployment(d.lastDeployment.id);
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
  let aiMsg = $state('');
  const toggleAI = () =>
    action('ai', async () => {
      const r = await api.put(`/apps/${aid}/ai-access`, { enabled: !detail.aiAccess });
      aiMsg = r.warning || '';
    });
  const restart = () => action('restart', () => api.post(`/apps/${aid}/restart`));
  const stop = () => action('stop', () => api.post(`/apps/${aid}/stop`));
  const start = () => action('start', () => api.post(`/apps/${aid}/start`));

  async function saveApp(fields, deployAfter) {
    saved = '';
    let ok = false;
    await action('save', async () => {
      await api.put(`/apps/${aid}`, payload(fields));
      ok = true;
      saved = deployAfter ? 'Saved. Deploying…' : 'Saved. Deploy to apply the changes.';
      if (deployAfter) {
        const d = await api.post(`/apps/${aid}/deploy`);
        showDeployment(d.id);
      }
    });
    // Keep what the person typed if the save failed, so they can fix it.
    if (ok) await refresh(true);
    return ok;
  }
  // Errors belong to the tab they happened on.
  let lastTab = '';
  $effect(() => {
    if (tab !== lastTab) {
      lastTab = tab;
      error = '';
      saved = '';
    }
  });
  let more = $state(false);

  // Unsaved Environment or Settings edits: ask before leaving the page.
  let dirty = $derived(
    !!detail && !!form && (env !== detail.app.env || JSON.stringify(payload(form)) !== JSON.stringify(payload(formFromApp(detail.app)))),
  );
  $effect(() => {
    setLeaveGuard(() => dirty);
    return () => setLeaveGuard(null);
  });

  function saveEnv(e) {
    e.preventDefault();
    saveApp({ ...formFromApp(detail.app), env }, e.submitter?.value !== 'save');
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
        port: Number(newDomain.port) || 0,
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
      await copyText(text);
      copied = key;
      setTimeout(() => (copied = ''), 1500);
    } catch {
      /* clipboard unavailable (non-HTTPS) */
    }
  }

  function masked(s) {
    return reveal ? s : '•'.repeat(Math.min(s?.length || 0, 16));
  }

  // Opens the logs panel on the newest launch that crashed.
  async function crashLogs() {
    try {
      const r = await api.get(`/apps/${aid}/runs`);
      const crashed = r.runs.find((x) => x.state === 'failed' || x.state === 'rejected');
      showRun(crashed ? crashed.number : 0);
    } catch {
      showRun(0);
    }
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
    <StatusBadge status={badge} />
    {#snippet actions()}
      {#if canDeploy}<button class="btn primary" disabled={!!busy} onclick={deploy} title={isDb ? 'Recreate the database container with its current settings. Data is kept.' : 'Build and start the latest version'}><span class="ico"><Icon name="refresh" size={16} /></span> {isDb ? 'Redeploy' : 'Deploy'}</button>{/if}
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

  <div class="page" class:wide={tab === 'query'}>
    {#if error}<p class="error">{error}</p>{/if}
    {#if failed && failure && !isPreview}
      <section class="card failure" role="alert">
        <div class="row between">
          <h3>{failure.title}</h3>
          <button class="btn small" onclick={() => showDeployment(failed.id)}>Show build log</button>
        </div>
        <p class="muted">{failure.hint}</p>
        {#if detail.status.state === 'running'}
          <small class="faint">The previous version is still running, so visitors don't notice.</small>
        {/if}
        {#if canDeploy && failure.kind === 'git-auth'}
          <div class="row fix">
            {#if isAdmin()}<a class="btn" href="#/settings/git">Connect GitHub or GitLab</a>{/if}
            <form class="row tight tokenform" onsubmit={saveToken}>
              <input type="password" bind:value={tokenFix} placeholder="Paste an access token" autocomplete="off" aria-label="Access token" required />
              <button class="btn primary" disabled={!!busy}>Save and deploy</button>
            </form>
          </div>
          <small class="faint">A token with read access to the repository, for example a GitHub fine-grained token with Contents: Read.</small>
        {:else if canDeploy && (failure.kind === 'server-app' || failure.kind === 'no-dockerfile')}
          <div class="row fix">
            <a class="btn" href={DOCKERFILE_HELP} target="_blank" rel="noopener noreferrer">Example Dockerfiles</a>
            <a class="btn ghost" href="#/p/{pid}/a/{aid}/settings">Change the build type</a>
          </div>
        {:else if canDeploy && (failure.kind === 'branch' || failure.kind === 'build-path')}
          <div class="row fix"><button class="btn" onclick={() => { more = true; go(`/p/${pid}/a/${aid}/settings`); }}>Open settings</button></div>
        {:else if canDeploy && failure.kind === 'image'}
          <form class="row tight fix" onsubmit={saveImage}>
            <input type="text" class="mono" value={imageFix || detail.app.image} oninput={(e) => (imageFix = e.currentTarget.value)} aria-label="Image" required />
            <button class="btn primary" disabled={!!busy}>Save and deploy</button>
          </form>
        {:else if failure.kind === 'registry' && isAdmin()}
          <div class="row fix"><a class="btn" href="#/settings/registries">Add a registry login</a></div>
        {/if}
        {#if failed.tail?.length && !['git-auth', 'server-app', 'image'].includes(failure.kind)}
          <pre class="tail" use:scrollEnd={failed.tail}>{failed.tail.join('\n')}</pre>
        {/if}
      </section>
    {/if}
    {#if detail.status.message && detail.status.state === 'failing'}
      <p class="error">
        The app is failing to start: {detail.status.message}
        <button class="linkish" onclick={crashLogs}>See what the last crash logged</button>
      </p>
    {/if}

    {#if isPreview}
      <section class="card preview-banner">
        <div class="row between">
          <span><Icon name="pr" size={16} /> Preview of pull request <strong>#{detail.app.prNumber}</strong> {detail.app.prTitle}</span>
          {#if detail.app.prUrl}<a href={detail.app.prUrl} target="_blank" rel="noopener noreferrer">View pull request</a>{/if}
        </div>
        <small class="faint">
          Built from <code>{detail.app.gitBranch}</code> with the settings of
          {#if detail.parent}<a href="#/p/{pid}/a/{detail.parent.id}">{detail.parent.name}</a>{:else}its app{/if}.
          It is removed when the pull request closes.
        </small>
      </section>
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
          {#if canDeploy && detail.status.state === 'stopped'}
            <button class="btn" disabled={!!busy} onclick={start}>Start</button>
          {:else if canDeploy && !['not_deployed', 'deploy_failed', 'deploying'].includes(detail.status.state)}
            <button class="btn" disabled={!!busy} onclick={restart}>Restart</button>
            <button class="btn" disabled={!!busy} onclick={stop}>Stop</button>
          {/if}
        </div>
      </section>

      {#if !isDb}
      <section class="card">
        <h3>Internal address</h3>
        <p class="muted">
          Other apps and databases in this project can reach {isDb ? 'this database' : 'this app'} over the project's private network,
          without going through the internet.
        </p>
        <dl class="conn routes">
          {#each internalRoutes as [label, val, hint]}
            <dt>{label}</dt>
            <dd>
              <code>{val}</code>
              <button class="btn ghost icon small" title="Copy {label.toLowerCase()} address" aria-label="Copy {label.toLowerCase()} address" onclick={() => copy(val, 'route-' + label)}><Icon name="copy" size={14} /></button>
              {#if copied === 'route-' + label}<span class="ok">Copied</span>{:else}<span class="faint hint">{hint}</span>{/if}
            </dd>
          {/each}
        </dl>
        {#if !internalPort}
          <small class="faint">Add the port your app listens on, for example <code>http://{detail.app.name}:3000/</code>. Adding a domain sets it here.</small>
        {/if}
      </section>
      {/if}

      {#if isDb}
        {@const db = detail.database}
        <section class="card">
          <div class="row between">
            <h3>Connection</h3>
            <button class="btn ghost small" onclick={() => (reveal = !reveal)}><Icon name="eye" size={16} /> {reveal ? 'Hide' : 'Show'} secrets</button>
          </div>
          <p class="muted">Apps in this project connect with these details. The database is not reachable from the internet.</p>
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
            <small class="faint">Apps usually read it from a variable named {urlVar}. The short host <code>{detail.app.name}</code> works too.</small>
          </div>
          {#if canDeploy}
            {#if useMsg}<p class="ok">{useMsg}</p>{/if}
            {#if !useOpen}
              <div><button class="btn primary" onclick={() => { useOpen = true; useApp = otherApps[0]?.id || 0; useVar = urlVar; }}>Use in an app</button></div>
            {:else if otherApps.length === 0}
              <p class="muted">There are no apps in this project yet. <a href="#/p/{pid}/new">Create one</a>, then come back to connect it.</p>
            {:else}
              <form class="row use" onsubmit={useInApp}>
                <label class="field">
                  <span>App</span>
                  <select bind:value={useApp} onchange={() => (replaceVar = false)}>
                    {#each otherApps as x (x.id)}<option value={x.id}>{x.name}</option>{/each}
                  </select>
                </label>
                <label class="field">
                  <span>Variable</span>
                  <input type="text" bind:value={useVar} class="mono" spellcheck="false" oninput={() => (replaceVar = false)} />
                </label>
                <button class="btn primary" disabled={!!busy}>{replaceVar ? 'Replace and redeploy' : 'Add and redeploy'}</button>
                <button type="button" class="btn ghost" onclick={() => { useOpen = false; replaceVar = false; }}>Cancel</button>
              </form>
              {#if replaceVar}<p class="warn">{otherApps.find((x) => x.id === Number(useApp))?.name} already has {useVar}. Replace it, or type another variable name.</p>{/if}
              <small class="faint">Adds the connection URL to the app's environment variables and redeploys it.</small>
            {/if}
          {/if}
        </section>

        {#if detail.aiAccessSupported !== false}
        <section class="card">
          <div class="row between">
            <h3>AI read-only access</h3>
            <span class="pill" class:on={detail.aiAccess}>{detail.aiAccess ? 'On' : 'Off'}</span>
          </div>
          <p class="muted">
            Lets AI assistants connected to SimplePanel look at this database's tables and run read-only queries.
            They use a separate user that can only read this database's data. Writes are refused, queries stop after 10 seconds,
            results are capped at 500 rows, and every query is listed under AI activity in Settings. The AI never sees any password.
          </p>
          <div class="row end">
            {#if aiMsg}<span class="muted small">{aiMsg}</span>{/if}
            <button class="btn {detail.aiAccess ? '' : 'primary'}" disabled={busy === 'ai' || !canDeploy} onclick={toggleAI}>
              {busy === 'ai' ? 'Working…' : detail.aiAccess ? 'Turn off' : 'Allow AI read-only access'}
            </button>
          </div>
        </section>
        {/if}
      {:else}
      <section class="card">
        <h3>Domains</h3>
        {#if detail.autoHost || (!detail.app.autoDomain && detail.wildcard)}
          <div class="auto">
            <Icon name="link" size={16} />
            {#if detail.app.autoDomain}
              <a href="https://{detail.autoHost}" target="_blank" rel="noopener noreferrer">{detail.autoHost}</a>
              <span class="faint">→ port {detail.autoPort} · automatic</span>
            {:else}
              <span class="faint">Automatic subdomain is off</span>
            {/if}
            {#if canDeploy}
              <button class="btn ghost small" disabled={autoBusy} onclick={() => setAuto(!detail.app.autoDomain, detail.app.httpPort)}>{detail.app.autoDomain ? 'Turn off' : 'Turn on'}</button>
            {/if}
          </div>
          {#if ui.advanced && canDeploy && detail.app.autoDomain}
            <form class="row" onsubmit={(e) => { e.preventDefault(); setAuto(true, autoPort); }}>
              <label class="field port">
                <span>Automatic subdomain port</span>
                <input type="number" min="0" max="65535" bind:value={autoPort} placeholder={String(detail.autoPort)} />
              </label>
              <button class="btn" disabled={autoBusy}>Set port</button>
              <small class="faint">0 uses the port of the first domain below, or 80.</small>
            </form>
          {/if}
        {/if}
        {#if detail.domains.length === 0 && !detail.autoHost}
          <p class="muted">
            No domains yet, so this app is only reachable by other apps in this project. Add a domain to put it on the internet with HTTPS.
            {#if !detail.wildcard && isAdmin()}Or set up <a href="#/settings">automatic subdomains</a> once, and every app gets an address. No domain of your own? It can use a free sslip.io address.{/if}
          </p>
        {/if}
        <ul class="domains">
          {#each detail.domains as d (d.id)}
            <li>
              <div class="dline">
                <Icon name="globe" size={16} />
                <a href="{d.https ? 'https' : 'http'}://{d.host}" target="_blank" rel="noopener noreferrer">{d.host}</a>
                {#if editPort === d.id}
                  <form class="row tight inline" onsubmit={(e) => { e.preventDefault(); setDomainPort(d, editPortValue); }}>
                    <input type="number" min="0" max="65535" bind:value={editPortValue} placeholder="auto" aria-label="App port" class="portin" />
                    <button class="btn small primary">Save</button>
                    <button type="button" class="btn ghost small" onclick={() => (editPort = 0)}>Cancel</button>
                  </form>
                {:else}
                  <span class="meta">
                    <span class="faint">→ port {routePort(d)}{d.port ? '' : ' (automatic)'}{d.https ? '' : ' · HTTP only'}</span>
                    {#if canDeploy}
                      <span class="acts">
                        <button class="btn ghost small" onclick={() => { editPort = d.id; editPortValue = d.port || ''; }}>Change port</button>
                        <button class="btn ghost small" onclick={() => removeDomain(d.id)}>Remove</button>
                      </span>
                    {/if}
                  </span>
                {/if}
              </div>
              {#if d.port && detail.app.imagePort && d.port !== detail.app.imagePort}
                <p class="warn">
                  The app's image listens on port {detail.app.imagePort}, but this domain sends visitors to port {d.port}.
                  {#if canDeploy}<button class="linkish" onclick={() => setDomainPort(d, 0)}>Send it to {detail.app.imagePort}</button>{/if}
                </p>
              {/if}
              <DomainStatus host={d.host} https={d.https} {serverIp} />
            </li>
          {/each}
        </ul>
        {#if canDeploy}
        <form class="row" onsubmit={addDomain}>
          <label class="field host">
            <span>Domain</span>
            <input type="text" bind:value={newDomain.host} placeholder="app.example.com" required />
          </label>
          <label class="field port">
            <span>App port</span>
            <input type="number" min="0" max="65535" bind:value={newDomain.port} placeholder={detail.app.imagePort ? `auto (${detail.app.imagePort})` : 'auto'} />
          </label>
          {#if ui.advanced}
            <label class="check"><input type="checkbox" bind:checked={newDomain.https} /> HTTPS</label>
          {/if}
          <button class="btn">Add domain</button>
        </form>
        {/if}
        {#if domainErr}<p class="error">{domainErr}</p>{/if}
        {#if canDeploy}<small class="faint">
          First add an A record for the domain pointing at {serverIp ? serverIp : "this server's IP address"}. HTTPS certificates are issued automatically.
          Leave the port empty to use the one the app listens on.
        </small>{/if}
      </section>

      {#if detail.app.source === 'git' && !isPreview}
        {#key aid}
          <GitCard {detail} {aid} {pid} {canDeploy} onchange={() => refresh(false)} onrotate={rotateWebhook} />
        {/key}
      {/if}

      {#if ui.advanced && detail.webhookPath && detail.app.source !== 'git'}
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
    {:else if tab === 'query' && detail.canQuery}
      {#key aid}
        <QueryView {aid} appName={detail.app.name} engine={detail.database?.engine || detail.app.kind} {canDeploy} />
      {/key}
    {:else if tab === 'cron' && !isPreview}
      {#key aid}
        <CronJobs {aid} {canDeploy} appName={detail.app.name} {isDb} />
      {/key}
    {:else if tab === 'metrics'}
      <Metrics path="/apps/{aid}/metrics" />
    {:else if tab === 'backups' && detail.backupKind}
      <BackupsView appId={aid} appName={detail.app.name} isDb={isDb} engine={detail.database?.engine || ''} level={level} />
    {:else if tab === 'environment' && canDeploy}
      <form class="card" onsubmit={saveEnv}>
        <h3>Environment variables</h3>
        <p class="muted">One <code>KEY=value</code> per line, or paste a whole <code>.env</code> file. Lines starting with # are ignored. Changes apply when the app deploys.</p>
        <textarea bind:value={env} rows="12" spellcheck="false" placeholder="DATABASE_URL=postgres://…&#10;NODE_ENV=production"></textarea>
        <div class="row between envbar">
          {#if dbs.length}
            <select class="insert" aria-label="Insert a database's connection URL" onchange={(e) => { insertDb(Number(e.currentTarget.value)); e.currentTarget.value = ''; }}>
              <option value="">Insert a database URL…</option>
              {#each dbs as x (x.id)}<option value={x.id}>{x.name}</option>{/each}
            </select>
          {:else}<span></span>{/if}
          <div class="row end">
            {#if saved}<span class="ok">{saved}</span>{/if}
            <button class="btn" name="action" value="save" disabled={!!busy}>Save</button>
            <button class="btn primary" name="action" value="deploy" disabled={!!busy}>Save and deploy</button>
          </div>
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
                <span class="dot {depDot(d.status)}"></span>
                <span class="grow">#{d.id} · {depLabel(d.status)}{#if d.commitSha} <code class="sha">{d.commitSha.slice(0, 7)}</code> <span class="faint msg">{d.commitMsg}</span>{/if}</span>
                <span class="faint">{d.trigger}</span>
                <span class="faint">{timeAgo(d.createdAt)}</span>
              </button>
            </li>
          {/each}
        </ul>
      </section>
    {:else if tab === 'settings' && form && isPreview}
      <section class="card">
        <h3>Settings</h3>
        <p class="muted">Previews use the settings of {detail.parent?.name || 'the app they preview'}. Change them there; the next push to the pull request picks them up.</p>
      </section>
      {#if canDeploy}
      <section class="card danger-zone">
        <h3>Remove preview</h3>
        <p class="muted">Stops the preview and deletes its volumes. A new push to the pull request creates it again.</p>
        <div><button class="btn danger" disabled={!!busy} onclick={removeApp}>Remove preview</button></div>
      </section>
      {/if}
    {:else if tab === 'settings' && form}
      <form class="card" onsubmit={saveSettings}>
        <h3>{isDb ? 'Database settings' : 'App settings'}</h3>
        <AppFields bind:value={form} hasToken={detail.app.hasGitToken} database={isDb} bind:more />
        {#if !ui.advanced}
          <div>
            <button type="button" class="linkish" onclick={() => (more = !more)}>{more ? 'Show fewer options' : 'Show more options'}</button>
            {#if !more}<small class="faint">({isDb ? 'version, CPU and memory limits, volumes, ports' : form.source === 'git' ? 'access token, build path, Dockerfile, replicas, limits, volumes, ports, command' : 'replicas, limits, volumes, ports, command'})</small>{/if}
          </div>
        {/if}
        <div class="row end">
          {#if saved}<span class="ok">{saved}</span>{/if}
          {#if canDeploy}<button class="btn primary" disabled={!!busy}>Save</button>{:else}<span class="faint">Your role can view these settings but not change them.</span>{/if}
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

      {#if canManage}
      <section class="card danger-zone">
        <h3>Delete {isDb ? 'database' : 'app'}</h3>
        <p class="muted">{isDb ? 'Removes the database from SimplePanel. Its data stays on the server’s disk in a Docker volume, so it can be recovered by hand.' : 'Removes the app and its domains. Data in its volumes stays on the server’s disk.'}</p>
        <div class="row">
          <label class="field">
            <span>Type the {isDb ? 'database' : 'app'} name to confirm</span>
            <input type="text" bind:value={confirmName} placeholder={detail.app.name} />
          </label>
          <button class="btn danger" disabled={confirmName !== detail.app.name || !!busy} onclick={removeApp}>Delete {isDb ? 'database' : 'app'}</button>
        </div>
      </section>
      {/if}
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
  .page.wide {
    max-width: 1400px;
  }
  .card {
    display: flex;
    flex-direction: column;
    gap: var(--sp-space-3);
  }
  .pill {
    font-size: var(--sp-text-xs);
    font-weight: var(--sp-weight-bold);
    padding: 2px var(--sp-space-2);
    border-radius: var(--sp-radius-round);
    background: var(--sp-bg-main);
    color: var(--sp-text-muted);
    border: 1px solid var(--sp-border);
  }
  .pill.on {
    color: var(--sp-success);
    border-color: var(--sp-success);
  }
  .small {
    font-size: var(--sp-text-sm);
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
    flex-direction: column;
    gap: var(--sp-space-1);
    padding: var(--sp-space-2) 0;
    border-bottom: 1px solid var(--sp-border);
  }
  .domains li:last-child {
    border-bottom: 0;
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
  .routes dd {
    flex-wrap: wrap;
  }
  .routes .hint {
    font-size: var(--sp-text-sm);
  }
  .conn code {
    overflow-wrap: anywhere;
  }
  .auto {
    display: flex;
    align-items: center;
    gap: var(--sp-space-2);
    flex-wrap: wrap;
  }
  .auto a {
    font-weight: var(--sp-weight-medium);
    overflow-wrap: anywhere;
  }
  .auto .btn {
    margin-left: auto;
  }
  .danger-zone {
    border-color: var(--sp-danger);
  }
  .failure {
    border-color: var(--sp-danger);
  }
  .failure h3 {
    color: var(--sp-danger);
  }
  .fix {
    align-items: center;
    gap: var(--sp-space-2);
  }
  .envbar {
    flex-wrap: wrap;
  }
  .envbar .insert {
    flex: 0 1 260px;
    width: auto;
  }
  .envbar .end {
    margin-left: auto;
  }
  .tail {
    margin: 0;
    padding: var(--sp-space-2) var(--sp-space-3);
    background: var(--sp-bg-main);
    border-radius: var(--sp-radius-md);
    font-size: var(--sp-text-xs);
    white-space: pre-wrap;
    overflow-wrap: anywhere;
    max-height: 180px;
    overflow: auto;
  }
  .tokenform {
    flex: 1 1 260px;
  }
  .warn {
    margin: 0;
    color: var(--sp-warning);
    font-size: var(--sp-text-sm);
  }
  .dline {
    display: flex;
    align-items: center;
    gap: var(--sp-space-2);
    flex-wrap: wrap;
    width: 100%;
  }
  .dline > a {
    font-weight: var(--sp-weight-medium);
    overflow-wrap: anywhere;
  }
  .meta {
    display: flex;
    align-items: center;
    gap: var(--sp-space-2);
    flex: 1 1 auto;
    flex-wrap: wrap;
  }
  .acts {
    display: flex;
    gap: var(--sp-space-1);
    margin-left: auto;
  }
  .inline {
    gap: var(--sp-space-2);
  }
  .portin {
    width: 110px;
  }
  .use {
    align-items: flex-end;
  }

  .preview-banner {
    border-color: var(--sp-accent);
  }
  .preview-banner span {
    display: inline-flex;
    align-items: center;
    gap: var(--sp-space-2);
    flex-wrap: wrap;
  }
  .sha {
    font-size: var(--sp-text-xs);
  }
  .msg {
    font-size: var(--sp-text-xs);
  }
  :global(.btn.icon.active) {
    background: var(--sp-bg-active);
  }
  .ico {
    display: inline-flex;
  }
  @media (max-width: 560px) {
    .row.tight {
      flex-wrap: wrap;
    }
    .ico {
      display: none;
    }
    .tabs {
      flex-wrap: wrap;
    }
    .acts {
      margin-left: 0;
    }
  }
</style>
