<script>
  import { copyText } from '../../lib/clipboard.js';
  import { onMount } from 'svelte';
  import { api } from '../../lib/api.js';
  import { data, timeAgo, roleLabels } from '../../lib/state.svelte.js';
  import Modal from '../Modal.svelte';
  import Icon from '../Icon.svelte';

  const roles = [
    ['admin', 'Admin', 'Everything except changing the owner: all projects, users, backups and server settings.'],
    ['developer', 'Developer', 'Create, configure and deploy apps and databases in the projects you pick. No server settings.'],
    ['viewer', 'Viewer', 'See apps, logs, metrics and backups in the projects you pick. Change nothing.'],
  ];

  let users = $state([]);
  let audit = $state([]);
  let err = $state('');
  let editing = $state(null); // { id?, username, role, allProjects, access }
  let formErr = $state('');
  let link = $state(null); // { username, url, kind }
  let removing = $state(null);
  let copied = $state(false);
  let showAll = $state(false);

  async function load() {
    [users, audit] = await Promise.all([api.get('/users'), api.get('/audit?limit=100')]);
  }
  onMount(() => {
    load().catch((e) => (err = e.message));
  });

  function openNew() {
    formErr = '';
    editing = { username: '', role: 'developer', allProjects: false, access: {}, dbQuery: false };
  }
  function openEdit(u) {
    formErr = '';
    editing = { id: u.id, username: u.username, role: u.role, allProjects: u.allProjects, access: { ...u.access }, dbQuery: u.dbQuery };
  }

  async function save(e) {
    e.preventDefault();
    formErr = '';
    const body = {
      username: editing.username,
      role: editing.role,
      allProjects: editing.allProjects,
      access: editing.allProjects ? {} : editing.access,
      dbQuery: editing.role !== 'admin' && editing.dbQuery,
    };
    try {
      if (editing.id) {
        await api.put(`/users/${editing.id}`, body);
      } else {
        const r = await api.post('/users', body);
        link = { username: r.user.username, url: location.origin + r.invitePath, kind: 'invite' };
      }
      editing = null;
      await load();
    } catch (e2) {
      formErr = e2.message;
    }
  }

  async function reset(u) {
    err = '';
    try {
      const r = await api.post(`/users/${u.id}/reset`);
      link = { username: u.username, url: location.origin + r.invitePath, kind: r.kind };
    } catch (e) {
      err = e.message;
    }
  }

  async function remove() {
    err = '';
    try {
      await api.del(`/users/${removing.id}`);
      removing = null;
      await load();
    } catch (e) {
      err = e.message;
    }
  }

  async function copy(text) {
    try {
      await copyText(text);
      copied = true;
      setTimeout(() => (copied = false), 1500);
    } catch {
      /* clipboard unavailable */
    }
  }

  function accessSummary(u) {
    if (u.role === 'owner' || u.role === 'admin') return 'All projects';
    const parts = Object.entries(u.access)
      .map(([pid, r]) => {
        const p = data.projects.find((x) => String(x.id) === pid);
        return p ? `${p.name} (${roleLabels[r].toLowerCase()})` : null;
      })
      .filter(Boolean);
    if (u.allProjects) return parts.length ? `All projects; ${parts.join(', ')}` : 'All projects';
    return parts.join(', ') || 'No projects';
  }
</script>

<section class="card">
  <div class="row between">
    <h2>Users</h2>
    <button class="btn primary" onclick={openNew}><Icon name="plus" size={16} /> Invite user</button>
  </div>
  <p class="muted">Invite people with their own sign-in. Each gets a role, and developers and viewers only see the projects you pick.</p>
  {#if err}<p class="error">{err}</p>{/if}
  <ul class="list">
    {#each users as u (u.id)}
      <li>
        <span class="avatar">{u.username[0].toUpperCase()}</span>
        <div class="grow">
          <div class="name">
            {u.username}
            <span class="role {u.role}">{roleLabels[u.role]}</span>
            {#if u.pending}<span class="pending">Invite not used yet</span>{/if}
            {#if u.dbQuery}<span class="perm" title="Can run read-only queries in the databases of their projects">Query databases</span>{/if}
          </div>
          <div class="faint small">{accessSummary(u)}</div>
        </div>
        {#if u.role !== 'owner' && u.id !== data.user?.id}
          <div class="actions">
            <button class="btn ghost small" onclick={() => openEdit(u)}>Edit</button>
            <button class="btn ghost small" onclick={() => reset(u)}>{u.pending ? 'New invite link' : 'Reset password'}</button>
            <button class="btn ghost small icon" title="Remove {u.username}" aria-label="Remove {u.username}" onclick={() => (removing = u)}><Icon name="trash" size={16} /></button>
          </div>
        {/if}
      </li>
    {/each}
  </ul>
</section>

<section class="card">
  <h2>Activity</h2>
  <p class="muted small">Changes made in SimplePanel and through AI tokens, newest first. Secret values are never recorded.</p>
  {#if audit.length === 0}
    <p class="muted small">Nothing yet.</p>
  {:else}
    <ul class="list activity">
      {#each showAll ? audit : audit.slice(0, 10) as e (e.id)}
        <li>
          <span class="dot" class:failed={!e.ok} class:succeeded={e.ok}></span>
          <div class="grow">
            <div>{e.action.replaceAll('_', ' ')} {#if e.target}<span class="faint">· {e.target}</span>{/if}</div>
            {#if e.args && e.action.includes('quer') || e.action === 'browse table'}
              <div class="faint small mono q">{(() => { try { return JSON.parse(e.args).query; } catch { return ''; } })()}</div>
            {/if}
            {#if !e.ok && e.error}<div class="small fail">{e.error}</div>{/if}
          </div>
          <div class="faint small when">{e.username || e.tokenName}{e.source === 'mcp' ? ' (AI)' : ''}<br />{timeAgo(e.createdAt)}</div>
        </li>
      {/each}
    </ul>
    {#if audit.length > 10}
      <div><button class="btn ghost small" onclick={() => (showAll = !showAll)}>{showAll ? 'Show less' : `Show all ${audit.length}`}</button></div>
    {/if}
  {/if}
</section>

{#if editing}
  <Modal title={editing.id ? `Edit ${editing.username}` : 'Invite a user'} onclose={() => (editing = null)}>
    <form class="form" onsubmit={save}>
      {#if !editing.id}
        <label class="field">
          <span>Username</span>
          <!-- svelte-ignore a11y_autofocus -->
          <input type="text" bind:value={editing.username} placeholder="jamie" autofocus required />
          <small>Lowercase letters, numbers and hyphens. They choose their own password from the invite link.</small>
        </label>
      {/if}
      <div class="field">
        <span>Role</span>
        <div class="roles">
          {#each roles as [key, label, desc]}
            <label class="choice" class:active={editing.role === key}>
              <input type="radio" name="role" value={key} bind:group={editing.role} />
              <strong>{label}</strong>
              <small class="muted">{desc}</small>
            </label>
          {/each}
        </div>
      </div>
      {#if editing.role !== 'admin'}
        <div class="field">
          <span>Projects</span>
          <label class="check"><input type="checkbox" bind:checked={editing.allProjects} /> All projects, including new ones, as {roleLabels[editing.role].toLowerCase()}</label>
          {#if !editing.allProjects}
            {#if data.projects.length === 0}
              <small>There are no projects yet.</small>
            {/if}
            <div class="access">
              {#each data.projects as p (p.id)}
                <label class="prow">
                  <span>{p.name}</span>
                  <select bind:value={editing.access[String(p.id)]} aria-label="Access to {p.name}">
                    <option value={undefined}>No access</option>
                    <option value="viewer">Viewer</option>
                    {#if editing.role === 'developer'}<option value="developer">Developer</option>{/if}
                  </select>
                </label>
              {/each}
            </div>
          {/if}
        </div>
      {/if}
      <div class="field">
        <span>Permissions</span>
        {#if editing.role === 'admin'}
          <small>Admins can always query databases (read-only).</small>
        {:else}
          <label class="check"><input type="checkbox" bind:checked={editing.dbQuery} /> Query databases (read-only)</label>
          <small>Lets them browse tables and run read-only queries on the databases in their projects. Queries go through a read-only database user and are listed in Activity.</small>
        {/if}
      </div>
      {#if formErr}<p class="error">{formErr}</p>{/if}
      <div class="row end">
        <button type="button" class="btn ghost" onclick={() => (editing = null)}>Cancel</button>
        <button class="btn primary">{editing.id ? 'Save' : 'Create invite link'}</button>
      </div>
    </form>
  </Modal>
{/if}

{#if link}
  <Modal title={link.kind === 'invite' ? `Invite link for ${link.username}` : `Password reset link for ${link.username}`} onclose={() => (link = null)}>
    <p class="muted">
      Send this link to {link.username}. It works once and expires in 7 days.
      {#if link.kind !== 'invite'}Using it signs them out everywhere.{/if}
    </p>
    <div class="row tight">
      <input type="text" readonly class="mono" value={link.url} aria-label="Link" />
      <button class="btn primary" onclick={() => copy(link.url)}>{copied ? 'Copied' : 'Copy'}</button>
    </div>
    <div class="row end"><button class="btn" onclick={() => (link = null)}>Done</button></div>
  </Modal>
{/if}

{#if removing}
  <Modal title="Remove {removing.username}?" onclose={() => (removing = null)}>
    <p class="muted">They are signed out and their AI tokens stop working. Their past activity stays in the log.</p>
    <div class="row end">
      <button class="btn ghost" onclick={() => (removing = null)}>Cancel</button>
      <button class="btn danger" onclick={remove}>Remove user</button>
    </div>
  </Modal>
{/if}

<style>
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
    min-width: 160px;
  }
  .name {
    display: flex;
    align-items: center;
    gap: var(--sp-space-2);
    font-weight: var(--sp-weight-medium);
    flex-wrap: wrap;
  }
  .small {
    font-size: var(--sp-text-sm);
  }
  .avatar {
    width: 32px;
    height: 32px;
    border-radius: var(--sp-radius-round);
    background: var(--sp-bg-elevated);
    border: 1px solid var(--sp-border-strong);
    display: grid;
    place-items: center;
    font-weight: var(--sp-weight-bold);
    font-size: var(--sp-text-sm);
    flex: none;
  }
  .role,
  .pending {
    font-size: var(--sp-text-xs);
    font-weight: var(--sp-weight-bold);
    padding: 1px var(--sp-space-2);
    border-radius: var(--sp-radius-round);
    border: 1px solid var(--sp-border-strong);
    color: var(--sp-text-muted);
  }
  .role.owner,
  .role.admin {
    border-color: var(--sp-accent);
    color: var(--sp-accent);
  }
  .pending {
    border-color: var(--sp-warning);
    color: var(--sp-warning);
  }
  .perm {
    font-size: var(--sp-text-xs);
    font-weight: var(--sp-weight-bold);
    padding: 1px var(--sp-space-2);
    border-radius: var(--sp-radius-round);
    border: 1px solid var(--sp-success);
    color: var(--sp-success);
  }
  .q {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    max-width: 640px;
  }
  .actions {
    display: flex;
    gap: var(--sp-space-1);
  }
  .when {
    text-align: right;
  }
  .fail {
    color: var(--sp-danger);
  }
  .form {
    display: flex;
    flex-direction: column;
    gap: var(--sp-space-4);
  }
  .roles {
    display: grid;
    gap: var(--sp-space-2);
  }
  .choice {
    display: grid;
    grid-template-columns: auto 1fr;
    column-gap: var(--sp-space-2);
    padding: var(--sp-space-2) var(--sp-space-3);
    border: 1px solid var(--sp-border);
    border-radius: var(--sp-radius-md);
    cursor: pointer;
  }
  .choice.active {
    border-color: var(--sp-accent);
    background: var(--sp-bg-hover);
  }
  .choice input {
    grid-row: span 2;
    margin-top: 4px;
  }
  .check {
    display: flex;
    align-items: center;
    gap: var(--sp-space-2);
    font-size: var(--sp-text-sm);
  }
  .access {
    display: flex;
    flex-direction: column;
    gap: var(--sp-space-1);
  }
  .prow {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--sp-space-3);
    font-size: var(--sp-text-sm);
  }
  .prow select {
    width: 160px;
  }
  .row.tight {
    flex-wrap: nowrap;
    align-items: center;
  }
  .row.tight input {
    flex: 1;
  }
</style>
