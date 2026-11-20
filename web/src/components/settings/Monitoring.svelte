<script>
  import { onMount } from 'svelte';
  import { api } from '../../lib/api.js';
  import { ui } from '../../lib/state.svelte.js';
  import ServerMemory from './ServerMemory.svelte';

  let s = $state(null);
  let defaults = $state(null);
  let msg = $state('');
  let err = $state('');

  onMount(async () => {
    try {
      const r = await api.get('/monitoring/settings');
      s = r.settings;
      defaults = r.defaults;
    } catch (e) {
      err = e.message;
    }
  });

  async function save(e) {
    e.preventDefault();
    msg = err = '';
    try {
      const body = Object.fromEntries(Object.entries(s).map(([k, v]) => [k, Number(v) || 0]));
      s = await api.put('/monitoring/settings', body);
      msg = 'Saved.';
    } catch (e2) {
      err = e2.message;
    }
  }

  const fields = [
    ['cpu', 'Server CPU', '%', false],
    ['memory', 'Server memory', '%', false],
    ['disk', 'Server disk', '%', false],
    ['serviceMemory', 'Service memory', '% of its limit', true],
    ['serviceCpu', 'Service CPU', '% of its limit', true],
  ];
</script>

<section class="card">
  <h2>Monitoring</h2>
  <p class="muted">
    SimplePanel records CPU, memory, disk and network use of the server and every service every 30 seconds.
    See it on the Overview page and on each service's Metrics tab. It runs inside SimplePanel, so there's nothing extra to install.
  </p>
</section>

<ServerMemory />

{#if err}<p class="error">{err}</p>{/if}
{#if s}
  <form class="card" onsubmit={save}>
    <h3>Alerts</h3>
    <p class="muted">Send a notification when usage stays above these levels. Set a level to 0 to turn that alert off. Alerts go to the notification channels that have them ticked.</p>
    <div class="grid">
      {#each fields as [key, label, unit, adv] (key)}
        {#if !adv || ui.advanced}
          <label class="field">
            <span>{label}</span>
            <div class="unit"><input type="number" min="0" max="100" bind:value={s[key]} /><span class="faint">{unit}</span></div>
          </label>
        {/if}
      {/each}
      <label class="field">
        <span>For at least</span>
        <div class="unit"><input type="number" min="1" max="120" bind:value={s.minutes} /><span class="faint">minutes</span></div>
      </label>
    </div>
    {#if !ui.advanced}<small class="faint">Turn on Advanced view for per-service CPU and memory alerts.</small>{/if}

    <h3>History</h3>
    <label class="field narrow">
      <span>Keep history for</span>
      <div class="unit"><input type="number" min="1" max="90" bind:value={s.retentionDays} /><span class="faint">days</span></div>
      <small>The last 24 hours are kept at full detail, older history as 5-minute averages.</small>
    </label>

    <div class="row end">
      {#if msg}<span class="ok">{msg}</span>{/if}
      <button type="button" class="btn ghost" onclick={() => (s = { ...defaults })}>Reset to defaults</button>
      <button class="btn primary">Save</button>
    </div>
  </form>
{/if}

<style>
  .grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(170px, 1fr));
    gap: var(--sp-space-3);
  }
  .unit {
    display: flex;
    align-items: center;
    gap: var(--sp-space-2);
    font-size: var(--sp-text-sm);
    white-space: nowrap;
  }
  .unit input {
    width: 90px;
    flex: none;
  }
  .narrow {
    max-width: 320px;
  }
</style>
