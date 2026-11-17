<script>
  // Invite and password reset links: set a password, then sign in.
  import { onMount } from 'svelte';
  import { api } from '../lib/api.js';
  import { roleLabels } from '../lib/state.svelte.js';
  import Logo from './Logo.svelte';

  let { token, ondone } = $props();
  let info = $state(null);
  let loadErr = $state('');
  let password = $state('');
  let confirm = $state('');
  let error = $state('');
  let busy = $state(false);

  onMount(async () => {
    try {
      info = await api.get(`/invite/${encodeURIComponent(token)}`);
    } catch (e) {
      loadErr = e.message;
    }
  });

  async function submit(e) {
    e.preventDefault();
    error = '';
    if (password !== confirm) {
      error = "Passwords don't match.";
      return;
    }
    busy = true;
    try {
      const u = await api.post(`/invite/${encodeURIComponent(token)}`, { password });
      ondone(u);
    } catch (err) {
      error = err.message;
    } finally {
      busy = false;
    }
  }
</script>

<div class="wrap">
  <form class="card box" onsubmit={submit}>
    <div class="brand"><Logo size={40} /><h1>SimplePanel</h1></div>
    {#if loadErr}
      <p class="error">{loadErr}</p>
      <a href="#/">Go to sign in</a>
    {:else if !info}
      <p class="muted">Checking your link…</p>
    {:else}
      {#if info.kind === 'invite'}
        <p>You've been invited to SimplePanel as <strong>{roleLabels[info.role] || info.role}</strong>.</p>
        <p class="muted">Choose a password for <strong>{info.username}</strong> to finish setting up your account.</p>
      {:else}
        <p>Choose a new password for <strong>{info.username}</strong>.</p>
        <p class="muted">This signs you out of every other session.</p>
      {/if}
      <input type="text" autocomplete="username" value={info.username} hidden readonly />
      <label class="field">
        <span>New password</span>
        <input type="password" bind:value={password} autocomplete="new-password" required />
        <small>At least 10 characters.</small>
      </label>
      <label class="field">
        <span>Confirm password</span>
        <input type="password" bind:value={confirm} autocomplete="new-password" required />
      </label>
      {#if error}<p class="error">{error}</p>{/if}
      <button class="btn primary" disabled={busy}>{info.kind === 'invite' ? 'Create account' : 'Set password'}</button>
    {/if}
  </form>
</div>

<style>
  .wrap {
    min-height: 100%;
    display: grid;
    place-items: center;
    padding: var(--sp-space-4);
    background: var(--sp-bg-rail);
  }
  .box {
    width: 100%;
    max-width: 400px;
    display: flex;
    flex-direction: column;
    gap: var(--sp-space-4);
    padding: var(--sp-space-6) var(--sp-space-5);
    background: var(--sp-bg-main);
    box-shadow: var(--sp-shadow);
  }
  .brand {
    display: flex;
    align-items: center;
    gap: var(--sp-space-3);
  }
</style>
