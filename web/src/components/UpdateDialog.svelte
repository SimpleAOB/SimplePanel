<script>
  import Modal from './Modal.svelte';
  import Icon from './Icon.svelte';
  import ReleaseNotes from './ReleaseNotes.svelte';
  import { upd, startUpdate, display } from '../lib/update.svelte.js';

  let busy = $state(false);
  let err = $state('');
  let jobsRunning = $state(false);

  let s = $derived(upd.status);
  let rel = $derived(s?.check?.latest);
  let p = $derived(upd.progress);
  let active = $derived(p && (p.phase === 'working' || p.phase === 'restarting'));

  async function go(force) {
    busy = true;
    err = '';
    try {
      await startUpdate(force);
    } catch (e) {
      err = e.message;
      jobsRunning = !!e.jobsRunning;
    } finally {
      busy = false;
    }
  }

  function close() {
    if (active) return; // keep following the restart
    upd.open = false;
    if (p && p.phase !== 'done') upd.progress = null;
  }

  const steps = ['Downloading', 'Verifying the image', 'Saving a copy of the database', 'Restarting SimplePanel'];
  let stepIndex = $derived.by(() => {
    if (!p) return -1;
    if (p.phase === 'restarting') return 4;
    if (p.phase === 'done') return 5;
    const i = steps.findIndex((x) => p.step?.startsWith(x));
    return i < 0 ? 0 : i;
  });
</script>

<Modal title={p ? `Updating to ${p.target}` : `Update to ${display(rel?.version)}`} onclose={close}>
  {#if !p}
    <p>
      SimplePanel will restart to install <strong>{display(rel.version)}</strong> (you're on {s.current}). The panel is away for about half a minute;
      your apps, databases and sites keep running.
    </p>
    <ul class="facts">
      <li><Icon name="lock" size={16} /><span>Only the official image from <code>{s.image}</code>, checked against the {rel.tag} release and pinned by digest.</span></li>
      <li><Icon name="db" size={16} /><span>A copy of the panel's database is saved first.</span></li>
      <li><Icon name="refresh" size={16} /><span>If the new version doesn't pass its health check, Swarm puts {s.current} back automatically.</span></li>
    </ul>
    <details open>
      <summary>What's new in {display(rel.version)}</summary>
      <div class="notes-box"><ReleaseNotes notes={rel.notes} /></div>
      <a class="small" href={rel.url} target="_blank" rel="noopener noreferrer">Full release notes on GitHub</a>
    </details>
    {#if err}
      <p class="error">{err}{jobsRunning ? '. Update anyway, or wait until they finish.' : ''}</p>
    {/if}
    <div class="actions">
      <button class="btn ghost" onclick={close}>Not now</button>
      {#if jobsRunning}
        <button class="btn danger" disabled={busy} onclick={() => go(true)}>Update anyway</button>
      {:else}
        <button class="btn primary" disabled={busy} onclick={() => go(false)}>{busy ? 'Starting…' : `Update to ${display(rel.version)}`}</button>
      {/if}
    </div>
  {:else}
    <ol class="steps">
      {#each [...steps, 'Waiting for the new version'] as label, i (label)}
        <li class:done={i < stepIndex} class:now={i === stepIndex && p.phase !== 'failed'}>
          <span class="dot"></span>{label === 'Downloading' ? `Downloading ${p.target}` : label}
        </li>
      {/each}
    </ol>
    {#if p.phase === 'restarting'}
      <p class="muted">SimplePanel is restarting. This page reconnects by itself.</p>
    {:else if p.phase === 'done'}
      <p class="ok">Updated to {p.target}. Reloading…</p>
    {:else if p.phase === 'slow'}
      <p class="muted">This is taking longer than usual. Check <code>docker service ps simplepanel</code> on the server, or keep this page open.</p>
    {:else if p.phase === 'failed'}
      <p class="error">
        {p.rolledBack ? `The new version didn't start, so Swarm put ${s.current} back: ` : 'The update failed: '}{p.error}
      </p>
      <p class="faint small">Nothing else changed. The details are in the activity log under Settings › Users.</p>
      <div class="actions"><button class="btn" onclick={close}>Close</button></div>
    {/if}
  {/if}
</Modal>

<style>
  .actions {
    display: flex;
    justify-content: flex-end;
    gap: var(--sp-space-2);
  }
  .ok {
    color: var(--sp-success);
  }
  .facts {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: var(--sp-space-2);
    font-size: var(--sp-text-sm);
    color: var(--sp-text-muted);
  }
  .facts li {
    display: flex;
    gap: var(--sp-space-2);
    align-items: flex-start;
  }
  .facts :global(svg) {
    flex: none;
    margin-top: 2px;
    color: var(--sp-success);
  }
  summary {
    cursor: pointer;
    font-weight: var(--sp-weight-medium);
    margin-bottom: var(--sp-space-2);
  }
  .notes-box {
    max-height: 240px;
    overflow: auto;
    padding: var(--sp-space-3);
    border: 1px solid var(--sp-border);
    border-radius: var(--sp-radius-md);
    background: var(--sp-bg-code);
    margin-bottom: var(--sp-space-2);
  }
  .small {
    font-size: var(--sp-text-sm);
  }
  .steps {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: var(--sp-space-2);
    color: var(--sp-text-faint);
  }
  .steps li {
    display: flex;
    align-items: center;
    gap: var(--sp-space-2);
  }
  .dot {
    width: 10px;
    height: 10px;
    border-radius: 50%;
    border: 2px solid currentColor;
    flex: none;
  }
  .steps li.done {
    color: var(--sp-text-muted);
  }
  .steps li.done .dot {
    background: var(--sp-success);
    border-color: var(--sp-success);
  }
  .steps li.now {
    color: var(--sp-text);
  }
  .steps li.now .dot {
    border-color: var(--sp-accent);
    animation: pulse 1.2s ease-in-out infinite;
  }
  @keyframes pulse {
    50% {
      background: var(--sp-accent);
    }
  }
</style>
