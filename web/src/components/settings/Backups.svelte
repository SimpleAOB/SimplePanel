<script>
  import { onMount } from 'svelte';
  import { api } from '../../lib/api.js';
  import { ui } from '../../lib/state.svelte.js';
  import Modal from '../Modal.svelte';
  import Icon from '../Icon.svelte';
  import BackupsView from '../BackupsView.svelte';

  // Endpoint presets for common providers.
  const providers = [
    ['s3', 'AWS S3', 'https://s3.amazonaws.com', 'us-east-1'],
    ['r2', 'Cloudflare R2', 'https://<account-id>.r2.cloudflarestorage.com', 'auto'],
    ['b2', 'Backblaze B2', 'https://s3.<region>.backblazeb2.com', ''],
    ['other', 'Other (MinIO, Wasabi…)', '', 'us-east-1'],
  ];

  let dests = $state([]);
  let err = $state('');
  let editing = $state(null);
  let formErr = $state('');
  let testMsg = $state('');
  let busy = $state('');
  let removing = $state(null);
  let revealed = $state({});
  let viewKey = $state(0);

  async function load() {
    dests = await api.get('/backups/destinations');
  }
  onMount(() => {
    load().catch((e) => (err = e.message));
  });

  function openNew() {
    formErr = testMsg = '';
    editing = { provider: 's3', name: '', endpoint: providers[0][2], region: providers[0][3], bucket: '', prefix: 'simplepanel', accessKey: '', secretKey: '', pathStyle: false, passphrase: '', encrypt: true };
  }
  function openEdit(d) {
    formErr = testMsg = '';
    editing = { ...d, provider: 'other', secretKey: '', passphrase: '', encrypt: d.encrypted, wasEncrypted: d.encrypted };
  }
  function pickProvider(key) {
    const p = providers.find((x) => x[0] === key);
    editing.provider = key;
    editing.endpoint = p[2];
    editing.region = p[3];
    editing.pathStyle = key === 'other';
  }

  function body() {
    const b = { ...editing };
    b.clearPassphrase = !editing.encrypt && editing.wasEncrypted;
    if (!editing.encrypt) b.passphrase = '';
    return b;
  }

  async function test() {
    formErr = testMsg = '';
    busy = 'test';
    try {
      await api.post('/backups/destinations/test', body());
      testMsg = 'Connected. SimplePanel can write to this bucket.';
    } catch (e) {
      formErr = e.message;
    } finally {
      busy = '';
    }
  }

  async function save(e) {
    e.preventDefault();
    formErr = testMsg = '';
    if (editing.encrypt && !editing.wasEncrypted && editing.passphrase.length < 12) {
      formErr = 'Use an encryption passphrase of at least 12 characters, or turn encryption off.';
      return;
    }
    busy = 'save';
    try {
      if (editing.id) await api.put(`/backups/destinations/${editing.id}`, body());
      else await api.post('/backups/destinations', body());
      editing = null;
      await load();
      viewKey++;
    } catch (e2) {
      formErr = e2.message;
    } finally {
      busy = '';
    }
  }

  async function remove() {
    err = '';
    try {
      await api.del(`/backups/destinations/${removing.id}`);
      removing = null;
      await load();
      viewKey++;
    } catch (e) {
      err = e.message;
    }
  }

  async function reveal(d) {
    if (revealed[d.id]) {
      revealed[d.id] = '';
      return;
    }
    try {
      revealed[d.id] = (await api.post(`/backups/destinations/${d.id}/key`)).passphrase;
    } catch (e) {
      err = e.message;
    }
  }

  function generate() {
    const a = new Uint8Array(18);
    crypto.getRandomValues(a);
    editing.passphrase = btoa(String.fromCharCode(...a)).replace(/[+/=]/g, '').slice(0, 24);
  }
</script>

<section class="card">
  <div class="row between">
    <h2>Backup destinations</h2>
    <button class="btn primary" onclick={openNew}><Icon name="plus" size={16} /> Add destination</button>
  </div>
  <p class="muted">
    Where backups are stored: any S3-compatible storage, such as AWS S3, Cloudflare R2, Backblaze B2, Wasabi or your own MinIO.
    Then set up backups on each database's Backups tab.
  </p>
  {#if err}<p class="error">{err}</p>{/if}
  {#if dests.length === 0}
    <p class="faint">No destinations yet.</p>
  {/if}
  <ul class="list">
    {#each dests as d (d.id)}
      <li>
        <Icon name="archive" size={18} />
        <div class="grow">
          <div class="name">
            {d.name}
            {#if d.encrypted}<span class="tag"><Icon name="lock" size={12} /> Encrypted</span>{/if}
          </div>
          <div class="faint small mono">{d.bucket}/{d.prefix} · {d.endpoint.replace(/^https?:\/\//, '')}</div>
          {#if revealed[d.id]}
            <div class="small">Passphrase: <code>{revealed[d.id]}</code></div>
          {/if}
        </div>
        {#if d.encrypted}
          <button class="btn ghost small" onclick={() => reveal(d)}>{revealed[d.id] ? 'Hide' : 'Show'} passphrase</button>
        {/if}
        <button class="btn ghost small" onclick={() => openEdit(d)}>Edit</button>
        <button class="btn ghost small icon" title="Remove {d.name}" aria-label="Remove {d.name}" onclick={() => (removing = d)}><Icon name="trash" size={16} /></button>
      </li>
    {/each}
  </ul>
</section>

<section class="sub">
  <h2>SimplePanel's configuration</h2>
  <p class="muted">Back up SimplePanel's own database: projects, apps, settings, users and backup history. Database contents are backed up separately, per database.</p>
</section>
{#key viewKey}<BackupsView panel />{/key}

{#if editing}
  <Modal title={editing.id ? `Edit ${editing.name}` : 'Add a backup destination'} onclose={() => (editing = null)}>
    <form class="form" onsubmit={save}>
      {#if !editing.id}
        <div class="field">
          <span>Provider</span>
          <div class="segmented wrap">
            {#each providers as [key, label]}
              <button type="button" class:active={editing.provider === key} onclick={() => pickProvider(key)}>{label}</button>
            {/each}
          </div>
        </div>
      {/if}
      <div class="row">
        <label class="field">
          <span>Name</span>
          <input type="text" bind:value={editing.name} placeholder="Offsite backups" required />
        </label>
        <label class="field">
          <span>Bucket</span>
          <input type="text" bind:value={editing.bucket} placeholder="my-backups" required />
        </label>
      </div>
      <div class="row">
        <label class="field grow2">
          <span>Endpoint</span>
          <input type="text" bind:value={editing.endpoint} placeholder="https://s3.example.com" required />
        </label>
        <label class="field">
          <span>Region</span>
          <input type="text" bind:value={editing.region} placeholder="us-east-1" />
        </label>
      </div>
      <div class="row">
        <label class="field">
          <span>Access key</span>
          <input type="text" bind:value={editing.accessKey} autocomplete="off" required />
        </label>
        <label class="field">
          <span>Secret key</span>
          <input type="password" bind:value={editing.secretKey} autocomplete="new-password" placeholder={editing.id ? 'Unchanged' : ''} required={!editing.id} />
        </label>
      </div>
      {#if ui.advanced || editing.provider === 'other'}
        <div class="row">
          <label class="field">
            <span>Folder in the bucket</span>
            <input type="text" bind:value={editing.prefix} placeholder="simplepanel" />
          </label>
          <label class="check"><input type="checkbox" bind:checked={editing.pathStyle} /> Path-style URLs (MinIO and most self-hosted storage)</label>
        </div>
      {/if}

      <div class="field">
        <label class="check"><input type="checkbox" bind:checked={editing.encrypt} /> <strong>Encrypt backups</strong></label>
        {#if editing.encrypt}
          {#if editing.wasEncrypted}
            <small>Backups are encrypted with the saved passphrase. Enter a new one only to change it; older backups still need the old one.</small>
          {/if}
          <div class="row tight">
            <input type="password" bind:value={editing.passphrase} autocomplete="new-password" placeholder={editing.wasEncrypted ? 'Unchanged' : 'At least 12 characters'} aria-label="Encryption passphrase" />
            <button type="button" class="btn" onclick={generate}>Generate</button>
          </div>
          <small>
            Backups are encrypted before they leave this server, with <a href="https://age-encryption.org" target="_blank" rel="noopener noreferrer">age</a>.
            Keep the passphrase somewhere safe: without it, backups can't be restored on a new server. You can see it again here later.
          </small>
        {:else}
          <small>Without encryption, anyone with access to the bucket can read your data.</small>
        {/if}
      </div>

      {#if formErr}<p class="error">{formErr}</p>{/if}
      {#if testMsg}<p class="ok">{testMsg}</p>{/if}
      <div class="row end">
        <button type="button" class="btn ghost" onclick={() => (editing = null)}>Cancel</button>
        <button type="button" class="btn" disabled={!!busy} onclick={test}>{busy === 'test' ? 'Testing…' : 'Test connection'}</button>
        <button class="btn primary" disabled={!!busy}>{busy === 'save' ? 'Checking…' : 'Save'}</button>
      </div>
    </form>
  </Modal>
{/if}

{#if removing}
  <Modal title="Remove {removing.name}?" onclose={() => (removing = null)}>
    <p class="muted">Schedules that use it are removed too. Backups already in the bucket are kept there.</p>
    <div class="row end">
      <button class="btn ghost" onclick={() => (removing = null)}>Cancel</button>
      <button class="btn danger" onclick={remove}>Remove destination</button>
    </div>
  </Modal>
{/if}

<style>
  .sub {
    display: flex;
    flex-direction: column;
    gap: var(--sp-space-1);
    margin-top: var(--sp-space-3);
  }
  .list {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
  }
  .list li {
    display: flex;
    align-items: center;
    gap: var(--sp-space-3);
    padding: var(--sp-space-2) 0;
    border-top: 1px solid var(--sp-border);
    flex-wrap: wrap;
  }
  .list li:first-child {
    border-top: 0;
  }
  .grow {
    flex: 1;
    min-width: 180px;
  }
  .name {
    display: flex;
    align-items: center;
    gap: var(--sp-space-2);
    font-weight: var(--sp-weight-medium);
  }
  .tag {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    font-size: var(--sp-text-xs);
    color: var(--sp-success);
  }
  .small {
    font-size: var(--sp-text-sm);
  }
  .form {
    display: flex;
    flex-direction: column;
    gap: var(--sp-space-3);
  }
  .grow2 {
    flex: 2 1 220px !important;
  }
  .check {
    display: flex;
    align-items: center;
    gap: var(--sp-space-2);
    font-size: var(--sp-text-sm);
    min-height: 34px;
  }
  .segmented.wrap {
    flex-wrap: wrap;
    align-self: flex-start;
  }
  .row.tight {
    flex-wrap: nowrap;
    align-items: center;
  }
  .row.tight input {
    flex: 1;
  }
</style>
