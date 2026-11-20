<script>
  let { status } = $props();
  const labels = {
    running: 'Running',
    starting: 'Starting',
    stopped: 'Stopped',
    failing: 'Failing',
    not_deployed: 'Not deployed',
    deploy_failed: 'Deploy failed',
    deploying: 'Deploying…',
    unknown: 'Unknown',
  };
  let state = $derived(status?.state || 'unknown');
</script>

<span class="badge" title={status?.message || ''}>
  <span class="dot {state === 'deploy_failed' ? 'failed' : state}"></span>
  {labels[state] || state}
  {#if status && status.desired > 0}<span class="faint">{status.running}/{status.desired}</span>{/if}
</span>

<style>
  .badge {
    display: inline-flex;
    align-items: center;
    gap: var(--sp-space-1);
    padding: 2px var(--sp-space-2);
    border-radius: var(--sp-radius-round);
    background: var(--sp-bg-hover);
    font-size: var(--sp-text-xs);
    font-weight: var(--sp-weight-medium);
    white-space: nowrap;
    flex: none;
  }
</style>
