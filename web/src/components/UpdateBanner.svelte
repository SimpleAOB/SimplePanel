<script>
  // "Update available" strip above the main column, for owners and admins.
  import Icon from './Icon.svelte';
  import { upd, isDismissed, dismiss, display } from '../lib/update.svelte.js';

  let hidden = $state(false);
  let s = $derived(upd.status);
  let v = $derived(s?.check?.latest?.version);
  let job = $derived(s?.job);
  // A version that just failed or was rolled back isn't advertised again;
  // Settings still offers it.
  let failed = $derived(job && job.to === display(v) && (job.state === 'failed' || job.state === 'rolled_back'));
  let show = $derived(
    !!s && !hidden && (s.canUpdate ? !isDismissed(v) && !failed : job?.state === 'running' || job?.state === 'restarting'),
  );
</script>

{#if show}
  <div class="banner" role="status">
    <Icon name="upgrade" size={18} />
    {#if job?.state === 'running' || job?.state === 'restarting'}
      <span class="text">Updating SimplePanel to <strong>{job.to}</strong> · {job.step}…</span>
      <button class="btn small" onclick={() => (upd.open = true)}>Details</button>
    {:else}
      <span class="text">
        <strong>SimplePanel {display(v)}</strong> is available. You're on {s.current}.
      </span>
      <a class="btn small ghost" href="#/settings/general">What's new</a>
      <button class="btn small primary" onclick={() => (upd.open = true)}>Update now</button>
      <button
        class="btn ghost icon close"
        aria-label="Hide until the next release"
        title="Hide until the next release"
        onclick={() => {
          dismiss(v);
          hidden = true;
        }}><Icon name="x" size={16} /></button>
    {/if}
  </div>
{/if}

<style>
  .banner {
    display: flex;
    align-items: center;
    gap: var(--sp-space-3);
    padding: var(--sp-space-2) var(--sp-space-4);
    background: color-mix(in srgb, var(--sp-accent) 14%, var(--sp-bg-main));
    border-bottom: 1px solid color-mix(in srgb, var(--sp-accent) 35%, var(--sp-border));
    font-size: var(--sp-text-sm);
    flex: none;
    flex-wrap: wrap;
  }
  .banner > :global(svg) {
    color: var(--sp-accent);
    flex: none;
  }
  .text {
    flex: 1 1 200px;
    min-width: 0;
  }
  .close {
    width: 28px;
    height: 28px;
  }
</style>
