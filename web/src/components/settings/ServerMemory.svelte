<script>
  // How this server's memory is shared out: live use, what services are
  // allowed, and whether the server-wide cap protects SSH and Docker.
  import { onMount } from 'svelte';
  import { api } from '../../lib/api.js';
  import { ui, isAdmin } from '../../lib/state.svelte.js';
  import { fmtMB, loadCapacity } from '../../lib/memory.svelte.js';

  let info = $state(null);
  let err = $state('');
  let reserve = $state(0);
  let autoLimits = $state(true);
  let msg = $state('');
  let saveErr = $state('');

  async function load() {
    try {
      info = await loadCapacity();
      if (info.known) {
        reserve = info.settings.reserveMb || 0;
        autoLimits = !info.settings.noAutoLimits;
      }
    } catch (e) {
      err = e.message;
    }
  }
  onMount(() => {
    load();
    const t = setInterval(load, 30000);
    return () => clearInterval(t);
  });

  async function save(e) {
    e.preventDefault();
    msg = saveErr = '';
    try {
      await api.put('/capacity/settings', { reserveMb: Number(reserve) || 0, noAutoLimits: !autoLimits });
      msg = 'Saved. Services pick up new limits the next time they deploy.';
      await load();
    } catch (e2) {
      saveErr = e2.message;
    }
  }

  let cap = $derived(info?.known ? info.capacity : null);
  let usedPct = $derived(cap ? Math.round((info.usedMb / cap.memTotalMb) * 100) : 0);
  let allowedPct = $derived(cap ? Math.round((info.committedMb / cap.availableMb) * 100) : 0);
  let level = (p, warn, bad) => (p >= bad ? 'bad' : p >= warn ? 'warn' : 'good');
  let shown = $state(6);
</script>

{#if err}<p class="error">{err}</p>{/if}
{#if cap}
  <section class="card mem">
    <div class="head">
      <h3>Memory</h3>
      <span class="faint">{fmtMB(cap.memTotalMb)} on this server{info.swapMb ? ` · ${fmtMB(info.swapMb)} swap` : ' · no swap'}</span>
    </div>

    <div class="meter">
      <div class="label"><span>In use now</span><span class="num {level(usedPct, 80, info.pressurePct)}">{fmtMB(info.usedMb)} · {usedPct}%</span></div>
      <div class="bar"><span class="fill {level(usedPct, 80, info.pressurePct)}" style:width="{Math.min(usedPct, 100)}%"></span></div>
      {#if usedPct >= info.pressurePct}
        <small class="bad-text">The server is nearly out of memory. New deploys wait for an owner or admin to confirm.</small>
      {:else if info.swapUsedMb > 256}
        <small class="faint">{fmtMB(info.swapUsedMb)} of swap in use: the server is short of memory at times.</small>
      {/if}
    </div>

    <div class="meter">
      <div class="label">
        <span>Allowed for services</span>
        <span class="num {level(allowedPct, 150, 250)}">{fmtMB(info.committedMb)} of {fmtMB(cap.availableMb)}</span>
      </div>
      <div class="bar"><span class="fill {level(allowedPct, 150, 250)}" style:width="{Math.min(allowedPct, 100)}%"></span></div>
      <small class="faint">
        {fmtMB(cap.reserveMb)} is kept for the system (Linux, SSH, Docker) and {fmtMB(cap.panelMb)} for SimplePanel itself. Each service has a memory limit; if it goes
        over, only that service restarts.
        {#if allowedPct > 100}
          Limits add up to more than there is, which is fine while services aren't all busy at once.
        {/if}
        {#if info.unlimited}{info.unlimited} service{info.unlimited === 1 ? ' has' : 's have'} no limit.{/if}
      </small>
    </div>

    {#if info.services.length}
      <ul class="services">
        {#each info.services.slice(0, shown) as s (s.id)}
          <li>
            <a href="#/p/{s.projectId}/a/{s.id}/settings">{s.project}/{s.name}</a>
            <span class="faint">
              {#if !s.replicas}stopped · {/if}{s.limitMb ? fmtMB(s.limitMb) : 'no limit'}{s.replicas > 1 && s.limitMb ? ` × ${s.replicas}` : ''}{s.auto ? ' (automatic)' : ''}
            </span>
          </li>
        {/each}
      </ul>
      {#if info.services.length > shown}
        <button class="linkish" onclick={() => (shown = info.services.length)}>Show all {info.services.length}</button>
      {/if}
    {/if}

    {#if info.hostCapMb > 0}
      <p class="status good-text">
        Protected: all containers together can use at most {fmtMB(info.hostCapMb)}, so SSH and Docker keep working even if
        apps run out of memory.
      </p>
    {:else}
      <div class="status warn-box">
        <strong>Server-wide memory cap is off.</strong>
        If apps use up all the memory, the server can stop responding (even to SSH). To turn the cap on, run the installer
        again on the server. It restarts Docker and your apps once (about a minute):
        <code class="cmd">curl -fsSL https://raw.githubusercontent.com/SimpleAOB/SimplePanel/main/install.sh | sudo SP_PROTECT_HOST=1 sh</code>
      </div>
    {/if}

    {#if ui.advanced && isAdmin()}
      <form class="adv" onsubmit={save}>
        <div class="row">
          <label class="field">
            <span>Keep for the system</span>
            <div class="unit"><input type="number" min="0" step="64" bind:value={reserve} /><span class="faint">MB</span></div>
            <small>0 = automatic ({fmtMB(cap.autoReserve ? cap.reserveMb : Math.min(Math.max(Math.floor(cap.memTotalMb * 0.15), 384), 2048))}).
              Sizes the automatic limits; the server-wide cap is set by the installer.</small>
          </label>
          <label class="check">
            <input type="checkbox" bind:checked={autoLimits} />
            Give services without their own limit an automatic one
          </label>
        </div>
        <div class="row end">
          {#if msg}<span class="ok">{msg}</span>{/if}
          {#if saveErr}<span class="error">{saveErr}</span>{/if}
          <button class="btn primary">Save</button>
        </div>
      </form>
    {/if}
  </section>
{/if}

<style>
  .mem {
    display: flex;
    flex-direction: column;
    gap: var(--sp-space-4);
  }
  .head {
    display: flex;
    justify-content: space-between;
    align-items: baseline;
    gap: var(--sp-space-3);
    flex-wrap: wrap;
  }
  .meter {
    display: flex;
    flex-direction: column;
    gap: var(--sp-space-1);
  }
  .label {
    display: flex;
    justify-content: space-between;
    gap: var(--sp-space-3);
    font-size: var(--sp-text-sm);
  }
  .num {
    font-variant-numeric: tabular-nums;
    font-weight: var(--sp-weight-medium);
  }
  .bar {
    height: 8px;
    border-radius: var(--sp-radius-round);
    background: var(--sp-bg-input);
    border: 1px solid var(--sp-border);
    overflow: hidden;
  }
  .fill {
    display: block;
    height: 100%;
  }
  .fill.good {
    background: var(--sp-success);
  }
  .fill.warn,
  .num.warn {
    background: var(--sp-warning);
  }
  .num.warn {
    background: none;
    color: var(--sp-warning);
  }
  .fill.bad {
    background: var(--sp-danger);
  }
  .num.bad,
  .bad-text {
    color: var(--sp-danger);
  }
  .num.good {
    color: var(--sp-text);
  }
  .services {
    list-style: none;
    margin: 0;
    padding: 0;
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(260px, 1fr));
    gap: var(--sp-space-1) var(--sp-space-4);
    font-size: var(--sp-text-sm);
  }
  .services li {
    display: flex;
    justify-content: space-between;
    gap: var(--sp-space-2);
    min-width: 0;
  }
  .services a {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .services .faint {
    white-space: nowrap;
  }
  .status {
    margin: 0;
    font-size: var(--sp-text-sm);
  }
  .good-text {
    color: var(--sp-success);
  }
  .warn-box {
    padding: var(--sp-space-2) var(--sp-space-3);
    border-radius: var(--sp-radius-md);
    border: 1px solid color-mix(in srgb, var(--sp-warning) 45%, var(--sp-border));
    background: color-mix(in srgb, var(--sp-warning) 12%, transparent);
    display: flex;
    flex-direction: column;
    gap: var(--sp-space-1);
  }
  .cmd {
    display: block;
    padding: var(--sp-space-2);
    background: var(--sp-bg-code);
    border-radius: var(--sp-radius-sm);
    overflow-x: auto;
    white-space: pre-wrap;
    word-break: break-all;
    font-size: var(--sp-text-xs);
  }
  .adv {
    display: flex;
    flex-direction: column;
    gap: var(--sp-space-3);
    padding-top: var(--sp-space-3);
    border-top: 1px solid var(--sp-border);
  }
  .adv .row {
    align-items: flex-start;
  }
  .unit {
    display: flex;
    align-items: center;
    gap: var(--sp-space-2);
  }
  .unit input {
    width: 110px;
  }
  .check {
    display: flex;
    gap: var(--sp-space-2);
    align-items: center;
    font-size: var(--sp-text-sm);
    margin-top: var(--sp-space-5);
  }
  .end {
    justify-content: flex-end;
    align-items: center;
  }
</style>
