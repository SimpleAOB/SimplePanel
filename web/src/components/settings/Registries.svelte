<script>
  // Settings → Registries: saved logins for private container registries
  // (GHCR, Docker Hub, GitLab, or any other). Apps that run a Docker image
  // pick one; Git builds use them for private base images.
  import { onMount } from 'svelte';
  import { api } from '../../lib/api.js';
  import { ui, timeAgo } from '../../lib/state.svelte.js';
  import { registryPresets, presetFor, forgetRegistries } from '../../lib/registries.js';
  import Modal from '../Modal.svelte';
  import Icon from '../Icon.svelte';

  let regs = $state([]);
  let loaded = $state(false);
  let err = $state('');
  let notice = $state('');
  let editing = $state(null);
  let formErr = $state('');
  let testMsg = $state('');
  let checkFailed = $state(false);
  let busy = $state('');
  let removing = $state(null);

  async function load() {
    forgetRegistries();
    try {
      regs = await api.get('/registries');
    } catch (e) {
      err = e.message;
    } finally {
      loaded = true;
    }
  }
  onMount(load);

  function reset() {
    formErr = testMsg = '';
    checkFailed = false;
  }
  function openNew() {
    reset();
    notice = '';
    const p = registryPresets[0];
    editing = { preset: p.key, name: p.name, server: p.server, username: '', password: '' };
  }
  function openEdit(r) {
    reset();
    notice = '';
    editing = { id: r.id, preset: presetFor(r.server).key, name: r.name, server: r.server, username: r.username, password: '', apps: r.apps };
  }
  function pick(key) {
    const p = registryPresets.find((x) => x.key === key);
    const prev = registryPresets.find((x) => x.key === editing.preset);
    if (!editing.name || editing.name === prev?.name) editing.name = p.name;
    editing.preset = key;
    editing.server = p.server;
    reset();
  }

  let preset = $derived(editing ? registryPresets.find((x) => x.key === editing.preset) : null);
  let showServer = $derived(editing && (editing.preset === 'other' || ui.advanced || editing.id));

  async function test() {
    reset();
    busy = 'test';
    try {
      await api.post('/registries/test', { ...editing, id: editing.id || 0 });
      testMsg = `Signed in to ${editing.server} as ${editing.username}.`;
    } catch (e) {
      formErr = e.message;
    } finally {
      busy = '';
    }
  }

  async function save(e, skipCheck = false) {
    e?.preventDefault();
    reset();
    busy = 'save';
    const tokenChanged = editing.id && editing.password;
    try {
      const body = { ...editing, skipCheck };
      const r = editing.id ? await api.put(`/registries/${editing.id}`, body) : await api.post('/registries', body);
      if (tokenChanged && r.apps) {
        notice = `Saved. ${r.apps} app${r.apps === 1 ? '' : 's'} use this login: redeploy ${r.apps === 1 ? 'it' : 'them'} to start using the new token.`;
      }
      editing = null;
      await load();
    } catch (e2) {
      formErr = e2.message;
      checkFailed = e2.status === 400 && e2.message.startsWith('signing in to');
    } finally {
      busy = '';
    }
  }

  async function remove() {
    err = '';
    try {
      await api.del(`/registries/${removing.id}`);
      removing = null;
      await load();
    } catch (e) {
      err = e.message;
    }
  }
</script>

<section class="card">
  <div class="row between">
    <h2>Registries</h2>
    <button class="btn primary" onclick={openNew}><Icon name="plus" size={16} /> Add login</button>
  </div>
  <p class="muted">
    Sign in to private container registries such as GitHub (GHCR), Docker Hub or GitLab. Apps that run a private image use the login for
    its registry, and Git builds use them for private base images. Tokens are stored on this server only and are never shown again.
  </p>
  {#if err}<p class="error">{err}</p>{/if}
  {#if notice}<p class="ok">{notice}</p>{/if}

  {#if loaded && regs.length === 0}
    <p class="faint">No logins yet. Public images work without one.</p>
  {/if}
  {#if regs.length}
    <ul class="regs">
      {#each regs as r (r.id)}
        {@const p = presetFor(r.server)}
        <li>
          <span class="logo" aria-hidden="true">{p.badge}</span>
          <div class="grow">
            <div class="name">{r.name}</div>
            <div class="faint small">
              <span class="mono">{r.server}</span> · signed in as <span class="mono">{r.username}</span> · {r.apps} app{r.apps === 1 ? '' : 's'} · updated {timeAgo(r.updatedAt)}
            </div>
          </div>
          <div class="actions">
            <button class="btn ghost small" onclick={() => openEdit(r)}>Edit</button>
            <button class="btn ghost small icon" title="Remove {r.name}" aria-label="Remove {r.name}" onclick={() => (removing = r)}><Icon name="trash" size={16} /></button>
          </div>
        </li>
      {/each}
    </ul>
  {/if}
</section>

{#if editing}
  <Modal title={editing.id ? `Edit ${editing.name}` : 'Add a registry login'} onclose={() => (editing = null)}>
    <form class="form" onsubmit={save}>
      {#if !editing.id}
        <div class="field">
          <span>Registry</span>
          <div class="segmented wrap" role="radiogroup" aria-label="Registry">
            {#each registryPresets as p (p.key)}
              <button type="button" role="radio" aria-checked={editing.preset === p.key} class:active={editing.preset === p.key} onclick={() => pick(p.key)}>{p.label}</button>
            {/each}
          </div>
        </div>
      {/if}
      <div class="row">
        <label class="field">
          <span>Name</span>
          <input type="text" bind:value={editing.name} maxlength="60" placeholder="GitHub" required />
        </label>
        {#if showServer}
          <label class="field">
            <span>Registry address</span>
            <input type="text" class="mono" bind:value={editing.server} placeholder="registry.example.com" required />
          </label>
        {/if}
      </div>
      <div class="row">
        <label class="field">
          <span>Username</span>
          <input type="text" bind:value={editing.username} autocomplete="off" placeholder={preset?.userHint} required />
        </label>
        <label class="field">
          <span>{preset?.tokenLabel || 'Access token'}</span>
          <input type="password" bind:value={editing.password} autocomplete="new-password" placeholder={editing.id ? 'Unchanged' : ''} required={!editing.id} />
        </label>
      </div>
      {#if preset?.help}
        <small class="help">
          {preset.help}
          {#if preset.link}<a href={preset.link} target="_blank" rel="noopener noreferrer">{preset.linkText} <Icon name="link" size={12} /></a>{/if}
        </small>
      {/if}
      {#if editing.id && editing.apps}
        <small class="help">{editing.apps} app{editing.apps === 1 ? '' : 's'} use this login. A new token applies from each app's next deploy.</small>
      {/if}

      {#if formErr}
        <p class="error">{formErr}</p>
        {#if checkFailed}
          <small class="help">If the registry is only reachable from other servers in the swarm, you can <button type="button" class="inline-link" onclick={() => save(null, true)}>save it without checking</button>.</small>
        {/if}
      {/if}
      {#if testMsg}<p class="ok">{testMsg}</p>{/if}
      <div class="row end">
        <button type="button" class="btn ghost" onclick={() => (editing = null)}>Cancel</button>
        <button type="button" class="btn" disabled={!!busy} onclick={test}>{busy === 'test' ? 'Testing…' : 'Test login'}</button>
        <button class="btn primary" disabled={!!busy}>{busy === 'save' ? 'Checking…' : 'Save'}</button>
      </div>
    </form>
  </Modal>
{/if}

{#if removing}
  <Modal title="Remove {removing.name}?" onclose={() => (removing = null)}>
    <p class="muted">
      {#if removing.apps}
        {removing.apps} app{removing.apps === 1 ? '' : 's'} use this login. They keep running, but their next deploy pulls without a login, which fails for private images.
      {:else}
        No apps use this login.
      {/if}
    </p>
    <div class="row end">
      <button class="btn ghost" onclick={() => (removing = null)}>Cancel</button>
      <button class="btn danger" onclick={remove}>Remove login</button>
    </div>
  </Modal>
{/if}

<style>
  .regs {
    list-style: none;
    margin: 0;
    padding: 0;
    border: 1px solid var(--sp-border);
    border-radius: var(--sp-radius-md);
  }
  .regs li {
    display: flex;
    align-items: center;
    gap: var(--sp-space-3);
    padding: var(--sp-space-3);
  }
  .actions {
    display: flex;
    align-items: center;
    gap: var(--sp-space-1);
    flex: none;
  }
  .regs li + li {
    border-top: 1px solid var(--sp-border);
  }
  .logo {
    display: inline-grid;
    place-items: center;
    width: 28px;
    height: 28px;
    border-radius: var(--sp-radius-md);
    background: var(--sp-bg-active);
    font-size: var(--sp-text-xs);
    font-weight: var(--sp-weight-bold);
    flex: none;
  }
  .grow {
    flex: 1;
    min-width: 0;
    overflow-wrap: anywhere;
  }
  .name {
    font-weight: var(--sp-weight-bold);
  }
  .small {
    font-size: var(--sp-text-sm);
  }
  .form {
    display: flex;
    flex-direction: column;
    gap: var(--sp-space-3);
  }
  .segmented.wrap {
    flex-wrap: wrap;
    align-self: flex-start;
  }
  .help {
    color: var(--sp-text-muted);
  }
  .help a {
    display: inline-flex;
    align-items: center;
    gap: 2px;
  }
  .inline-link {
    border: 0;
    padding: 0;
    background: none;
    color: var(--sp-accent);
    font: inherit;
    cursor: pointer;
  }
  .inline-link:hover {
    text-decoration: underline;
  }
</style>
