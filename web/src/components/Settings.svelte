<script>
  import { onMount } from 'svelte';
  import { api } from '../lib/api.js';
  import { ui, toggleTheme } from '../lib/state.svelte.js';
  import Topbar from './Topbar.svelte';

  let panelDomain = $state('');
  let domainMsg = $state('');
  let domainErr = $state('');
  let current = $state('');
  let next = $state('');
  let pwMsg = $state('');
  let pwErr = $state('');

  onMount(async () => {
    const s = await api.get('/system');
    panelDomain = s.panelDomain || '';
  });

  async function saveDomain(e) {
    e.preventDefault();
    domainMsg = domainErr = '';
    try {
      await api.put('/settings', { panelDomain });
      domainMsg = panelDomain
        ? `Saved. Once DNS for ${panelDomain} points at this server, open https://${panelDomain}.`
        : 'Saved. The panel is only reachable on port 3000.';
    } catch (err) {
      domainErr = err.message;
    }
  }

  async function changePassword(e) {
    e.preventDefault();
    pwMsg = pwErr = '';
    try {
      await api.post('/me/password', { current, new: next });
      current = next = '';
      pwMsg = 'Password changed. Other sessions were signed out.';
    } catch (err) {
      pwErr = err.message;
    }
  }
</script>

<Topbar>Settings</Topbar>
<div class="page">
  <form class="card" onsubmit={saveDomain}>
    <h2>Panel domain</h2>
    <p class="muted">Serve SimplePanel itself over HTTPS on your own domain. Point an A record at this server first.</p>
    <div class="row">
      <label class="field">
        <span>Domain</span>
        <input type="text" bind:value={panelDomain} placeholder="panel.example.com" />
      </label>
      <button class="btn primary">Save</button>
    </div>
    {#if domainMsg}<p class="ok">{domainMsg}</p>{/if}
    {#if domainErr}<p class="error">{domainErr}</p>{/if}
  </form>

  <section class="card">
    <h2>Appearance</h2>
    <div class="row between">
      <p class="muted">Theme</p>
      <div class="segmented">
        <button class:active={ui.theme === 'dark'} onclick={() => ui.theme !== 'dark' && toggleTheme()}>Dark</button>
        <button class:active={ui.theme === 'light'} onclick={() => ui.theme !== 'light' && toggleTheme()}>Light</button>
      </div>
    </div>
  </section>

  <form class="card" onsubmit={changePassword}>
    <h2>Change password</h2>
    <div class="row">
      <label class="field">
        <span>Current password</span>
        <input type="password" bind:value={current} autocomplete="current-password" required />
      </label>
      <label class="field">
        <span>New password</span>
        <input type="password" bind:value={next} autocomplete="new-password" required />
      </label>
      <button class="btn">Change</button>
    </div>
    {#if pwMsg}<p class="ok">{pwMsg}</p>{/if}
    {#if pwErr}<p class="error">{pwErr}</p>{/if}
  </form>
</div>

<style>
  .page {
    padding: var(--sp-space-5);
    display: flex;
    flex-direction: column;
    gap: var(--sp-space-4);
    max-width: 760px;
    width: 100%;
  }
  .card {
    display: flex;
    flex-direction: column;
    gap: var(--sp-space-3);
  }
  .between {
    justify-content: space-between;
    align-items: center;
  }
  .ok {
    color: var(--sp-success);
    font-size: var(--sp-text-sm);
  }
</style>
