<script>
  // Backups of one service (a database or an app's volumes) or of
  // SimplePanel's own configuration (panel = true): schedules, the backup
  // list, restores, and scanning a bucket for backups made elsewhere.
  import { onMount } from 'svelte';
  import { api } from '../lib/api.js';
  import { ui, timeAgo, isAdmin } from '../lib/state.svelte.js';
  import { bytes, dateTime, schedulePresets, describeSchedule } from '../lib/format.js';
  import Icon from './Icon.svelte';
  import Modal from './Modal.svelte';

  let { panel = false, appId = 0, appName = '', isDb = false, level = 3 } = $props();

  let base = $derived(panel ? '/backups/panel' : `/apps/${appId}/backups`);
  let canRun = $derived(panel || level >= 2);
  let canManage = $derived(panel || level >= 3);

  let ov = $state(null);
  let err = $state('');
  let busy = $state('');
  let runDest = $state(0);
  let adding = $state(null); // new schedule form
  let addErr = $state('');
  let openLog = $state(0);
  let logText = $state('');
  let restoring = $state(null); // { backup?, remote?, mode, newName, confirm, running?: restore }
  let restoreErr = $state('');
  let deleting = $state(null);
  let scanDest = $state(0);
  let remote = $state(null);
  let scanErr = $state('');

  async function load() {
    try {
      ov = await api.get(base);
      if (!runDest && ov.destinations.length) runDest = ov.jobs[0]?.destinationId || ov.destinations[0].id;
      if (!scanDest && ov.destinations.length) scanDest = ov.destinations[0].id;
    } catch (e) {
      err = e.message;
    }
  }
  onMount(() => {
    load();
    const t = setInterval(() => {
      if (ov?.backups.some((b) => b.status === 'running') || ov?.restores.some((r) => r.status === 'running')) load();
    }, 3000);
    return () => clearInterval(t);
  });

  async function act(name, fn) {
    busy = name;
    err = '';
    try {
      await fn();
      await load();
    } catch (e) {
      err = e.message;
    } finally {
      busy = '';
    }
  }

  const runNow = () => act('run', () => api.post(base, { destinationId: Number(runDest) }));

  function newSchedule() {
    addErr = '';
    adding = { destinationId: ov.destinations[0]?.id, preset: '0 3 * * *', custom: '', keep: 7 };
  }
  async function saveSchedule(e) {
    e.preventDefault();
    addErr = '';
    const schedule = adding.preset === 'custom' ? adding.custom : adding.preset;
    try {
      await api.post(panel ? '/backups/panel/jobs' : `/apps/${appId}/backup-jobs`, {
        destinationId: Number(adding.destinationId),
        schedule,
        keep: Number(adding.keep),
      });
      adding = null;
      await load();
    } catch (e2) {
      addErr = e2.message;
    }
  }
  const toggleJob = (j) =>
    act('job', () => api.put(`/backup-jobs/${j.id}`, { destinationId: j.destinationId, schedule: j.schedule, keep: j.keep, enabled: !j.enabled }));
  const deleteJob = (j) => act('job', () => api.del(`/backup-jobs/${j.id}`));

  async function showLog(kind, id) {
    if (openLog === `${kind}${id}`) {
      openLog = 0;
      return;
    }
    openLog = `${kind}${id}`;
    logText = 'Loading…';
    try {
      const r = await api.get(kind === 'b' ? `/backups/${id}` : `/restores/${id}`);
      logText = r.log || 'No log.';
    } catch (e) {
      logText = e.message;
    }
  }

  function startRestore(src) {
    restoreErr = '';
    restoring = { ...src, mode: isDb ? 'new' : 'replace', newName: `${appName}-restored`.slice(0, 40), confirm: '' };
  }
  async function doRestore(e) {
    e.preventDefault();
    restoreErr = '';
    const body = restoring.backup
      ? { backupId: restoring.backup.id }
      : { destinationId: restoring.remote.destinationId, key: restoring.remote.key };
    if (restoring.mode === 'new') body.newName = restoring.newName;
    else body.confirm = restoring.confirm;
    try {
      const r = await api.post(`/apps/${appId}/restore`, body);
      restoring.running = r;
      pollRestore(r.id);
    } catch (e2) {
      restoreErr = e2.message;
    }
  }
  async function pollRestore(id) {
    while (restoring?.running?.id === id) {
      try {
        const r = await api.get(`/restores/${id}`);
        if (restoring?.running?.id !== id) return;
        restoring.running = r;
        if (r.status !== 'running') {
          load();
          return;
        }
      } catch {
        /* keep trying */
      }
      await new Promise((res) => setTimeout(res, 2000));
    }
  }

  const removeBackup = () =>
    act('delete', async () => {
      await api.del(`/backups/${deleting.id}`);
      deleting = null;
    });

  async function scan() {
    scanErr = '';
    remote = null;
    busy = 'scan';
    try {
      remote = await api.get(`${base}/remote?destination=${scanDest}`);
    } catch (e) {
      scanErr = e.message;
    } finally {
      busy = '';
    }
  }

  function remoteHref(r) {
    return r.backupId
      ? `/api/backups/${r.backupId}/download`
      : `/api/apps/${appId}/backups/file?destination=${r.destinationId}&key=${encodeURIComponent(r.key)}`;
  }

  let what = $derived(panel ? 'SimplePanel’s configuration' : isDb ? 'this database' : 'this app’s volumes');
</script>

{#if err}<p class="error">{err}</p>{/if}

{#if ov}
  {#if ov.destinations.length === 0}
    <section class="card">
      <h3>Backups</h3>
      <p class="muted">
        Backups go to S3-compatible storage such as AWS S3, Cloudflare R2, Backblaze B2 or MinIO.
        {#if isAdmin()}
          Add a destination in <a href="#/settings/backups">Settings › Backups</a> first.
        {:else}
          Ask an admin to add a backup destination first.
        {/if}
      </p>
    </section>
  {:else}
    <section class="card">
      <div class="row between">
        <h3>Schedule</h3>
        {#if canRun && !adding}<button class="btn small" onclick={newSchedule}><Icon name="plus" size={14} /> Add schedule</button>{/if}
      </div>
      {#if ov.jobs.length === 0 && !adding}
        <p class="muted">No automatic backups yet. Add a schedule to back up {what} regularly.</p>
      {/if}
      <ul class="list">
        {#each ov.jobs as j (j.id)}
          <li>
            <Icon name="archive" size={16} />
            <div class="grow">
              <div>{describeSchedule(j.schedule)} <span class="faint">to {j.destination}</span></div>
              <div class="faint small">
                Keeps the last {j.keep} ·
                {#if j.enabled && j.nextRunAt}Next {dateTime(j.nextRunAt)} ({ov.timezone}){:else}Paused{/if}
              </div>
            </div>
            {#if canRun}
              <button class="btn ghost small" disabled={busy === 'job'} onclick={() => toggleJob(j)}>{j.enabled ? 'Pause' : 'Resume'}</button>
              <button class="btn ghost small icon" title="Delete schedule" aria-label="Delete schedule" disabled={busy === 'job'} onclick={() => deleteJob(j)}><Icon name="trash" size={16} /></button>
            {/if}
          </li>
        {/each}
      </ul>
      {#if adding}
        <form class="sched" onsubmit={saveSchedule}>
          <div class="row">
            <label class="field">
              <span>How often</span>
              <select bind:value={adding.preset}>
                {#each schedulePresets as [v, label]}<option value={v}>{label}</option>{/each}
                {#if ui.advanced}<option value="custom">Custom (cron)</option>{/if}
              </select>
            </label>
            {#if adding.preset === 'custom'}
              <label class="field">
                <span>Cron expression</span>
                <input type="text" bind:value={adding.custom} placeholder="30 2 * * 1-5" class="mono" required />
              </label>
            {/if}
            <label class="field">
              <span>Destination</span>
              <select bind:value={adding.destinationId}>
                {#each ov.destinations as d (d.id)}<option value={d.id}>{d.name}{d.encrypted ? ' (encrypted)' : ''}</option>{/each}
              </select>
            </label>
            <label class="field keep">
              <span>Keep</span>
              <input type="number" min="1" max="1000" bind:value={adding.keep} />
            </label>
          </div>
          <small class="faint">
            Times are in the server's time zone ({ov.timezone}). Older scheduled backups beyond “Keep” are deleted; manual backups are kept until you delete them.
            {#if !ui.advanced}Turn on Advanced view for custom cron schedules.{/if}
          </small>
          {#if addErr}<p class="error">{addErr}</p>{/if}
          <div class="row end">
            <button type="button" class="btn ghost" onclick={() => (adding = null)}>Cancel</button>
            <button class="btn primary">Save schedule</button>
          </div>
        </form>
      {/if}
    </section>

    <section class="card">
      <div class="row between">
        <h3>Backups</h3>
        {#if canRun}
          <div class="row tight">
            {#if ov.destinations.length > 1}
              <select bind:value={runDest} aria-label="Destination">
                {#each ov.destinations as d (d.id)}<option value={d.id}>{d.name}</option>{/each}
              </select>
            {/if}
            <button class="btn primary" disabled={busy === 'run'} onclick={runNow}>{busy === 'run' ? 'Starting…' : 'Back up now'}</button>
          </div>
        {/if}
      </div>
      {#if ov.backups.length === 0}
        <p class="muted">No backups yet.</p>
      {/if}
      <ul class="list">
        {#each ov.backups as b (b.id)}
          <li class="col">
            <div class="line">
              <span class="dot {b.status}" title={b.status}></span>
              <button class="link grow" onclick={() => showLog('b', b.id)}>
                <span>{dateTime(b.createdAt)}</span>
                <span class="faint small">
                  {b.status === 'running' ? 'Running…' : b.status === 'failed' ? 'Failed' : bytes(b.size)} · {b.destination}
                  · {b.trigger.startsWith('manual') ? 'manual' : 'scheduled'}
                </span>
              </button>
              {#if b.encrypted}<span class="faint" title="Encrypted"><Icon name="lock" size={14} /></span>{/if}
              {#if b.status === 'succeeded'}
                {#if canRun}
                  <a class="btn ghost small" href="/api/backups/{b.id}/download" download>Download</a>
                {/if}
                {#if !panel && canRun}
                  <button class="btn ghost small" onclick={() => startRestore({ backup: b })}>Restore</button>
                {/if}
              {/if}
              {#if canManage && b.status !== 'running'}
                <button class="btn ghost small icon" title="Delete backup" aria-label="Delete backup" onclick={() => (deleting = b)}><Icon name="trash" size={16} /></button>
              {/if}
            </div>
            {#if openLog === `b${b.id}`}<pre class="log">{logText}</pre>{/if}
          </li>
        {/each}
      </ul>
      {#if panel}
        <small class="faint">
          Restore SimplePanel's configuration by downloading a backup, unzipping it and replacing <code>simplepanel.db</code> in the data folder while SimplePanel is stopped.
        </small>
      {/if}
    </section>

    {#if !panel && ov.restores.length}
      <section class="card">
        <h3>Restores</h3>
        <ul class="list">
          {#each ov.restores as r (r.id)}
            <li class="col">
              <div class="line">
                <span class="dot {r.status}" title={r.status}></span>
                <button class="link grow" onclick={() => showLog('r', r.id)}>
                  <span>{dateTime(r.createdAt)}</span>
                  <span class="faint small">
                    {r.status === 'running' ? 'Running…' : r.status} · by {r.username}
                    {#if r.targetAppId && r.targetAppId !== appId} · into a new database{/if}
                  </span>
                </button>
                {#if r.targetAppId && r.targetAppId !== appId}
                  <a class="btn ghost small" href="#/p/{ui.route.projectId}/a/{r.targetAppId}">Open</a>
                {/if}
              </div>
              {#if openLog === `r${r.id}`}<pre class="log">{logText}</pre>{/if}
            </li>
          {/each}
        </ul>
      </section>
    {/if}

    {#if ui.advanced}
      <section class="card">
        <h3>Find backups in a bucket</h3>
        <p class="muted">
          Lists every backup of {what} stored in a destination, including ones made by another SimplePanel server that used the same bucket and folder.
          Useful after moving to a new server.
        </p>
        <div class="row">
          <label class="field">
            <span>Destination</span>
            <select bind:value={scanDest}>
              {#each ov.destinations as d (d.id)}<option value={d.id}>{d.name}</option>{/each}
            </select>
          </label>
          <button class="btn" disabled={busy === 'scan'} onclick={scan}>{busy === 'scan' ? 'Scanning…' : 'Scan'}</button>
        </div>
        {#if scanErr}<p class="error">{scanErr}</p>{/if}
        {#if remote}
          {#if remote.length === 0}<p class="muted">Nothing found.</p>{/if}
          <ul class="list">
            {#each remote as r (r.key)}
              <li>
                <div class="grow">
                  <div class="mono small">{r.name}</div>
                  <div class="faint small">{new Date(r.time).toLocaleString()} · {bytes(r.size)}{r.backupId ? '' : ' · not in this panel’s list'}</div>
                </div>
                {#if r.encrypted}<span class="faint" title="Encrypted"><Icon name="lock" size={14} /></span>{/if}
                {#if canRun && (r.backupId || !panel)}<a class="btn ghost small" href={remoteHref(r)} download>Download</a>{/if}
                {#if !panel && canRun}<button class="btn ghost small" onclick={() => startRestore({ remote: r })}>Restore</button>{/if}
              </li>
            {/each}
          </ul>
        {/if}
      </section>
    {/if}
  {/if}
{/if}

{#if restoring}
  <Modal title="Restore backup" onclose={() => (restoring = null)}>
    {#if restoring.running}
      {@const r = restoring.running}
      <p>
        <span class="dot {r.status}"></span>
        {r.status === 'running' ? 'Restoring…' : r.status === 'succeeded' ? 'Restore finished.' : 'Restore failed.'}
      </p>
      <pre class="log">{r.log || 'Starting…'}</pre>
      <div class="row end">
        {#if r.status === 'succeeded' && r.targetAppId && r.targetAppId !== appId}
          <a class="btn primary" href="#/p/{ui.route.projectId}/a/{r.targetAppId}" onclick={() => (restoring = null)}>Open the new database</a>
        {/if}
        <button class="btn" onclick={() => (restoring = null)}>Close</button>
      </div>
    {:else}
      <form class="sched" onsubmit={doRestore}>
        <p class="muted">
          From {restoring.backup ? dateTime(restoring.backup.createdAt) : restoring.remote.name}.
        </p>
        {#if isDb}
          <div class="choices">
            <label class="choice" class:active={restoring.mode === 'new'}>
              <input type="radio" value="new" bind:group={restoring.mode} />
              <strong>Into a new database</strong>
              <small class="muted">Creates a new database in this project and loads the backup there. {appName} isn't touched.</small>
            </label>
            <label class="choice" class:active={restoring.mode === 'replace'} class:disabled={!canManage}>
              <input type="radio" value="replace" bind:group={restoring.mode} disabled={!canManage} />
              <strong>Replace {appName}'s data</strong>
              <small class="muted">
                {canManage ? `Tables in the backup replace the ones in ${appName}. Apps using it see the old data right away.` : 'Needs manage access to this project.'}
              </small>
            </label>
          </div>
        {/if}
        {#if restoring.mode === 'new'}
          <label class="field">
            <span>New database name</span>
            <input type="text" bind:value={restoring.newName} required />
          </label>
        {:else}
          <p class="warn">
            {isDb
              ? `This replaces the current data in ${appName}.`
              : `This stops ${appName}, replaces everything in its volumes with the backup, and starts it again.`}
            It can't be undone, so consider backing up first.
          </p>
          <label class="field">
            <span>Type {appName} to confirm</span>
            <input type="text" bind:value={restoring.confirm} placeholder={appName} />
          </label>
        {/if}
        {#if restoreErr}<p class="error">{restoreErr}</p>{/if}
        <div class="row end">
          <button type="button" class="btn ghost" onclick={() => (restoring = null)}>Cancel</button>
          {#if restoring.mode === 'new'}
            <button class="btn primary">Restore into new database</button>
          {:else}
            <button class="btn danger" disabled={restoring.confirm !== appName || !canManage}>Replace data</button>
          {/if}
        </div>
      </form>
    {/if}
  </Modal>
{/if}

{#if deleting}
  <Modal title="Delete this backup?" onclose={() => (deleting = null)}>
    <p class="muted">The backup from {dateTime(deleting.createdAt)} is deleted from {deleting.destination}. This can't be undone.</p>
    <div class="row end">
      <button class="btn ghost" onclick={() => (deleting = null)}>Cancel</button>
      <button class="btn danger" disabled={busy === 'delete'} onclick={removeBackup}>Delete backup</button>
    </div>
  </Modal>
{/if}

<style>
  .card {
    display: flex;
    flex-direction: column;
    gap: var(--sp-space-3);
  }
  .between {
    justify-content: space-between;
    align-items: center;
  }
  .end {
    justify-content: flex-end;
    align-items: center;
  }
  .row.tight {
    flex-wrap: nowrap;
    align-items: center;
  }
  .row.tight select {
    width: auto;
  }
  .list {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
  }
  .list li {
    display: flex;
    align-items: center;
    gap: var(--sp-space-2);
    padding: var(--sp-space-2) 0;
    border-top: 1px solid var(--sp-border);
  }
  .list li:first-child {
    border-top: 0;
  }
  .list li.col {
    flex-direction: column;
    align-items: stretch;
  }
  .line {
    display: flex;
    align-items: center;
    gap: var(--sp-space-2);
    flex-wrap: wrap;
  }
  .grow {
    flex: 1;
    min-width: 0;
  }
  .link {
    display: flex;
    flex-direction: column;
    align-items: flex-start;
    border: 0;
    background: none;
    color: var(--sp-text);
    font: inherit;
    text-align: left;
    cursor: pointer;
    padding: 0;
  }
  .link:hover span:first-child {
    text-decoration: underline;
  }
  .small {
    font-size: var(--sp-text-sm);
  }
  .log {
    margin: 0;
    padding: var(--sp-space-3);
    background: var(--sp-bg-code);
    border-radius: var(--sp-radius-md);
    max-height: 260px;
    overflow: auto;
    white-space: pre-wrap;
    font-size: var(--sp-text-xs);
  }
  .sched {
    display: flex;
    flex-direction: column;
    gap: var(--sp-space-3);
  }
  .keep {
    flex: 0 1 90px !important;
  }
  .choices {
    display: grid;
    gap: var(--sp-space-2);
  }
  .choice {
    display: grid;
    grid-template-columns: auto 1fr;
    column-gap: var(--sp-space-2);
    padding: var(--sp-space-2) var(--sp-space-3);
    border: 1px solid var(--sp-border);
    border-radius: var(--sp-radius-md);
    cursor: pointer;
  }
  .choice.active {
    border-color: var(--sp-accent);
    background: var(--sp-bg-hover);
  }
  .choice.disabled {
    opacity: 0.6;
    cursor: not-allowed;
  }
  .choice input {
    grid-row: span 2;
    margin-top: 4px;
  }
  .warn {
    padding: var(--sp-space-2) var(--sp-space-3);
    border: 1px solid var(--sp-warning);
    border-radius: var(--sp-radius-md);
    color: var(--sp-warning);
    font-size: var(--sp-text-sm);
  }
</style>
