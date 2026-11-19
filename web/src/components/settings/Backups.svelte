<script>
  import { onMount } from 'svelte';
  import { api } from '../../lib/api.js';
  import Modal from '../Modal.svelte';
  import Icon from '../Icon.svelte';
  import BackupsView from '../BackupsView.svelte';
  import DestinationDialog from '../DestinationDialog.svelte';

  let dests = $state([]);
  let err = $state('');
  let editing = $state(null);
  let removing = $state(null);
  let revealed = $state({});
  let viewKey = $state(0);

  async function load() {
    dests = await api.get('/backups/destinations');
  }
  onMount(() => {
    load().catch((e) => (err = e.message));
  });

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

  function openNew() {
    editing = { new: true };
  }
  function openEdit(d) {
    editing = d;
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
  <DestinationDialog dest={editing.id ? editing : null} onclose={() => (editing = null)} onsaved={async () => { editing = null; await load(); viewKey++; }} />
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
</style>
