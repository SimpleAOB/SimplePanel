<script>
  // Right-hand panel: live app logs, or a deployment's build log.
  import { onMount, tick } from 'svelte';
  import { api } from '../lib/api.js';
  import { ui, panel, setPanelOpen } from '../lib/state.svelte.js';
  import Icon from './Icon.svelte';

  const aid = ui.route.appId;
  let logs = $state('');
  let dep = $state(null);
  let error = $state('');
  let follow = $state(true);
  let box;

  async function scrollEnd() {
    await tick();
    if (follow && box) box.scrollTop = box.scrollHeight;
  }

  async function loadLogs() {
    try {
      const r = await api.get(`/apps/${aid}/logs?tail=500`);
      logs = r.logs;
      error = '';
      scrollEnd();
    } catch (err) {
      error = err.message;
    }
  }

  async function loadDep() {
    if (!panel.deploymentId) return;
    try {
      dep = await api.get(`/deployments/${panel.deploymentId}`);
      if (dep.appId !== aid) {
        dep = null;
        return;
      }
      error = '';
      scrollEnd();
    } catch (err) {
      error = err.message;
    }
  }

  async function latestDeployment() {
    const deps = await api.get(`/apps/${aid}/deployments`);
    if (deps.length) panel.deploymentId = deps[0].id;
  }

  onMount(() => {
    if (panel.tab === 'deploy' && panel.deploymentId) loadDep();
    else if (panel.tab === 'deploy') latestDeployment().then(loadDep);
    else loadLogs();
    const t = setInterval(() => {
      if (panel.tab === 'logs') loadLogs();
      else if (!dep || dep.status === 'running' || dep.status === 'queued' || dep.id !== panel.deploymentId) loadDep();
    }, 2000);
    return () => clearInterval(t);
  });

  async function switchTab(t) {
    panel.tab = t;
    error = '';
    if (t === 'logs') loadLogs();
    else {
      if (!panel.deploymentId) await latestDeployment().catch(() => {});
      loadDep();
    }
  }
</script>

<aside class="panel" aria-label="Details">
  <header>
    <div class="segmented">
      <button class:active={panel.tab === 'logs'} onclick={() => switchTab('logs')}>App logs</button>
      <button class:active={panel.tab === 'deploy'} onclick={() => switchTab('deploy')}>Build log</button>
    </div>
    <button class="btn ghost icon" aria-label="Close panel" onclick={() => setPanelOpen(false)}><Icon name="x" size={18} /></button>
  </header>
  {#if panel.tab === 'deploy' && dep}
    <div class="meta">
      <span class="dot {dep.status}"></span> Deployment #{dep.id} · {dep.status}
    </div>
  {/if}
  {#if error}<p class="error">{error}</p>{/if}
  <pre bind:this={box} class="log">{panel.tab === 'logs' ? logs || 'No logs yet.' : dep?.log || 'No deployment selected.'}</pre>
  <footer>
    <label class="check"><input type="checkbox" bind:checked={follow} /> Follow</label>
  </footer>
</aside>

<style>
  .panel {
    height: 100%;
    min-height: 0;
    display: flex;
    flex-direction: column;
    background: var(--sp-bg-panel);
    border-left: 1px solid var(--sp-border);
  }
  header {
    height: var(--sp-header-height);
    flex: none;
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 0 var(--sp-space-3);
    border-bottom: 1px solid var(--sp-border);
  }
  .meta {
    display: flex;
    align-items: center;
    gap: var(--sp-space-2);
    padding: var(--sp-space-2) var(--sp-space-3);
    font-size: var(--sp-text-sm);
    color: var(--sp-text-muted);
  }
  .error {
    margin: var(--sp-space-2) var(--sp-space-3);
  }
  .log {
    flex: 1;
    min-height: 0;
    margin: 0;
    padding: var(--sp-space-3);
    overflow: auto;
    background: var(--sp-bg-code);
    color: var(--sp-text);
    font-size: var(--sp-text-xs);
    line-height: 1.6;
    white-space: pre-wrap;
    word-break: break-all;
  }
  footer {
    flex: none;
    padding: var(--sp-space-2) var(--sp-space-3);
    border-top: 1px solid var(--sp-border);
  }
  .check {
    display: flex;
    gap: var(--sp-space-2);
    align-items: center;
    font-size: var(--sp-text-sm);
    color: var(--sp-text-muted);
  }

  @media (max-width: 1100px) {
    .panel {
      position: fixed;
      inset: 0 0 0 auto;
      width: min(var(--sp-panel-width), 100vw);
      z-index: 25;
      box-shadow: var(--sp-shadow);
    }
  }
</style>
