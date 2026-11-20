<script>
  import { api } from '../lib/api.js';
  import Logo from './Logo.svelte';

  let { mode, ondone, needsCode = false } = $props();
  let code = $state('');
  // svelte-ignore state_referenced_locally
  let username = $state(mode === 'setup' ? 'admin' : '');
  let password = $state('');
  let confirm = $state('');
  let error = $state('');
  let busy = $state(false);

  async function submit(e) {
    e.preventDefault();
    error = '';
    if (mode === 'setup' && password !== confirm) {
      error = "Passwords don't match.";
      return;
    }
    busy = true;
    try {
      const u = await api.post(mode === 'setup' ? '/setup' : '/login', mode === 'setup' ? { username, password, code: code.trim() } : { username, password });
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
    {#if mode === 'setup'}
      <p class="muted">Welcome! Create the admin account to claim this server.</p>
      {#if needsCode}
        <label class="field">
          <span>Setup code</span>
          <input type="text" bind:value={code} autocomplete="one-time-code" spellcheck="false" class="mono" required />
          <small>The installer printed it when it finished. Lost it? Run <code>docker service inspect simplepanel --format '&#123;&#123;json .Spec.TaskTemplate.ContainerSpec.Env&#125;&#125;'</code> on the server.</small>
        </label>
      {/if}
    {:else}
      <p class="muted">Sign in to continue.</p>
    {/if}
    <label class="field">
      <span>Username</span>
      <input type="text" bind:value={username} autocomplete="username" required />
    </label>
    <label class="field">
      <span>Password</span>
      <input type="password" bind:value={password} autocomplete={mode === 'setup' ? 'new-password' : 'current-password'} required />
      {#if mode === 'setup'}<small>At least 10 characters.</small>{/if}
    </label>
    {#if mode === 'setup'}
      <label class="field">
        <span>Confirm password</span>
        <input type="password" bind:value={confirm} autocomplete="new-password" required />
      </label>
    {/if}
    {#if error}<p class="error">{error}</p>{/if}
    <button class="btn primary" disabled={busy}>{mode === 'setup' ? 'Create account' : 'Sign in'}</button>
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
    max-width: 380px;
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
