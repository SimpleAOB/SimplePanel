<script>
  import { copyText } from '../lib/clipboard.js';
  // Settings section: API tokens for the built-in MCP server, setup
  // instructions for AI clients, and the log of what AI tokens did.
  import { onMount } from 'svelte';
  import { api } from '../lib/api.js';
  import { timeAgo, data } from '../lib/state.svelte.js';
  import Icon from './Icon.svelte';

  let panelDomain = $state('');

  const scopes = [
    ['read', 'Read-only', 'See projects, services, logs and deployments. Query databases that allow AI read-only access.'],
    ['manage', 'Manage', 'Read-only, plus create apps and databases, change settings and environment variables, add domains and deploy.'],
    ['full', 'Full', 'Manage, plus delete apps, databases and projects, and restore backups over current data.'],
  ];
  const scopeLabel = Object.fromEntries(scopes.map(([k, l]) => [k, l]));

  let tokens = $state([]);
  let audit = $state([]);
  let name = $state('');
  let scope = $state('read');
  let created = $state(null); // { name, secret } right after creation
  let client = $state('claude');
  let err = $state('');
  let copied = $state('');
  let showAll = $state(false);

  let endpoint = $derived(panelDomain ? `https://${panelDomain}/mcp` : `${location.origin}/mcp`);
  let insecure = $derived(endpoint.startsWith('http://') && !['localhost', '127.0.0.1'].includes(location.hostname));
  let secret = $derived(created?.secret || '<your token>');
  let claudeCmd = $derived(`claude mcp add --transport http simplepanel ${endpoint} --header "Authorization: Bearer ${secret}"`);
  let jsonCfg = $derived(
    JSON.stringify({ mcpServers: { simplepanel: { type: 'http', url: endpoint, headers: { Authorization: `Bearer ${secret}` } } } }, null, 2),
  );

  async function load() {
    [tokens, audit] = await Promise.all([api.get('/tokens'), api.get('/audit?limit=100&source=mcp')]);
  }
  onMount(() => {
    load().catch((e) => (err = e.message));
    api.get('/system').then((s) => (panelDomain = s.panelDomain || '')).catch(() => {});
    const t = setInterval(() => api.get('/audit?limit=100&source=mcp').then((a) => (audit = a)).catch(() => {}), 10000);
    return () => clearInterval(t);
  });

  async function create(e) {
    e.preventDefault();
    err = '';
    try {
      const r = await api.post('/tokens', { name: name.trim(), scope });
      created = { name: r.token.name, secret: r.secret };
      name = '';
      await load();
    } catch (e2) {
      err = e2.message;
    }
  }

  async function revoke(t) {
    err = '';
    try {
      await api.del(`/tokens/${t.id}`);
      if (created?.name === t.name) created = null;
      await load();
    } catch (e) {
      err = e.message;
    }
  }

  async function copy(text, key) {
    try {
      await copyText(text);
      copied = key;
      setTimeout(() => (copied = ''), 1500);
    } catch {
      /* clipboard blocked */
    }
  }
</script>

<section class="card">
  <h2>Connect an AI</h2>
  <p class="muted">
    SimplePanel has a built-in MCP server, so AI assistants such as Claude can check on your apps, read logs, change settings and deploy.
    Create a token, then add SimplePanel to your AI app with the setup below. The AI only gets the tools its token allows and never sees passwords
    or other secrets.
  </p>

  <form class="create" onsubmit={create}>
    <label class="field">
      <span>Token name</span>
      <input type="text" bind:value={name} placeholder="e.g. Claude on my laptop" maxlength="60" required />
    </label>
    <div class="field">
      <span>Access</span>
      <div class="segmented" role="radiogroup" aria-label="Access">
        {#each scopes as [key, label]}
          <button type="button" role="radio" aria-checked={scope === key} class:active={scope === key} onclick={() => (scope = key)}>{label}</button>
        {/each}
      </div>
      <small>{scopes.find((s) => s[0] === scope)[2]} A token can never do more than your own role allows.</small>
    </div>
    <div><button class="btn primary">Create token</button></div>
  </form>
  {#if err}<p class="error">{err}</p>{/if}

  {#if created}
    <div class="once">
      <p><strong>Token “{created.name}” created.</strong> Copy it now: it won't be shown again.</p>
      <div class="row tight">
        <input type="text" readonly class="mono" value={created.secret} aria-label="Token" />
        <button class="btn" onclick={() => copy(created.secret, 'secret')}>{copied === 'secret' ? 'Copied' : 'Copy'}</button>
      </div>
    </div>
  {/if}

  <div class="setup">
    <div class="row between">
      <h3>Setup</h3>
      <div class="segmented" role="tablist" aria-label="AI app">
        <button type="button" role="tab" aria-selected={client === 'claude'} class:active={client === 'claude'} onclick={() => (client = 'claude')}>Claude Code</button>
        <button type="button" role="tab" aria-selected={client === 'json'} class:active={client === 'json'} onclick={() => (client = 'json')}>Other apps</button>
      </div>
    </div>
    {#if client === 'claude'}
      <p class="muted small">Run this in a terminal:</p>
      <pre class="code">{claudeCmd}</pre>
      <div><button class="btn small" onclick={() => copy(claudeCmd, 'cmd')}><Icon name="copy" size={14} /> {copied === 'cmd' ? 'Copied' : 'Copy command'}</button></div>
    {:else}
      <p class="muted small">For apps that take an MCP config file (Claude Desktop, Cursor, VS Code and others). Some call the type <code>streamable-http</code>.</p>
      <pre class="code">{jsonCfg}</pre>
      <div><button class="btn small" onclick={() => copy(jsonCfg, 'json')}><Icon name="copy" size={14} /> {copied === 'json' ? 'Copied' : 'Copy config'}</button></div>
    {/if}
    <small class="faint">Server URL: <code>{endpoint}</code>{#if !created} · Replace &lt;your token&gt; with a token you created.{/if}</small>
    {#if insecure}
      <p class="warn">This panel is served over plain HTTP, so the token would travel unencrypted. Set a panel domain under General to use HTTPS first.</p>
    {/if}
  </div>

  <h3>Tokens</h3>
  {#if tokens.length === 0}
    <p class="muted small">No tokens yet.</p>
  {:else}
    <ul class="list">
      {#each tokens as t (t.id)}
        <li>
          <div class="grow">
            <div class="name">{t.name}</div>
            <div class="faint small">{#if t.username && t.username !== data.user?.username}{t.username} · {/if}Created {timeAgo(t.createdAt)} · {t.lastUsedAt ? `Last used ${timeAgo(t.lastUsedAt)}` : 'Never used'}</div>
          </div>
          {#if t.api}<span class="scope" title="Can also use the REST API and CLI">API</span>{/if}
          <span class="scope {t.scope}">{scopeLabel[t.scope] || t.scope}</span>
          <button class="btn ghost small" onclick={() => revoke(t)}>Revoke</button>
        </li>
      {/each}
    </ul>
  {/if}

  <h3>AI activity</h3>
  <p class="muted small">Changes made with tokens, and every database query. Secret values are never recorded.</p>
  {#if audit.length === 0}
    <p class="muted small">Nothing yet.</p>
  {:else}
    <ul class="list activity">
      {#each showAll ? audit : audit.slice(0, 8) as e (e.id)}
        <li>
          <span class="dot" class:fail={!e.ok} title={e.ok ? 'Succeeded' : 'Failed'}></span>
          <div class="grow">
            <div><code>{e.action}</code> {#if e.target}<span class="target">{e.target}</span>{/if}</div>
            {#if e.action === 'db_query'}
              <div class="faint small mono q">{(() => { try { return JSON.parse(e.args).query; } catch { return ''; } })()}</div>
            {/if}
            {#if !e.ok}<div class="fail-msg small">{e.error}</div>{/if}
          </div>
          <div class="faint small when">{e.tokenName}{#if e.username && e.username !== data.user?.username} · {e.username}{/if}<br />{timeAgo(e.createdAt)}</div>
        </li>
      {/each}
    </ul>
    {#if audit.length > 8}
      <div><button class="btn ghost small" onclick={() => (showAll = !showAll)}>{showAll ? 'Show less' : `Show all ${audit.length}`}</button></div>
    {/if}
  {/if}
</section>

<style>
  .card {
    display: flex;
    flex-direction: column;
    gap: var(--sp-space-3);
  }
  h3 {
    font-size: var(--sp-text-md, 1rem);
    margin-top: var(--sp-space-2);
  }
  .create {
    display: flex;
    flex-direction: column;
    gap: var(--sp-space-3);
  }
  .segmented {
    align-self: flex-start;
  }
  .once {
    border: 1px solid var(--sp-success);
    border-radius: var(--sp-radius-md);
    padding: var(--sp-space-3);
    display: flex;
    flex-direction: column;
    gap: var(--sp-space-2);
  }
  .row.tight {
    flex-wrap: nowrap;
    align-items: center;
  }
  .row.tight input {
    flex: 1;
  }
  .between {
    justify-content: space-between;
    align-items: center;
  }
  .setup {
    display: flex;
    flex-direction: column;
    gap: var(--sp-space-2);
    padding: var(--sp-space-3);
    border-radius: var(--sp-radius-md);
    background: var(--sp-bg-main);
  }
  .setup h3 {
    margin: 0;
  }
  .code {
    margin: 0;
    padding: var(--sp-space-3);
    border-radius: var(--sp-radius-md);
    border: 1px solid var(--sp-border);
    background: var(--sp-bg-elevated);
    font-family: var(--sp-font-mono);
    font-size: var(--sp-text-sm);
    white-space: pre-wrap;
    overflow-wrap: anywhere;
  }
  .small {
    font-size: var(--sp-text-sm);
  }
  .warn {
    color: var(--sp-warning, var(--sp-danger));
    font-size: var(--sp-text-sm);
  }
  .list {
    list-style: none;
    margin: 0;
    padding: 0;
    border: 1px solid var(--sp-border);
    border-radius: var(--sp-radius-md);
  }
  .list li {
    display: flex;
    align-items: center;
    gap: var(--sp-space-3);
    padding: var(--sp-space-2) var(--sp-space-3);
  }
  .list li + li {
    border-top: 1px solid var(--sp-border);
  }
  .grow {
    flex: 1;
    min-width: 0;
  }
  .name {
    font-weight: var(--sp-weight-bold);
  }
  .scope {
    font-size: var(--sp-text-xs);
    font-weight: var(--sp-weight-bold);
    padding: 2px var(--sp-space-2);
    border-radius: var(--sp-radius-round);
    border: 1px solid var(--sp-border);
    color: var(--sp-text-muted);
  }
  .scope.manage {
    color: var(--sp-accent);
    border-color: var(--sp-accent);
  }
  .scope.full {
    color: var(--sp-danger);
    border-color: var(--sp-danger);
  }
  .activity li {
    align-items: flex-start;
  }
  .dot {
    width: 8px;
    height: 8px;
    margin-top: 7px;
    border-radius: 50%;
    background: var(--sp-success);
    flex: none;
  }
  .dot.fail {
    background: var(--sp-danger);
  }
  .target {
    color: var(--sp-text-muted);
  }
  .q {
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .fail-msg {
    color: var(--sp-danger);
    overflow-wrap: anywhere;
  }
  .when {
    text-align: right;
    flex: none;
  }
</style>
