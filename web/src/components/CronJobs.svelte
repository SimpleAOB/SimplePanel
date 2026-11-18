<script>
  // Cron jobs tab: scheduled commands that run inside the service's
  // container, with their recent runs and output.
  import { onMount } from 'svelte';
  import { api } from '../lib/api.js';
  import { ui, timeAgo } from '../lib/state.svelte.js';
  import { describeSchedule, schedulePresets } from '../lib/cron.js';
  import Icon from './Icon.svelte';

  let { aid, canDeploy, appName, isDb = false } = $props();

  let jobs = $state([]);
  let loaded = $state(false);
  let err = $state('');
  let editing = $state(null); // form object or null
  let selected = $state(0); // job id whose runs are shown
  let runs = $state([]);
  let run = $state(null); // run with output
  let busy = $state('');
  let timezone = $state('');

  const blank = () => ({ id: 0, name: '', schedule: '0 3 * * *', command: '', timeout: 3600, enabled: true, preset: '0 3 * * *' });

  async function load() {
    try {
      jobs = await api.get(`/apps/${aid}/cron`);
      if (selected) await loadRuns(false);
    } catch (e) {
      err = e.message;
    } finally {
      loaded = true;
    }
  }

  async function loadRuns(pickLatest = true) {
    runs = await api.get(`/cron/${selected}/runs`);
    if (run && runs.some((r) => r.id === run.id)) {
      run = await api.get(`/cron-runs/${run.id}`);
    } else if (pickLatest && runs.length) {
      run = await api.get(`/cron-runs/${runs[0].id}`);
    }
  }

  onMount(() => {
    load();
    api.get('/system').then((s) => (timezone = s.timezone || '')).catch(() => {});
    const t = setInterval(load, 4000);
    return () => clearInterval(t);
  });

  function edit(j) {
    const preset = schedulePresets.find((p) => p[0] === j.schedule) ? j.schedule : 'custom';
    editing = { ...j, preset };
  }

  async function save(e) {
    e.preventDefault();
    err = '';
    busy = 'save';
    const f = editing;
    const body = { name: f.name, schedule: f.preset === 'custom' ? f.schedule : f.preset, command: f.command, timeout: Number(f.timeout) || 3600, enabled: f.enabled };
    try {
      if (f.id) await api.put(`/cron/${f.id}`, body);
      else {
        const j = await api.post(`/apps/${aid}/cron`, body);
        selected = j.id;
      }
      editing = null;
      await load();
    } catch (e2) {
      err = e2.message;
    } finally {
      busy = '';
    }
  }

  async function toggle(j) {
    try {
      await api.put(`/cron/${j.id}`, { name: j.name, schedule: j.schedule, command: j.command, timeout: j.timeout, enabled: !j.enabled });
      await load();
    } catch (e) {
      err = e.message;
    }
  }

  async function remove(j) {
    try {
      await api.del(`/cron/${j.id}`);
      if (selected === j.id) {
        selected = 0;
        run = null;
      }
      editing = null;
      await load();
    } catch (e) {
      err = e.message;
    }
  }

  async function runNow(j) {
    busy = 'run' + j.id;
    err = '';
    try {
      const r = await api.post(`/cron/${j.id}/run`);
      selected = j.id;
      run = r;
      await load();
      await loadRuns(true);
    } catch (e) {
      err = e.message;
    } finally {
      busy = '';
    }
  }

  async function select(j) {
    selected = j.id;
    run = null;
    await loadRuns(true);
  }

  async function showRun(r) {
    run = await api.get(`/cron-runs/${r.id}`);
  }

  function nextLabel(j) {
    if (!j.enabled) return 'Paused';
    if (!j.nextRunAt) return '—';
    const d = new Date(j.nextRunAt * 1000);
    const s = Math.round((j.nextRunAt * 1000 - Date.now()) / 60000);
    if (s < 60 && s >= 0) return `in ${s} min`;
    return d.toLocaleString(undefined, { weekday: 'short', hour: '2-digit', minute: '2-digit' });
  }
</script>

<section class="card">
  <div class="row between">
    <h3>Cron jobs</h3>
    {#if canDeploy && !editing}
      <button class="btn small primary" onclick={() => (editing = blank())}><Icon name="plus" size={14} /> Add cron job</button>
    {/if}
  </div>
  <p class="muted">
    Run a command on a schedule inside a running container of {appName}, like <code>docker exec</code>. It sees the same files and
    environment variables as the {isDb ? 'database' : 'app'}. Times are in the server's time zone{timezone ? ` (${timezone})` : ''}.
  </p>
  {#if err}<p class="error">{err}</p>{/if}

  {#if editing}
    <form class="editor" onsubmit={save}>
      <div class="row">
        <label class="field">
          <span>Name</span>
          <input type="text" bind:value={editing.name} placeholder="e.g. Send digest emails" maxlength="60" required />
        </label>
        <label class="field">
          <span>Schedule</span>
          <select bind:value={editing.preset}>
            {#each schedulePresets as [expr, label]}<option value={expr}>{label}</option>{/each}
            <option value="custom">Custom (cron expression)</option>
          </select>
        </label>
      </div>
      {#if editing.preset === 'custom'}
        <label class="field">
          <span>Cron expression</span>
          <input type="text" class="mono" bind:value={editing.schedule} placeholder="*/15 * * * *" required />
          <small>minute hour day month weekday · {describeSchedule(editing.schedule)}</small>
        </label>
      {/if}
      <label class="field">
        <span>Command</span>
        <textarea class="mono" rows="2" bind:value={editing.command} placeholder={isDb ? 'psql -U "$POSTGRES_USER" -c "VACUUM ANALYZE"' : 'php artisan schedule:run'} required spellcheck="false"></textarea>
        <small>Runs with <code>sh -c</code>, so pipes, <code>&&</code> and <code>$VARIABLES</code> work.</small>
      </label>
      {#if ui.advanced}
        <div class="row">
          <label class="field">
            <span>Timeout (seconds)</span>
            <input type="number" min="1" max="86400" bind:value={editing.timeout} />
            <small>The command is stopped after this long. Up to 24 hours.</small>
          </label>
          <label class="check"><input type="checkbox" bind:checked={editing.enabled} /> Enabled</label>
        </div>
      {/if}
      <div class="row end">
        {#if editing.id}<button type="button" class="btn ghost danger-text" onclick={() => remove(editing)}>Delete</button>{/if}
        <span class="grow"></span>
        <button type="button" class="btn ghost" onclick={() => (editing = null)}>Cancel</button>
        <button class="btn primary" disabled={busy === 'save'}>{editing.id ? 'Save' : 'Add job'}</button>
      </div>
    </form>
  {/if}

  {#if loaded && jobs.length === 0 && !editing}
    <p class="faint">No cron jobs yet.</p>
  {/if}

  {#if jobs.length}
    <ul class="jobs">
      {#each jobs as j (j.id)}
        <li class:sel={selected === j.id}>
          <button class="main" onclick={() => select(j)}>
            <span class="dot {j.lastStatus}" title={j.lastStatus || 'never run'}></span>
            <span class="what">
              <span class="name">{j.name}{#if !j.enabled} <span class="tag">paused</span>{/if}</span>
              <code class="cmd">{j.command}</code>
            </span>
            <span class="when">
              <span>{describeSchedule(j.schedule)}</span>
              <span class="faint small">Next: {nextLabel(j)}{j.lastRunAt ? ` · last ${timeAgo(j.lastRunAt)}` : ''}</span>
            </span>
          </button>
          {#if canDeploy}
            <div class="actions">
              <button class="btn small" disabled={busy === 'run' + j.id || j.lastStatus === 'running'} onclick={() => runNow(j)}><Icon name="play" size={12} /> Run now</button>
              <button class="btn ghost small" onclick={() => toggle(j)}>{j.enabled ? 'Pause' : 'Resume'}</button>
              <button class="btn ghost small" onclick={() => edit(j)}>Edit</button>
            </div>
          {/if}
        </li>
      {/each}
    </ul>
  {/if}
</section>

{#if selected && jobs.find((j) => j.id === selected)}
  <section class="card">
    <h3>Recent runs · {jobs.find((j) => j.id === selected).name}</h3>
    {#if runs.length === 0}
      <p class="faint">Not run yet.</p>
    {:else}
      <div class="runs">
        <ul>
          {#each runs as r (r.id)}
            <li>
              <button class:active={run?.id === r.id} onclick={() => showRun(r)}>
                <span class="dot {r.status}"></span>
                <span class="grow">{r.status}{r.exitCode != null && r.exitCode !== 0 ? ` (exit ${r.exitCode})` : ''}</span>
                <span class="faint small">{r.trigger === 'schedule' ? '' : r.trigger + ' · '}{timeAgo(r.createdAt)}</span>
              </button>
            </li>
          {/each}
        </ul>
        <pre class="output">{run ? run.output || (run.status === 'running' ? 'Waiting for output…' : '(no output)') : 'Pick a run to see its output.'}</pre>
      </div>
    {/if}
  </section>
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
  .editor {
    display: flex;
    flex-direction: column;
    gap: var(--sp-space-3);
    padding: var(--sp-space-3);
    border: 1px solid var(--sp-accent);
    border-radius: var(--sp-radius-md);
    background: var(--sp-bg-main);
  }
  .end {
    align-items: center;
  }
  .grow {
    flex: 1;
  }
  .check {
    display: flex;
    gap: var(--sp-space-2);
    align-items: center;
    font-size: var(--sp-text-sm);
  }
  .danger-text {
    color: var(--sp-danger);
  }
  .jobs {
    list-style: none;
    margin: 0;
    padding: 0;
    border: 1px solid var(--sp-border);
    border-radius: var(--sp-radius-md);
  }
  .jobs li {
    display: flex;
    align-items: center;
    gap: var(--sp-space-2);
    padding-right: var(--sp-space-3);
    flex-wrap: wrap;
  }
  .jobs li + li {
    border-top: 1px solid var(--sp-border);
  }
  .jobs li.sel {
    background: var(--sp-bg-active);
  }
  .main {
    flex: 1 1 320px;
    min-width: 0;
    display: flex;
    align-items: center;
    gap: var(--sp-space-3);
    padding: var(--sp-space-3);
    border: 0;
    background: none;
    color: var(--sp-text);
    font: inherit;
    text-align: left;
    cursor: pointer;
  }
  .what {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  .name {
    font-weight: var(--sp-weight-bold);
  }
  .cmd {
    font-size: var(--sp-text-xs);
    color: var(--sp-text-muted);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .when {
    display: flex;
    flex-direction: column;
    align-items: flex-end;
    gap: 2px;
    font-size: var(--sp-text-sm);
    text-align: right;
  }
  .actions {
    display: flex;
    gap: var(--sp-space-1);
    padding-bottom: var(--sp-space-2);
    margin-left: auto;
  }
  @media (min-width: 900px) {
    .actions {
      padding-bottom: 0;
    }
  }
  .tag {
    font-size: var(--sp-text-xs);
    font-weight: normal;
    padding: 1px var(--sp-space-2);
    border-radius: var(--sp-radius-round);
    border: 1px solid var(--sp-border);
    color: var(--sp-text-muted);
  }
  .small {
    font-size: var(--sp-text-xs);
  }
  .runs {
    display: grid;
    grid-template-columns: minmax(180px, 240px) minmax(0, 1fr);
    gap: var(--sp-space-3);
  }
  .runs ul {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 2px;
    max-height: 320px;
    overflow-y: auto;
  }
  .runs li button {
    width: 100%;
    display: flex;
    align-items: center;
    gap: var(--sp-space-2);
    padding: var(--sp-space-2);
    border: 0;
    border-radius: var(--sp-radius-sm);
    background: none;
    color: var(--sp-text);
    font: inherit;
    font-size: var(--sp-text-sm);
    cursor: pointer;
    text-align: left;
  }
  .runs li button:hover,
  .runs li button.active {
    background: var(--sp-bg-hover);
  }
  .output {
    margin: 0;
    min-height: 120px;
    max-height: 320px;
    overflow: auto;
    padding: var(--sp-space-3);
    border-radius: var(--sp-radius-md);
    background: var(--sp-bg-main);
    border: 1px solid var(--sp-border);
    font-family: var(--sp-font-mono);
    font-size: var(--sp-text-xs);
    white-space: pre-wrap;
    overflow-wrap: anywhere;
  }
  @media (max-width: 640px) {
    .runs {
      grid-template-columns: 1fr;
    }
    .when {
      align-items: flex-start;
      text-align: left;
    }
  }
</style>
