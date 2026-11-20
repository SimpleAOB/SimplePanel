<script>
  // Shown while Docker isn't answering the panel. Apps usually keep
  // running; only the panel's view of them is stale.
  import Icon from './Icon.svelte';
  import { data, timeAgo } from '../lib/state.svelte.js';

  let d = $derived(data.docker);
  let since = $derived(d?.since ? Math.floor(new Date(d.since).getTime() / 1000) : 0);
</script>

{#if d && !d.ok}
  <div class="banner" role="alert">
    <Icon name="box" size={18} />
    <span class="text">
      <strong>Docker is not responding</strong>{#if since}{' '}(since {timeAgo(since)}){/if}. Your apps may still be running,
      but SimplePanel can't see or change them right now. It keeps retrying and reconnects on its own.
      This usually means the server ran out of memory.
    </span>
    <a class="btn small ghost" href="#/settings/monitoring">Server memory</a>
  </div>
{/if}

<style>
  .banner {
    display: flex;
    align-items: center;
    gap: var(--sp-space-3);
    padding: var(--sp-space-2) var(--sp-space-4);
    background: color-mix(in srgb, var(--sp-danger) 14%, var(--sp-bg-main));
    border-bottom: 1px solid color-mix(in srgb, var(--sp-danger) 40%, var(--sp-border));
    font-size: var(--sp-text-sm);
    flex: none;
    flex-wrap: wrap;
  }
  .banner > :global(svg) {
    color: var(--sp-danger);
    flex: none;
  }
  .text {
    flex: 1 1 260px;
    min-width: 0;
  }
</style>
