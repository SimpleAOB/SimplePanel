<script>
  // Memory limit for a service, sized to this server. `value` is the
  // app's memoryMb: 0 = automatic, -1 = no limit, otherwise MB.
  // The bar shows what other services are allowed, this one, and what's
  // left of the memory set aside for services.
  import { onMount } from 'svelte';
  import { ui } from '../lib/state.svelte.js';
  import { fmtMB, loadCapacity } from '../lib/memory.svelte.js';

  let { value = $bindable(0), kind = 'app', appId = 0, replicas = 1 } = $props();

  let info = $state(null);
  onMount(() => {
    loadCapacity(appId)
      .then((r) => (info = r))
      .catch(() => (info = { known: false }));
  });

  let cap = $derived(info?.known ? info.capacity : null);
  let avail = $derived(cap?.availableMb || 0);
  let auto = $derived(cap ? cap.defaults[kind] ?? cap.defaults.app : 0);
  const steps = [64, 128, 192, 256, 384, 512, 768, 1024, 1536, 2048, 3072, 4096, 6144, 8192, 12288, 16384, 24576, 32768, 49152, 65536];
  let presets = $derived(steps.filter((mb) => mb >= 128 && mb <= avail && mb !== auto));

  // The <select>'s value: 'auto', 'none', 'custom' or a number of MB.
  let choice = $state('auto');
  let custom = $state(0);
  // Pick the matching option once the presets are known.
  let synced = false;
  $effect(() => {
    if (!cap || synced) return;
    synced = true;
    const v = Number(value) || 0;
    if (v === 0) choice = 'auto';
    else if (v < 0) choice = 'none';
    else if (presets.includes(v) || v === auto) choice = String(v);
    else {
      choice = 'custom';
      custom = v;
    }
  });
  function pick(c) {
    choice = c;
    if (c === 'auto') value = 0;
    else if (c === 'none') value = -1;
    else if (c === 'custom') {
      custom = custom || Math.max(Number(value) || 0, auto);
      value = custom;
    } else value = Number(c);
  }

  let n = $derived(Math.max(Number(replicas) || 1, 1));
  let mine = $derived(value > 0 ? Number(value) : value === 0 && cap?.autoLimits ? auto : 0);
  let mineTotal = $derived(mine * n);
  let others = $derived(info?.othersCommittedMb || 0);
  let total = $derived(others + mineTotal);
  let scale = $derived(Math.max(avail, total, 1));
  // Limits are ceilings, not reservations: adding up to a bit more than
  // there is is normal. Only a big overshoot gets the warning colour.
  let over = $derived(cap && total > avail);
  let wayOver = $derived(cap && total > avail * 1.5);
  let tooBig = $derived(cap && mine > avail);
  const pct = (mb) => `${Math.min(100, (mb / scale) * 100)}%`;
</script>

{#if info && !info.known}
  <label class="field">
    <span>Memory limit (MB)</span>
    <input type="number" min="-1" step="64" bind:value placeholder="0 = automatic" />
  </label>
{:else if cap}
  <div class="field mem">
    <label class="label" for="mem-{appId}-{kind}">Memory</label>
    <div class="row pick">
      <select id="mem-{appId}-{kind}" value={choice} onchange={(e) => pick(e.currentTarget.value)}>
        <option value="auto">Automatic: {fmtMB(auto)} (recommended)</option>
        {#each presets as mb (mb)}
          <option value={String(mb)}>{fmtMB(mb)}</option>
        {/each}
        {#if choice === 'custom' || ui.advanced}<option value="custom">Custom…</option>{/if}
        {#if choice === 'none' || ui.advanced}<option value="none">No limit</option>{/if}
      </select>
      {#if choice === 'custom'}
        <div class="unit">
          <input type="number" min="16" step="32" bind:value={custom} oninput={() => (value = Number(custom) || 0)} aria-label="Custom memory limit in MB" />
          <span class="faint">MB</span>
        </div>
      {/if}
    </div>

    <div class="bar" role="img" aria-label="{fmtMB(others)} for other services, {fmtMB(mineTotal)} for this one, of {fmtMB(avail)}">
      <span class="seg others" style:width={pct(others)}></span>
      <span class="seg mine" class:over={wayOver} style:width={pct(mineTotal)}></span>
      {#if total > avail}<span class="limit" style:left={pct(avail)}></span>{/if}
    </div>
    <div class="legend">
      <span><i class="sw others"></i>Other services {fmtMB(others)}</span>
      <span><i class="sw mine" class:over={wayOver}></i>This one {mine ? fmtMB(mineTotal) : 'no limit'}{n > 1 && mine ? ` (${n} × ${fmtMB(mine)})` : ''}</span>
      <span class="faint">{fmtMB(avail)} for services</span>
    </div>

    <small>
      {#if choice === 'none'}
        No limit: this service can take all {fmtMB(avail)} meant for services and slow everything else down. Best kept for big servers.
      {:else if tooBig}
        That's more than the {fmtMB(avail)} this server has for services. It won't get more than that.
      {:else if wayOver}
        Together, services may now use {fmtMB(total)}, much more than the {fmtMB(avail)} there is. If several get busy at once, the biggest is restarted. Consider smaller limits or a bigger server.
      {:else if over}
        Limits add up to {fmtMB(total)}, a little more than the {fmtMB(avail)} there is. That's normal: services rarely all use their full limit at once, and if memory does run out the biggest one is restarted, not the server.
      {:else}
        This server has {fmtMB(cap.memTotalMb)} of memory. {fmtMB(cap.memTotalMb - avail)} is kept for the system and SimplePanel, and services share the other {fmtMB(avail)}. If this one goes over its limit, it's restarted.
      {/if}
    </small>
  </div>
{/if}

<style>
  .mem {
    gap: var(--sp-space-2);
  }
  .label {
    font-size: var(--sp-text-sm);
    font-weight: var(--sp-weight-medium);
    color: var(--sp-text-muted);
  }
  .pick {
    flex-wrap: wrap;
    align-items: center;
  }
  .pick select {
    flex: 1 1 220px;
    max-width: 340px;
  }
  .unit {
    display: flex;
    align-items: center;
    gap: var(--sp-space-2);
  }
  .unit input {
    width: 110px;
  }
  .bar {
    position: relative;
    display: flex;
    height: 8px;
    border-radius: var(--sp-radius-round);
    background: var(--sp-bg-input);
    border: 1px solid var(--sp-border);
    overflow: hidden;
  }
  .seg {
    height: 100%;
  }
  .others,
  .sw.others {
    background: var(--sp-text-faint);
  }
  .mine,
  .sw.mine {
    background: var(--sp-accent);
  }
  .mine.over,
  .sw.mine.over {
    background: var(--sp-warning);
  }
  .limit {
    position: absolute;
    top: -1px;
    bottom: -1px;
    width: 2px;
    background: var(--sp-text);
  }
  .legend {
    display: flex;
    flex-wrap: wrap;
    gap: var(--sp-space-1) var(--sp-space-4);
    font-size: var(--sp-text-xs);
    color: var(--sp-text-muted);
  }
  .legend span {
    display: inline-flex;
    align-items: center;
    gap: var(--sp-space-1);
  }
  .sw {
    width: 10px;
    height: 10px;
    border-radius: 2px;
    display: inline-block;
  }
</style>
