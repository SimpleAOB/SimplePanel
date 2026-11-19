<script>
  import { onMount } from 'svelte';
  import { api } from '../../lib/api.js';
  import { ui } from '../../lib/state.svelte.js';
  import { dateTime } from '../../lib/format.js';
  import { upd, loadUpdate, checkNow, display } from '../../lib/update.svelte.js';
  import ReleaseNotes from '../ReleaseNotes.svelte';
  import Icon from '../Icon.svelte';

  let checking = $state(false);
  let err = $state('');
  let auto = $state(false);
  let hour = $state(3);
  let smsg = $state('');
  let serr = $state('');

  let s = $derived(upd.status);
  let c = $derived(s?.check);
  let rel = $derived(c?.latest);
  let job = $derived(s?.job);

  onMount(async () => {
    const st = await loadUpdate();
    if (st) {
      auto = st.settings.auto;
      hour = st.settings.hour;
      // First visit, or the last check is old: ask GitHub now.
      if (!st.check.checkedAt || Date.now() / 1000 - st.check.checkedAt > 3600) check();
    }
  });

  async function check() {
    checking = true;
    err = '';
    try {
      await checkNow();
    } catch (e) {
      err = e.message;
    } finally {
      checking = false;
    }
  }

  async function saveSettings(e) {
    e.preventDefault();
    smsg = serr = '';
    try {
      await api.put('/update/settings', { auto, hour: Number(hour) });
      upd.status.settings = { auto, hour: Number(hour) };
      smsg = auto ? 'Saved. New stable releases install by themselves.' : 'Saved. You choose when to update.';
    } catch (e2) {
      serr = e2.message;
    }
  }

  const hours = Array.from({ length: 24 }, (_, h) => h);
  const pad = (h) => String(h).padStart(2, '0') + ':00';
  const jobLabel = { succeeded: 'Updated', failed: 'Failed', rolled_back: 'Rolled back', running: 'In progress', restarting: 'Restarting' };
</script>

<section class="card" id="updates">
  <div class="row between">
    <h2>Updates</h2>
    <button class="btn small" onclick={check} disabled={checking || !s}>
      <Icon name="refresh" size={14} />{checking ? 'Checking…' : 'Check now'}
    </button>
  </div>

  {#if !s}
    <p class="muted">Loading…</p>
  {:else}
    <div class="versions">
      <div><span class="faint">Running</span><strong>{s.current}</strong></div>
      <div><span class="faint">Latest stable</span><strong>{rel ? display(rel.version) : '—'}</strong></div>
      <div><span class="faint">Channel</span><strong>Stable</strong></div>
      <div><span class="faint">Last checked</span><strong>{c.checkedAt ? dateTime(c.checkedAt) : 'Never'}</strong></div>
    </div>

    {#if c.error}<p class="error">Couldn't check for updates: {c.error}</p>{/if}
    {#if err}<p class="error">{err}</p>{/if}

    {#if c.available && rel}
      <div class="available">
        <div class="row between">
          <div>
            <h3>{rel.name || display(rel.version)}</h3>
            {#if rel.publishedAt}<small class="faint">Released {dateTime(Date.parse(rel.publishedAt) / 1000)}</small>{/if}
          </div>
          {#if s.canUpdate}
            <button class="btn primary" onclick={() => (upd.open = true)}><Icon name="upgrade" size={16} />Update to {display(rel.version)}</button>
          {/if}
        </div>
        {#if c.note}<p class="muted small">{c.note}</p>{/if}
        <div class="notes-box"><ReleaseNotes notes={rel.notes} /></div>
        <a class="small" href={rel.url} target="_blank" rel="noopener noreferrer">Release on GitHub</a>
      </div>
    {:else if c.checkedAt}
      <p class="ok"><Icon name="upgrade" size={14} /> {c.note || "You're up to date."}</p>
    {/if}

    {#if s.blocker}<p class="warn">{s.blocker}</p>{/if}

    {#if job}
      <p class="small last" class:bad={job.state === 'failed' || job.state === 'rolled_back'}>
        Last update: <strong>{jobLabel[job.state] || job.state}</strong>, {job.from} → {job.to}
        {job.finishedAt ? `on ${dateTime(job.finishedAt)}` : ''}{job.by ? ` by ${job.by}` : ''}.
        {#if job.error}<br /><span>{job.error}</span>{/if}
      </p>
    {/if}

    {#if ui.advanced}
      <form class="advanced" onsubmit={saveSettings}>
        <h3>Automatic updates</h3>
        <label class="check"><input type="checkbox" bind:checked={auto} /> Install new stable releases automatically</label>
        <div class="row">
          <label class="field narrow">
            <span>Install at</span>
            <select bind:value={hour} disabled={!auto}>
              <option value={-1}>As soon as one is out</option>
              {#each hours as h (h)}<option value={h}>{pad(h)} server time</option>{/each}
            </select>
          </label>
          <button class="btn primary">Save</button>
        </div>
        <small class="faint">
          SimplePanel checks GitHub every 6 hours. Automatic updates wait for running deployments, backups and cron jobs to finish, and a
          version that was rolled back is never retried automatically. Turn on the “SimplePanel update” events in Notifications to hear about each one.
        </small>
        {#if smsg}<p class="ok">{smsg}</p>{/if}
        {#if serr}<p class="error">{serr}</p>{/if}

        <h3>How updates are checked</h3>
        <ul class="faint small how">
          <li>Releases come from <code>github.com/SimpleAOB/SimplePanel</code>; drafts and pre-releases are skipped.</li>
          <li>The image <code>{s.image}</code> must carry the release's version and commit, and is pinned by digest.</li>
          <li>The last 3 database copies are kept in <code>/data/updates</code> on the <code>simplepanel-data</code> volume.</li>
          <li>Swarm rolls back if the new version fails its health check or crashes within 30 seconds.</li>
        </ul>
      </form>
    {:else}
      <small class="faint">Turn on Advanced view to install updates automatically.</small>
    {/if}
  {/if}
</section>

<style>
  .versions {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(120px, 1fr));
    gap: var(--sp-space-3);
  }
  .versions div {
    display: flex;
    flex-direction: column;
    gap: 2px;
    font-size: var(--sp-text-sm);
  }
  .available {
    display: flex;
    flex-direction: column;
    gap: var(--sp-space-2);
    padding: var(--sp-space-3);
    border: 1px solid color-mix(in srgb, var(--sp-accent) 40%, var(--sp-border));
    border-radius: var(--sp-radius-md);
  }
  .available h3 {
    margin: 0;
  }
  .available .btn :global(svg),
  .row .btn :global(svg) {
    margin-right: 6px;
  }
  .notes-box {
    max-height: 260px;
    overflow: auto;
    padding: var(--sp-space-3);
    border-radius: var(--sp-radius-md);
    background: var(--sp-bg-code);
  }
  .small {
    font-size: var(--sp-text-sm);
  }
  .ok {
    display: flex;
    align-items: center;
    gap: var(--sp-space-2);
  }
  .warn {
    padding: var(--sp-space-2) var(--sp-space-3);
    border: 1px solid var(--sp-warning);
    border-radius: var(--sp-radius-md);
    font-size: var(--sp-text-sm);
  }
  .last {
    margin: 0;
    color: var(--sp-text-muted);
  }
  .last.bad strong,
  .last.bad span {
    color: var(--sp-danger);
  }
  .advanced {
    display: flex;
    flex-direction: column;
    gap: var(--sp-space-3);
    border-top: 1px solid var(--sp-border);
    padding-top: var(--sp-space-3);
  }
  .advanced h3 {
    margin: 0;
  }
  .narrow {
    max-width: 280px;
  }
  .how {
    margin: 0;
    padding-left: var(--sp-space-5);
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
</style>
