<script>
  import { copyText } from '../lib/clipboard.js';
  // Right-hand panel: app logs (all running containers, or one launch,
  // past or present) and deployment build logs. Resizable, searchable and
  // downloadable; extra filters show in advanced view.
  import { onMount } from 'svelte';
  import { api } from '../lib/api.js';
  import {
    ui,
    data,
    panel,
    setPanelOpen,
    setPanelWidth,
    panelMax,
    PANEL_MIN,
    logPrefs,
    saveLogPrefs,
    showDeployment,
    timeAgo,
    depLabel,
    depDot,
  } from '../lib/state.svelte.js';
  import { matcher, saveText, download, stamp, runLabel, runDot, duration, bytes, stripAnsi } from '../lib/logs.js';
  import Icon from './Icon.svelte';
  import LogView from './LogView.svelte';

  const aid = ui.route.appId;
  const DEFAULT_WIDTH = 420;

  let logs = $state('');
  let runs = $state([]);
  let deps = $state([]);
  let dep = $state(null);
  let error = $state('');
  let follow = $state(true);
  let query = $state('');
  let shown = $state([]);
  let total = $state(0);
  let menuOpen = $state(false);
  let copied = $state(false);
  let searchEl = $state();

  let app = $derived(data.apps.find((a) => a.id === aid));
  let run = $derived(panel.run ? runs.find((r) => r.number === panel.run) : null);
  let live = $derived(panel.tab === 'logs' ? !run || !run.archived : dep && (dep.status === 'running' || dep.status === 'queued'));
  let search = $derived(matcher(query, logPrefs));
  let re = $derived(search?.re || null);
  let text = $derived(panel.tab === 'logs' ? logs : stripAnsi(dep?.log || ''));
  // Databases and image apps are pulled, not built.
  let pulled = $derived(!!app && (app.source === 'image' || (app.kind && app.kind !== 'app')));

  // ---- Loading ----

  function logsQuery(extra = '') {
    const p = new URLSearchParams();
    p.set('tail', String(logPrefs.tail || 500));
    if (logPrefs.since) p.set('since', String(Math.floor(Date.now() / 1000) - logPrefs.since));
    if (panel.run) p.set('run', String(panel.run));
    return `/apps/${aid}/logs?${p}${extra}`;
  }

  let loading = false;
  async function loadLogs() {
    if (loading) return;
    loading = true;
    const want = panel.run;
    try {
      const r = await api.get(logsQuery());
      if (panel.run !== want || panel.tab !== 'logs') return;
      if (r.logs !== logs) logs = r.logs;
      error = '';
    } catch (err) {
      error = err.message;
    } finally {
      loading = false;
    }
  }

  async function loadRuns() {
    try {
      const r = await api.get(`/apps/${aid}/runs`);
      runs = r.runs;
    } catch {
      /* the logs request reports errors */
    }
  }

  async function loadDeps() {
    try {
      deps = await api.get(`/apps/${aid}/deployments`);
      if (!panel.deploymentId && deps.length) panel.deploymentId = deps[0].id;
    } catch (err) {
      error = err.message;
    }
  }

  async function loadDep() {
    if (!panel.deploymentId) return;
    try {
      const d = await api.get(`/deployments/${panel.deploymentId}`);
      if (d.appId !== aid) {
        // Left over from another app: show this app's newest deployment.
        panel.deploymentId = 0;
        dep = null;
        await loadDeps();
        if (panel.deploymentId) loadDep();
        return;
      }
      dep = d;
      error = '';
      // Keep the picker's label in step with the deployment it shows.
      if (dep) {
        const i = deps.findIndex((x) => x.id === dep.id);
        if (i < 0) loadDeps();
        else if (deps[i].status !== dep.status) deps[i] = { ...deps[i], status: dep.status };
      }
    } catch (err) {
      error = err.message;
    }
  }

  function refresh() {
    if (panel.tab === 'logs') loadLogs();
    else loadDep();
  }

  onMount(() => {
    if (panel.appId !== aid) {
      panel.appId = aid;
      panel.run = 0;
      panel.deploymentId = panel.tab === 'deploy' ? panel.deploymentId : 0;
    }
    loadRuns();
    if (panel.tab === 'deploy') loadDeps().then(loadDep);
    else loadLogs();
    let n = 0;
    const t = setInterval(() => {
      n++;
      if (document.hidden) return;
      if (panel.tab === 'logs') {
        if (live) loadLogs();
        if (n % 5 === 0) loadRuns();
      } else if (!dep || dep.id !== panel.deploymentId || live) {
        loadDep();
        if (n % 5 === 0 || (dep && !live)) loadDeps();
      }
    }, 2000);
    return () => clearInterval(t);
  });

  async function switchTab(t) {
    panel.tab = t;
    error = '';
    follow = true;
    if (t === 'logs') {
      loadLogs();
      loadRuns();
    } else {
      await loadDeps();
      loadDep();
    }
  }

  function pickRun(e) {
    panel.run = Number(e.currentTarget.value) || 0;
    logs = '';
    follow = true;
    loadLogs();
  }

  function pickDep(e) {
    panel.deploymentId = Number(e.currentTarget.value) || 0;
    dep = null;
    follow = true;
    loadDep();
  }

  function setPref(k, v) {
    logPrefs[k] = v;
    saveLogPrefs();
    if (k === 'tail' || k === 'since') loadLogs();
  }

  // ---- Export ----

  function baseName() {
    const name = app ? `${app.projectName || 'app'}-${app.name}` : `app-${aid}`;
    if (panel.tab === 'deploy') return `${name}-deploy-${dep?.id ?? ''}`;
    return panel.run ? `${name}-launch-${panel.run}` : name;
  }

  function downloadFull() {
    menuOpen = false;
    if (panel.tab === 'deploy') {
      if (dep) saveText(`${baseName()}.log`, dep.log || '');
      return;
    }
    const p = new URLSearchParams({ download: '1', tail: '0' });
    if (panel.run) p.set('run', String(panel.run));
    download(`/api/apps/${aid}/logs?${p}`);
  }

  function downloadShown() {
    menuOpen = false;
    saveText(`${baseName()}-${stamp()}${re || logPrefs.level !== 'all' ? '-filtered' : ''}.log`, shown.map((l) => l.raw).join('\n') + '\n');
  }

  async function copyShown() {
    try {
      await copyText(shown.map((l) => l.raw).join('\n'));
      copied = true;
      setTimeout(() => (copied = false), 1500);
    } catch {
      error = "Couldn't copy to the clipboard.";
    }
    menuOpen = false;
  }

  // ---- Resizing ----

  let width = $derived(ui.panelWidth || DEFAULT_WIDTH);
  let wide = $derived(ui.panelWidth > 0 && ui.panelWidth >= panelMax() - 1);
  let before = DEFAULT_WIDTH;
  let dragging = $state(false);

  function gripDown(e) {
    if (e.button !== 0) return;
    dragging = true;
    e.currentTarget.setPointerCapture(e.pointerId);
    e.preventDefault();
  }
  function gripMove(e) {
    if (dragging) setPanelWidth(window.innerWidth - e.clientX);
  }
  function gripUp() {
    dragging = false;
  }
  function gripKey(e) {
    const step = e.shiftKey ? 96 : 32;
    if (e.key === 'ArrowLeft') setPanelWidth(width + step);
    else if (e.key === 'ArrowRight') setPanelWidth(width - step);
    else if (e.key === 'Home') setPanelWidth(PANEL_MIN);
    else if (e.key === 'End') setPanelWidth(panelMax());
    else return;
    e.preventDefault();
  }
  function toggleWide() {
    if (wide) setPanelWidth(before === DEFAULT_WIDTH ? 0 : before);
    else {
      before = width;
      setPanelWidth(panelMax());
    }
  }

  function onResize() {
    // Below 1100px the panel overlays the page, so its saved width can stay.
    if (window.innerWidth > 1100 && ui.panelWidth > panelMax()) setPanelWidth(panelMax());
  }

  function onKey(e) {
    if (e.key === 'Escape' && menuOpen) {
      menuOpen = false;
      return;
    }
    const t = e.target;
    const typing = t.closest?.('input, textarea, select, [contenteditable]');
    if (e.key === '/' && !typing && !e.metaKey && !e.ctrlKey) {
      e.preventDefault();
      searchEl?.focus();
    }
  }

  function onDocClick(e) {
    if (menuOpen && !e.target.closest?.('.menu-wrap')) menuOpen = false;
  }

  function searchKey(e) {
    if (e.key === 'Escape' && query) {
      query = '';
      e.stopPropagation();
    }
  }

  function runText(r) {
    const when = timeAgo(r.startedAt);
    return `Launch #${r.number} · ${runLabel(r)} · ${when}`;
  }
</script>

<svelte:window onresize={onResize} onkeydown={onKey} onclick={onDocClick} />

<aside class="panel" class:dragging aria-label="Logs">
  <!-- A focusable separator is the ARIA window-splitter pattern. -->
  <!-- svelte-ignore a11y_no_noninteractive_element_interactions, a11y_no_noninteractive_tabindex -->
  <div
    class="grip"
    role="separator"
    aria-orientation="vertical"
    aria-label="Resize panel"
    aria-valuemin={PANEL_MIN}
    aria-valuemax={panelMax()}
    aria-valuenow={width}
    tabindex="0"
    title="Drag to resize. Double-click to reset."
    onpointerdown={gripDown}
    onpointermove={gripMove}
    onpointerup={gripUp}
    onpointercancel={gripUp}
    ondblclick={() => setPanelWidth(0)}
    onkeydown={gripKey}
  ></div>

  <header>
    <div class="segmented">
      <button class:active={panel.tab === 'logs'} onclick={() => switchTab('logs')}>App logs</button>
      <button class:active={panel.tab === 'deploy'} onclick={() => switchTab('deploy')}>{pulled ? 'Deploy log' : 'Build log'}</button>
    </div>
    <div class="head-actions">
      <button class="btn ghost icon wide-btn" aria-label={wide ? 'Make panel narrower' : 'Make panel wider'} title={wide ? 'Narrower' : 'Wider'} onclick={toggleWide}>
        <Icon name={wide ? 'shrink' : 'expand'} size={16} />
      </button>
      <button class="btn ghost icon" aria-label="Close panel" onclick={() => setPanelOpen(false)}><Icon name="x" size={18} /></button>
    </div>
  </header>

  <div class="source">
    {#if panel.tab === 'logs'}
      <select aria-label="Which logs" value={panel.run} onchange={pickRun} onfocus={loadRuns}>
        <option value={0}>All running containers · live</option>
        {#if runs.length}
          <optgroup label="Launches (each container start)">
            {#each runs as r (r.id)}
              <option value={r.number}>{runText(r)}</option>
            {/each}
          </optgroup>
        {/if}
      </select>
    {:else}
      <select aria-label="Which deployment" value={panel.deploymentId} onchange={pickDep}>
        {#if !deps.length}<option value={0}>No deployments yet</option>{/if}
        {#each deps as d (d.id)}
          <option value={d.id}>Deployment #{d.id} · {depLabel(d.status)} · {timeAgo(d.createdAt)}</option>
        {/each}
      </select>
    {/if}
  </div>

  {#if panel.tab === 'logs' && run}
    <div class="meta">
      <span class="dot {runDot(run)}"></span>
      <span>{runLabel(run)}</span>
      <span class="faint">
        · ran {duration((run.endedAt ?? Math.floor(Date.now() / 1000)) - run.startedAt)}
        {#if run.endedAt}· ended {timeAgo(run.endedAt)}{/if}
      </span>
      {#if run.deploymentId}
        <button class="linkish" onclick={() => showDeployment(run.deploymentId)}>from deploy #{run.deploymentId}</button>
      {/if}
      {#if ui.advanced}
        <span class="faint mono" title="Swarm task {run.taskId}">· replica {run.slot} · {run.image}</span>
      {/if}
    </div>
    {#if run.message && runDot(run) === 'failed' && !/non-zero exit/.test(run.message)}<p class="run-msg">{run.message}</p>{/if}
    {#if run.truncated}<p class="note">Only the last {bytes(run.logBytes)} of this launch's output was kept.</p>{/if}
  {:else if panel.tab === 'deploy' && dep}
    <div class="meta">
      <span class="dot {depDot(dep.status)}"></span> <span>{depLabel(dep.status)}</span>
      <span class="faint">· {dep.trigger}{dep.commitSha ? ` · ${dep.commitSha.slice(0, 7)}` : ''}</span>
    </div>
  {/if}

  <div class="tools">
    <label class="search" class:bad={search?.error}>
      <Icon name="search" size={14} />
      <input
        bind:this={searchEl}
        type="search"
        placeholder="Search logs"
        aria-label="Search logs"
        bind:value={query}
        onkeydown={searchKey}
        spellcheck="false"
      />
      {#if query && !search?.error}<span class="count">{shown.length}</span>{/if}
    </label>
    <div class="menu-wrap">
      <button class="btn small" aria-haspopup="menu" aria-expanded={menuOpen} onclick={() => (menuOpen = !menuOpen)} title="Download or copy">
        <Icon name="download" size={14} /> <span class="lbl">{copied ? 'Copied' : 'Export'}</span>
      </button>
      {#if menuOpen}
        <div class="menu" role="menu">
          <button role="menuitem" onclick={downloadFull}>
            <strong>Download full log</strong>
            <span>{panel.tab === 'deploy' ? 'The whole build log' : panel.run ? `Everything launch #${panel.run} logged` : 'Up to 100,000 lines'} as .log</span>
          </button>
          <button role="menuitem" onclick={downloadShown} disabled={!shown.length}>
            <strong>Download what's shown</strong>
            <span>{shown.length} line{shown.length === 1 ? '' : 's'}{re || logPrefs.level !== 'all' ? ', filtered' : ''}</span>
          </button>
          <button role="menuitem" onclick={copyShown} disabled={!shown.length}>
            <strong>Copy what's shown</strong>
            <span>To the clipboard</span>
          </button>
        </div>
      {/if}
    </div>
  </div>

  {#if ui.advanced}
    <div class="adv">
      <label>
        <span>Level</span>
        <select value={logPrefs.level} onchange={(e) => setPref('level', e.currentTarget.value)}>
          <option value="all">All</option>
          <option value="no-debug">Hide debug</option>
          <option value="warn">Warnings + errors</option>
          <option value="error">Errors only</option>
        </select>
      </label>
      {#if panel.tab === 'logs'}
        <label>
          <span>Since</span>
          <select value={logPrefs.since} onchange={(e) => setPref('since', Number(e.currentTarget.value))}>
            <option value={0}>Any time</option>
            <option value={300}>5 min</option>
            <option value={900}>15 min</option>
            <option value={3600}>1 hour</option>
            <option value={21600}>6 hours</option>
            <option value={86400}>24 hours</option>
            <option value={604800}>7 days</option>
          </select>
        </label>
        <label>
          <span>Lines</span>
          <select value={logPrefs.tail} onchange={(e) => setPref('tail', Number(e.currentTarget.value))}>
            <option value={100}>100</option>
            <option value={500}>500</option>
            <option value={1000}>1,000</option>
            <option value={2000}>2,000</option>
            <option value={5000}>5,000</option>
          </select>
        </label>
      {/if}
      <div class="toggles">
        <button class="chip" class:on={logPrefs.wrap} aria-pressed={logPrefs.wrap} onclick={() => setPref('wrap', !logPrefs.wrap)}>Wrap</button>
        <button class="chip" class:on={logPrefs.timestamps} aria-pressed={logPrefs.timestamps} onclick={() => setPref('timestamps', !logPrefs.timestamps)}>Time</button>
        <button class="chip mono" class:on={logPrefs.regex} aria-pressed={logPrefs.regex} title="Search with a regular expression" onclick={() => setPref('regex', !logPrefs.regex)}>.*</button>
        <button class="chip" class:on={logPrefs.caseSensitive} aria-pressed={logPrefs.caseSensitive} title="Match case" onclick={() => setPref('caseSensitive', !logPrefs.caseSensitive)}>Aa</button>
      </div>
    </div>
  {/if}

  {#if error}<p class="error">{error}</p>{/if}
  {#if search?.error}<p class="error">Search: {search.error}</p>{/if}

  <LogView
    {text}
    {re}
    level={ui.advanced ? logPrefs.level : 'all'}
    wrap={ui.advanced ? logPrefs.wrap : true}
    timestamps={ui.advanced ? logPrefs.timestamps : true}
    bind:follow
    bind:shown
    bind:total
    empty={panel.tab === 'logs'
      ? panel.run
        ? 'This launch logged nothing.'
        : logPrefs.since && ui.advanced
          ? 'Nothing logged in this time range.'
          : 'No logs yet.'
      : dep
        ? 'No build output.'
        : 'No deployment selected.'}
  />

  <footer>
    <label class="check"><input type="checkbox" bind:checked={follow} /> Follow</label>
    <span class="status">
      {#if live}
        <span class="live"><span class="pulse"></span>Live</span>
      {:else if panel.tab === 'logs' && run}
        <span>Saved</span>
      {/if}
      <span class="faint">
        {shown.length !== total ? `${shown.length.toLocaleString()} of ` : ''}{total.toLocaleString()} line{total === 1 ? '' : 's'}
      </span>
      <button class="btn ghost icon tiny" aria-label="Refresh" title="Refresh" onclick={refresh}><Icon name="refresh" size={14} /></button>
    </span>
  </footer>
</aside>

<style>
  .panel {
    position: relative;
    height: 100%;
    min-height: 0;
    min-width: 0;
    display: flex;
    flex-direction: column;
    background: var(--sp-bg-panel);
    border-left: 1px solid var(--sp-border);
  }
  .grip {
    position: absolute;
    left: -4px;
    top: 0;
    bottom: 0;
    width: 8px;
    cursor: col-resize;
    z-index: 2;
    touch-action: none;
  }
  .grip::after {
    content: '';
    position: absolute;
    left: 3px;
    top: 0;
    bottom: 0;
    width: 2px;
    background: transparent;
    transition: background var(--sp-transition);
  }
  .grip:hover::after,
  .grip:focus-visible::after,
  .dragging .grip::after {
    background: var(--sp-accent);
  }
  .grip:focus-visible {
    outline: none;
  }
  .dragging {
    user-select: none;
  }
  header {
    height: var(--sp-header-height);
    flex: none;
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--sp-space-2);
    padding: 0 var(--sp-space-3);
    border-bottom: 1px solid var(--sp-border);
  }
  .head-actions {
    display: flex;
    gap: var(--sp-space-1);
  }
  .source {
    flex: none;
    padding: var(--sp-space-2) var(--sp-space-3) 0;
  }
  .source select {
    width: 100%;
  }
  .meta {
    flex: none;
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--sp-space-1) var(--sp-space-2);
    padding: var(--sp-space-2) var(--sp-space-3) 0;
    font-size: var(--sp-text-sm);
    color: var(--sp-text);
  }
  .mono {
    font-family: var(--sp-font-mono);
    font-size: var(--sp-text-xs);
  }
  .run-msg,
  .note {
    flex: none;
    margin: var(--sp-space-2) var(--sp-space-3) 0;
    font-size: var(--sp-text-sm);
    overflow-wrap: anywhere;
  }
  .run-msg {
    color: var(--sp-danger);
  }
  .note {
    color: var(--sp-text-faint);
  }
  .tools {
    flex: none;
    display: flex;
    gap: var(--sp-space-2);
    align-items: center;
    padding: var(--sp-space-2) var(--sp-space-3);
  }
  .search {
    flex: 1;
    min-width: 0;
    display: flex;
    align-items: center;
    gap: var(--sp-space-2);
    height: 32px;
    padding: 0 var(--sp-space-2);
    border: 1px solid var(--sp-border);
    border-radius: var(--sp-radius-md);
    background: var(--sp-bg-input);
    color: var(--sp-text-faint);
  }
  .search:focus-within {
    border-color: var(--sp-accent);
    box-shadow: 0 0 0 3px var(--sp-focus-ring);
  }
  .search.bad {
    border-color: var(--sp-danger);
  }
  .search input {
    flex: 1;
    min-width: 0;
    height: 100%;
    border: 0 !important;
    padding: 0 !important;
    background: transparent !important;
    box-shadow: none !important;
    outline: none;
    color: var(--sp-text);
    font-size: var(--sp-text-sm);
  }
  .count {
    font-size: var(--sp-text-xs);
    color: var(--sp-text-muted);
  }
  .menu-wrap {
    position: relative;
  }
  .menu-wrap .btn {
    gap: var(--sp-space-1);
    height: 32px;
  }
  .menu {
    position: absolute;
    right: 0;
    top: calc(100% + 4px);
    z-index: 5;
    width: 250px;
    padding: var(--sp-space-1);
    border: 1px solid var(--sp-border-strong);
    border-radius: var(--sp-radius-md);
    background: var(--sp-bg-elevated);
    box-shadow: var(--sp-shadow);
  }
  .menu button {
    display: flex;
    flex-direction: column;
    gap: 2px;
    width: 100%;
    padding: var(--sp-space-2);
    border: 0;
    border-radius: var(--sp-radius-sm);
    background: none;
    color: var(--sp-text);
    font: inherit;
    font-size: var(--sp-text-sm);
    text-align: left;
    cursor: pointer;
  }
  .menu button:hover:not(:disabled) {
    background: var(--sp-bg-hover);
  }
  .menu button:disabled {
    opacity: 0.5;
    cursor: default;
  }
  .menu strong {
    font-weight: var(--sp-weight-medium);
  }
  .menu span {
    color: var(--sp-text-muted);
    font-size: var(--sp-text-xs);
  }
  .adv {
    flex: none;
    display: flex;
    flex-wrap: wrap;
    gap: var(--sp-space-2);
    align-items: center;
    padding: 0 var(--sp-space-3) var(--sp-space-2);
    font-size: var(--sp-text-xs);
    color: var(--sp-text-muted);
  }
  .adv label {
    display: flex;
    align-items: center;
    gap: var(--sp-space-1);
  }
  .adv select {
    height: 28px;
    padding-top: 0;
    padding-bottom: 0;
    font-size: var(--sp-text-xs);
  }
  .toggles {
    display: flex;
    gap: 2px;
    margin-left: auto;
  }
  .chip {
    height: 28px;
    min-width: 32px;
    padding: 0 var(--sp-space-2);
    border: 1px solid var(--sp-border);
    border-radius: var(--sp-radius-sm);
    background: transparent;
    color: var(--sp-text-muted);
    font: inherit;
    font-size: var(--sp-text-xs);
    cursor: pointer;
  }
  .chip.on {
    background: var(--sp-bg-active);
    border-color: var(--sp-border-strong);
    color: var(--sp-text);
  }
  .error {
    flex: none;
    margin: 0 var(--sp-space-3) var(--sp-space-2);
  }
  footer {
    flex: none;
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--sp-space-2);
    padding: var(--sp-space-1) var(--sp-space-2) var(--sp-space-1) var(--sp-space-3);
    border-top: 1px solid var(--sp-border);
    font-size: var(--sp-text-xs);
  }
  .check {
    display: flex;
    gap: var(--sp-space-2);
    align-items: center;
    font-size: var(--sp-text-sm);
    color: var(--sp-text-muted);
  }
  .status {
    display: flex;
    align-items: center;
    gap: var(--sp-space-2);
    color: var(--sp-text-muted);
  }
  .live {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    color: var(--sp-success);
  }
  .pulse {
    width: 6px;
    height: 6px;
    border-radius: var(--sp-radius-round);
    background: var(--sp-success);
    animation: pulse 2s ease-in-out infinite;
  }
  @keyframes pulse {
    50% {
      opacity: 0.3;
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .pulse {
      animation: none;
    }
  }
  .tiny {
    width: 26px;
    height: 26px;
  }

  @media (max-width: 1100px) {
    .panel {
      position: fixed;
      inset: 0 0 0 auto;
      width: min(var(--sp-panel-width), 100vw);
      z-index: 25;
      box-shadow: var(--sp-shadow);
    }
    .grip {
      display: none;
    }
    .wide-btn {
      display: none;
    }
  }
  @media (max-width: 420px) {
    .lbl {
      display: none;
    }
  }
</style>
