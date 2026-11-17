<script>
  // Resource history for the server (path /metrics/server) or a service
  // (/apps/:id/metrics), with a range picker. Refreshes every 30 seconds.
  import { onMount } from 'svelte';
  import { api } from '../lib/api.js';
  import { bytes, rate, pct } from '../lib/format.js';
  import Chart from './Chart.svelte';

  let { path, server = false } = $props();

  const ranges = [
    ['1h', '1 hour'],
    ['6h', '6 hours'],
    ['24h', '24 hours'],
    ['7d', '7 days'],
    ['30d', '30 days'],
  ];
  let range = $state('1h');
  let m = $state(null);
  let err = $state('');

  async function load() {
    try {
      m = await api.get(`${path}?range=${range}`);
      err = '';
    } catch (e) {
      err = e.message;
    }
  }
  onMount(() => {
    const t = setInterval(load, 30000);
    return () => clearInterval(t);
  });
  $effect(() => {
    range;
    load();
  });

  let pts = $derived(m?.samples ?? []);
  let last = $derived(pts[pts.length - 1]);
  let long = $derived(range === '7d' || range === '30d');
  let memLimit = $derived(m?.memoryLimitMb ? m.memoryLimitMb * 1048576 * Math.max(1, m.replicas || 1) : 0);
  let cpuCap = $derived(server || !m?.cpuLimit || !m?.host?.cpus ? 0 : (m.cpuLimit * Math.max(1, m.replicas || 1) * 100) / m.host.cpus);
</script>

<div class="metrics">
  <div class="bar">
    <div class="segmented" role="radiogroup" aria-label="Time range">
      {#each ranges as [k, label]}
        <button type="button" role="radio" aria-checked={range === k} class:active={range === k} onclick={() => (range = k)}>{label}</button>
      {/each}
    </div>
    {#if m?.host?.cpus}
      <span class="faint small">{m.host.cpus} CPU cores · {bytes(m.host.memTotal)} memory</span>
    {/if}
  </div>
  {#if err}<p class="error">{err}</p>{/if}

  {#if last}
    <div class="tiles">
      <div class="tile">
        <span class="label">CPU</span>
        <span class="value">{pct(last.cpu)}</span>
        <span class="faint small">of the whole server</span>
      </div>
      <div class="tile">
        <span class="label">Memory</span>
        <span class="value">{bytes(last.mem)}</span>
        <span class="faint small">
          {#if server}{pct((last.mem / last.memLimit) * 100)} of {bytes(last.memLimit)}{:else if memLimit}{pct((last.mem / memLimit) * 100)} of the {bytes(memLimit)} limit{:else}no limit set{/if}
        </span>
      </div>
      {#if server && last.diskTotal}
        <div class="tile">
          <span class="label">Disk</span>
          <span class="value">{pct((last.disk / last.diskTotal) * 100)}</span>
          <span class="faint small">{bytes(last.disk)} of {bytes(last.diskTotal)}</span>
        </div>
      {/if}
      <div class="tile">
        <span class="label">Network</span>
        <span class="value">{rate(last.rx + last.tx)}</span>
        <span class="faint small">{rate(last.rx)} in · {rate(last.tx)} out</span>
      </div>
    </div>
  {/if}

  {#if m}
    <div class="charts">
      <div class="card"><Chart title="CPU" series={[{ key: 'cpu', label: 'CPU', color: 'var(--sp-series-1)' }]} points={pts} format={(v) => pct(v)} max={server ? 100 : 0} limit={cpuCap} limitLabel="CPU limit" {long} /></div>
      <div class="card">
        <Chart
          title="Memory"
          series={[{ key: 'mem', label: 'Memory', color: 'var(--sp-series-1)' }]}
          points={pts}
          format={(v) => bytes(v, 0)}
          max={server && last ? last.memLimit : 0}
          limit={server ? 0 : memLimit}
          limitLabel="Memory limit"
          {long}
        />
      </div>
      <div class="card">
        <Chart
          title="Network"
          series={[
            { key: 'rx', label: 'In', color: 'var(--sp-series-1)' },
            { key: 'tx', label: 'Out', color: 'var(--sp-series-2)' },
          ]}
          points={pts}
          format={rate}
          {long}
        />
      </div>
      {#if server}
        <div class="card"><Chart title="Disk used" series={[{ key: 'disk', label: 'Disk', color: 'var(--sp-series-1)' }]} points={pts} format={(v) => bytes(v, 0)} max={last?.diskTotal || 0} {long} /></div>
      {/if}
    </div>
  {/if}
</div>

<style>
  .metrics {
    display: flex;
    flex-direction: column;
    gap: var(--sp-space-4);
  }
  .bar {
    display: flex;
    justify-content: space-between;
    align-items: center;
    gap: var(--sp-space-3);
    flex-wrap: wrap;
  }
  .small {
    font-size: var(--sp-text-xs);
  }
  .tiles {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(150px, 1fr));
    gap: var(--sp-space-3);
  }
  .tile {
    display: flex;
    flex-direction: column;
    gap: 2px;
    padding: var(--sp-space-3) var(--sp-space-4);
    border: 1px solid var(--sp-border);
    border-radius: var(--sp-radius-lg);
    background: var(--sp-bg-panel);
  }
  .label {
    font-size: var(--sp-text-xs);
    text-transform: uppercase;
    letter-spacing: 0.04em;
    color: var(--sp-text-muted);
    font-weight: var(--sp-weight-bold);
  }
  .value {
    font-size: var(--sp-text-xl);
    font-weight: var(--sp-weight-bold);
  }
  .charts {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(320px, 1fr));
    gap: var(--sp-space-3);
  }
  @media (max-width: 400px) {
    .charts {
      grid-template-columns: minmax(0, 1fr);
    }
  }
</style>
