<script>
  import { api } from '../../lib/api.js';
  import { ui, data, toggleTheme, roleLabels } from '../../lib/state.svelte.js';

  let current = $state('');
  let next = $state('');
  let msg = $state('');
  let err = $state('');

  async function changePassword(e) {
    e.preventDefault();
    msg = err = '';
    try {
      await api.post('/me/password', { current, new: next });
      current = next = '';
      msg = 'Password changed. Other sessions were signed out.';
    } catch (e2) {
      err = e2.message;
    }
  }
</script>

<section class="card">
  <h2>Your account</h2>
  <p class="muted">Signed in as <strong>{data.user?.username}</strong> ({roleLabels[data.user?.role] || data.user?.role}).</p>
</section>

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
  <input type="text" autocomplete="username" value={data.user?.username} hidden readonly />
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
  {#if msg}<p class="ok">{msg}</p>{/if}
  {#if err}<p class="error">{err}</p>{/if}
</form>
