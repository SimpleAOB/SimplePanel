<script>
  // Add or edit an S3-compatible backup destination. Used in Settings and
  // straight from a database's Backups tab.
  import { api } from '../lib/api.js';
  import { ui } from '../lib/state.svelte.js';
  import Modal from './Modal.svelte';

  let { dest = null, onclose, onsaved } = $props();

  // Endpoint presets for common providers.
  const providers = [
    ['s3', 'AWS S3', 'https://s3.amazonaws.com', 'us-east-1'],
    // R2 and B2 build the endpoint from the account ID or region.
    ['r2', 'Cloudflare R2', '', 'auto'],
    ['b2', 'Backblaze B2', '', ''],
    ['other', 'Other (MinIO, Wasabi…)', '', 'us-east-1'],
  ];
  const keyHelp = {
    s3: 'https://docs.aws.amazon.com/IAM/latest/UserGuide/id_credentials_access-keys.html',
    r2: 'https://developers.cloudflare.com/r2/api/tokens/',
    b2: 'https://www.backblaze.com/docs/cloud-storage-s3-compatible-app-keys',
  };

  // The dialog edits a copy, made once when it opens.
  function initial() {
    return dest
      ? { ...dest, provider: 'other', secretKey: '', passphrase: '', encrypt: dest.encrypted, wasEncrypted: dest.encrypted }
      : { provider: 's3', name: '', endpoint: providers[0][2], region: providers[0][3], bucket: '', account: '', prefix: 'simplepanel', accessKey: '', secretKey: '', pathStyle: false, passphrase: '', encrypt: true };
  }
  let editing = $state(initial());
  let formErr = $state('');
  let testMsg = $state('');
  let busy = $state('');

  function pickProvider(key) {
    const p = providers.find((x) => x[0] === key);
    editing.provider = key;
    editing.endpoint = p[2];
    editing.region = p[3];
    editing.pathStyle = key === 'other';
  }

  // The endpoint these providers need, from the account ID or region.
  function endpointFor(e) {
    if (e.provider === 'r2') return e.account.trim() ? `https://${e.account.trim()}.r2.cloudflarestorage.com` : '';
    if (e.provider === 'b2') return e.region.trim() ? `https://s3.${e.region.trim()}.backblazeb2.com` : '';
    return e.endpoint;
  }

  function body() {
    const b = { ...editing, endpoint: endpointFor(editing) };
    delete b.account;
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
      await onsaved?.();
    } catch (e2) {
      formErr = e2.message;
    } finally {
      busy = '';
    }
  }

  function generate() {
    const a = new Uint8Array(18);
    crypto.getRandomValues(a);
    editing.passphrase = btoa(String.fromCharCode(...a)).replace(/[+/=]/g, '').slice(0, 24);
  }
</script>

  <Modal title={editing.id ? `Edit ${editing.name}` : 'Add a backup destination'} {onclose}>
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
      {#if !editing.id && keyHelp[editing.provider]}
        <small class="faint">You need a bucket and an access key from your storage provider. <a href={keyHelp[editing.provider]} target="_blank" rel="noopener noreferrer">Where do I get these?</a></small>
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
      {#if !editing.id && editing.provider === 'r2'}
        <label class="field">
          <span>Account ID</span>
          <input type="text" bind:value={editing.account} placeholder="0123456789abcdef0123456789abcdef" autocomplete="off" required />
          <small>In the Cloudflare dashboard under R2 › Overview, labelled Account ID.</small>
        </label>
      {:else if !editing.id && editing.provider === 'b2'}
        <label class="field">
          <span>Region</span>
          <input type="text" bind:value={editing.region} placeholder="us-west-004" required />
          <small>On the bucket's page in Backblaze, the Endpoint reads s3.<em>region</em>.backblazeb2.com.</small>
        </label>
      {:else}
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
      {/if}
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
          <span class="label">Encryption passphrase</span>
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
        <button type="button" class="btn ghost" onclick={onclose}>Cancel</button>
        <button type="button" class="btn" disabled={!!busy} onclick={test}>{busy === 'test' ? 'Testing…' : 'Test connection'}</button>
        <button class="btn primary" disabled={!!busy}>{busy === 'save' ? 'Checking…' : 'Save'}</button>
      </div>
    </form>
  </Modal>

<style>
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
  .label {
    font-size: var(--sp-text-xs);
    font-weight: var(--sp-weight-bold);
    text-transform: uppercase;
    letter-spacing: 0.04em;
    color: var(--sp-text-muted);
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
  .end {
    justify-content: flex-end;
  }
  .ok {
    color: var(--sp-success);
  }
</style>
