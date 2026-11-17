<script>
  import { onMount } from 'svelte';
  import { api } from '../../lib/api.js';
  import { ui, data, timeAgo } from '../../lib/state.svelte.js';
  import Modal from '../Modal.svelte';
  import Icon from '../Icon.svelte';

  // Settings for each channel type: [key, label, placeholder, secret, help]
  const types = {
    discord: {
      label: 'Discord',
      fields: [['url', 'Webhook URL', 'https://discord.com/api/webhooks/…', true, 'Channel settings › Integrations › Webhooks › New webhook › Copy URL.']],
    },
    slack: {
      label: 'Slack',
      fields: [['url', 'Webhook URL', 'https://hooks.slack.com/services/…', true, 'Create an app with Incoming Webhooks turned on, then add a webhook to a channel.']],
    },
    telegram: {
      label: 'Telegram',
      fields: [
        ['token', 'Bot token', '123456:ABC…', true, 'Create a bot with @BotFather.'],
        ['chatId', 'Chat ID', '-1001234567890', false, 'Add the bot to the chat; the ID of a group starts with -100.'],
      ],
    },
    email: {
      label: 'Email',
      fields: [
        ['to', 'Send to', 'ops@example.com', false, 'Separate several addresses with commas.'],
        ['from', 'From', 'simplepanel@example.com', false, ''],
        ['host', 'SMTP server', 'smtp.example.com', false, ''],
        ['port', 'Port', '587', false, ''],
        ['username', 'Username', '', false, ''],
        ['password', 'Password', '', true, ''],
      ],
    },
    webhook: {
      label: 'Webhook',
      fields: [
        ['url', 'URL', 'https://example.com/hooks/simplepanel', false, 'Receives a JSON POST for each event.'],
        ['secret', 'Signing secret', 'Optional', true, 'Signs each request: X-SimplePanel-Signature is sha256= HMAC of the body.'],
      ],
    },
  };

  let channels = $state([]);
  let events = $state([]);
  let log = $state([]);
  let err = $state('');
  let editing = $state(null);
  let formErr = $state('');
  let testMsg = $state('');
  let busy = $state('');
  let removing = $state(null);

  async function load() {
    const r = await api.get('/notifications');
    channels = r.channels;
    events = r.events;
    log = r.log;
  }
  onMount(() => {
    load().catch((e) => (err = e.message));
  });

  function openNew(type = 'discord') {
    formErr = testMsg = '';
    editing = {
      name: '',
      type,
      config: type === 'email' ? { port: '587', security: 'starttls' } : {},
      events: events.filter((e) => e.default).map((e) => e.key),
      projects: [],
      enabled: true,
      saved: {},
    };
  }
  // The saved config with secret fields emptied: empty keeps the saved value.
  function editableConfig(c) {
    const config = { ...c.config };
    for (const f of types[c.type].fields) if (f[3]) config[f[0]] = '';
    return config;
  }
  function openEdit(c) {
    formErr = testMsg = '';
    editing = { ...c, config: editableConfig(c), events: [...c.events], projects: [...c.projects], saved: c.config };
  }
  function switchType(t) {
    editing.type = t;
    editing.config = t === 'email' ? { port: '587', security: 'starttls' } : {};
    editing.saved = {};
  }

  function body() {
    return { name: editing.name, type: editing.type, config: editing.config, events: editing.events, projects: editing.projects, enabled: editing.enabled };
  }

  async function test() {
    formErr = testMsg = '';
    busy = 'test';
    try {
      await api.post(editing.id ? `/notifications/${editing.id}/test` : '/notifications/test', body());
      testMsg = 'Sent. Check that the test message arrived.';
    } catch (e) {
      formErr = e.message;
    } finally {
      busy = '';
    }
  }

  async function save(e) {
    e.preventDefault();
    formErr = testMsg = '';
    try {
      if (editing.id) await api.put(`/notifications/${editing.id}`, body());
      else await api.post('/notifications', body());
      editing = null;
      await load();
    } catch (e2) {
      formErr = e2.message;
    }
  }

  async function toggle(c) {
    try {
      await api.put(`/notifications/${c.id}`, { name: c.name, type: c.type, config: editableConfig(c), events: c.events, projects: c.projects, enabled: !c.enabled });
      await load();
    } catch (e) {
      err = e.message;
    }
  }

  async function remove() {
    try {
      await api.del(`/notifications/${removing.id}`);
      removing = null;
      await load();
    } catch (e) {
      err = e.message;
    }
  }

  function toggleIn(list, v) {
    return list.includes(v) ? list.filter((x) => x !== v) : [...list, v];
  }

  const channelName = (id) => channels.find((c) => c.id === id)?.name || 'deleted channel';
</script>

<section class="card">
  <div class="row between">
    <h2>Notifications</h2>
    <button class="btn primary" onclick={() => openNew()}><Icon name="plus" size={16} /> Add channel</button>
  </div>
  <p class="muted">Get told when a deploy or backup fails, a service keeps crashing, or the server runs low on CPU, memory or disk.</p>
  {#if err}<p class="error">{err}</p>{/if}
  {#if channels.length === 0}
    <div class="quick">
      {#each Object.entries(types) as [key, t]}
        <button class="btn" onclick={() => openNew(key)}>{t.label}</button>
      {/each}
    </div>
  {/if}
  <ul class="list">
    {#each channels as c (c.id)}
      <li>
        <Icon name="bell" size={18} />
        <div class="grow">
          <div class="name">{c.name} <span class="faint small">{types[c.type]?.label}</span>{#if !c.enabled}<span class="off">Off</span>{/if}</div>
          <div class="faint small">
            {c.events.length} event{c.events.length === 1 ? '' : 's'}
            · {c.projects.length ? c.projects.map((p) => data.projects.find((x) => x.id === p)?.name || p).join(', ') : 'all projects'}
          </div>
        </div>
        <button class="btn ghost small" onclick={() => toggle(c)}>{c.enabled ? 'Turn off' : 'Turn on'}</button>
        <button class="btn ghost small" onclick={() => openEdit(c)}>Edit</button>
        <button class="btn ghost small icon" title="Remove {c.name}" aria-label="Remove {c.name}" onclick={() => (removing = c)}><Icon name="trash" size={16} /></button>
      </li>
    {/each}
  </ul>
</section>

{#if log.length}
  <section class="card">
    <h3>Recent deliveries</h3>
    <ul class="list">
      {#each log.slice(0, 15) as l (l.id)}
        <li>
          <span class="dot" class:succeeded={l.ok} class:failed={!l.ok}></span>
          <div class="grow">
            <div class="small">{l.title}</div>
            {#if !l.ok}<div class="small fail">{l.error}</div>{/if}
          </div>
          <span class="faint small">{channelName(l.channelId)} · {timeAgo(l.createdAt)}</span>
        </li>
      {/each}
    </ul>
  </section>
{/if}

{#if editing}
  <Modal title={editing.id ? `Edit ${editing.name}` : 'Add a notification channel'} onclose={() => (editing = null)}>
    <form class="form" onsubmit={save}>
      {#if !editing.id}
        <div class="segmented wrap" role="radiogroup" aria-label="Type">
          {#each Object.entries(types) as [key, t]}
            <button type="button" role="radio" aria-checked={editing.type === key} class:active={editing.type === key} onclick={() => switchType(key)}>{t.label}</button>
          {/each}
        </div>
      {/if}
      <label class="field">
        <span>Name</span>
        <input type="text" bind:value={editing.name} placeholder="#ops on Discord" required />
      </label>
      <div class="grid">
        {#each types[editing.type].fields as [key, label, ph, secret, help] (key)}
          <label class="field" class:wide={key === 'url' || key === 'to'}>
            <span>{label}</span>
            <input
              type={secret ? 'password' : 'text'}
              bind:value={editing.config[key]}
              placeholder={secret && editing.saved[key] ? 'Unchanged' : ph}
              autocomplete="off"
            />
            {#if help}<small>{help}</small>{/if}
          </label>
        {/each}
        {#if editing.type === 'email'}
          <label class="field">
            <span>Security</span>
            <select bind:value={editing.config.security}>
              <option value="starttls">STARTTLS (port 587)</option>
              <option value="tls">TLS (port 465)</option>
              <option value="none">None</option>
            </select>
          </label>
        {/if}
      </div>

      <div class="field">
        <span>Send these events</span>
        <div class="events">
          {#each events as ev (ev.key)}
            <label class="check"><input type="checkbox" checked={editing.events.includes(ev.key)} onchange={() => (editing.events = toggleIn(editing.events, ev.key))} /> {ev.label}</label>
          {/each}
        </div>
      </div>

      {#if ui.advanced && data.projects.length}
        <div class="field">
          <span>Only for these projects</span>
          <div class="events">
            {#each data.projects as p (p.id)}
              <label class="check"><input type="checkbox" checked={editing.projects.includes(p.id)} onchange={() => (editing.projects = toggleIn(editing.projects, p.id))} /> {p.name}</label>
            {/each}
          </div>
          <small>Leave all unticked for every project. Server alerts are always sent.</small>
        </div>
      {/if}

      {#if formErr}<p class="error">{formErr}</p>{/if}
      {#if testMsg}<p class="ok">{testMsg}</p>{/if}
      <div class="row end">
        <button type="button" class="btn ghost" onclick={() => (editing = null)}>Cancel</button>
        <button type="button" class="btn" disabled={busy === 'test'} onclick={test}>{busy === 'test' ? 'Sending…' : 'Send test'}</button>
        <button class="btn primary">Save</button>
      </div>
    </form>
  </Modal>
{/if}

{#if removing}
  <Modal title="Remove {removing.name}?" onclose={() => (removing = null)}>
    <p class="muted">Notifications stop going to this channel.</p>
    <div class="row end">
      <button class="btn ghost" onclick={() => (removing = null)}>Cancel</button>
      <button class="btn danger" onclick={remove}>Remove channel</button>
    </div>
  </Modal>
{/if}

<style>
  .quick {
    display: flex;
    gap: var(--sp-space-2);
    flex-wrap: wrap;
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
  .off {
    font-size: var(--sp-text-xs);
    color: var(--sp-warning);
    font-weight: var(--sp-weight-bold);
  }
  .small {
    font-size: var(--sp-text-sm);
  }
  .fail {
    color: var(--sp-danger);
    overflow-wrap: anywhere;
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
  .grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
    gap: var(--sp-space-3);
  }
  .grid .wide {
    grid-column: 1 / -1;
  }
  .events {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(190px, 1fr));
    gap: var(--sp-space-1) var(--sp-space-3);
  }
  .check {
    display: flex;
    align-items: center;
    gap: var(--sp-space-2);
    font-size: var(--sp-text-sm);
  }
</style>
