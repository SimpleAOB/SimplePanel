<script>
  import { copyText } from '../../lib/clipboard.js';
  // Settings section: REST API tokens for scripts, CI and the `sp` CLI.
  // Tokens made here carry API access; AI (MCP) tokens do not, so an AI
  // can never use the REST API to read secrets.
  import { onMount } from 'svelte';
  import { api } from '../../lib/api.js';
  import { timeAgo, data } from '../../lib/state.svelte.js';
  import Icon from '../Icon.svelte';

  const scopes = [
    ['read', 'Read-only', 'List projects and services, read logs, deployments and status.'],
    ['manage', 'Manage', 'Read-only, plus deploy, restart, change environment variables and run cron jobs.'],
    ['full', 'Full', 'Manage, plus delete services and projects and restore backups.'],
  ];
  const scopeLabel = Object.fromEntries(scopes.map(([k, l]) => [k, l]));
  const repo = 'https://github.com/SimpleAOB/SimplePanel';

  let tokens = $state([]);
  let name = $state('');
  let scope = $state('manage');
  let created = $state(null);
  let err = $state('');
  let copied = $state('');
  let tab = $state('cli');
  let panelDomain = $state('');

  let base = $derived(panelDomain ? `https://${panelDomain}` : location.origin);
  let secret = $derived(created?.secret || '<your token>');
  let insecure = $derived(base.startsWith('http://') && !['localhost', '127.0.0.1'].includes(location.hostname));
  let examples = $derived({
    cli: `# Install (Linux x86-64; see the release page for macOS, ARM and Windows)
curl -fsSL -o sp ${repo}/releases/latest/download/sp-linux-amd64
chmod +x sp && sudo mv sp /usr/local/bin/

sp login ${base}
sp services
sp deploy myproject/web --wait
sp logs myproject/web -f
sp env set myproject/web LOG_LEVEL=debug --deploy`,
    curl: `curl -H "Authorization: Bearer ${secret}" \\
  ${base}/api/projects

curl -X POST -H "Authorization: Bearer ${secret}" \\
  ${base}/api/apps/<id>/deploy`,
    ci: `# GitHub Actions: store the token as the SP_TOKEN secret
- name: Deploy
  env:
    SP_URL: ${base}
    SP_TOKEN: \${{ secrets.SP_TOKEN }}
  run: |
    curl -fsSL -o sp ${repo}/releases/latest/download/sp-linux-amd64
    chmod +x sp
    ./sp deploy myproject/web --wait`,
  });

  let apiTokens = $derived(tokens.filter((t) => t.api));

  async function load() {
    tokens = await api.get('/tokens');
  }
  onMount(() => {
    load().catch((e) => (err = e.message));
    api.get('/system').then((s) => (panelDomain = s.panelDomain || '')).catch(() => {});
  });

  async function create(e) {
    e.preventDefault();
    err = '';
    try {
      const r = await api.post('/tokens', { name: name.trim(), scope, api: true });
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
  <h2>API &amp; CLI</h2>
  <p class="muted">
    Script SimplePanel from a terminal or your CI with the <code>sp</code> command-line tool or the REST API. Create a token, then use it as a
    Bearer token. A token can never do more than your own role allows, and it can't manage users, tokens or Git connections.
  </p>

  <form class="create" onsubmit={create}>
    <label class="field">
      <span>Token name</span>
      <input type="text" bind:value={name} placeholder="e.g. GitHub Actions" maxlength="60" required />
    </label>
    <div class="field">
      <span>Access</span>
      <div class="segmented" role="radiogroup" aria-label="Access">
        {#each scopes as [key, label]}
          <button type="button" role="radio" aria-checked={scope === key} class:active={scope === key} onclick={() => (scope = key)}>{label}</button>
        {/each}
      </div>
      <small>{scopes.find((s) => s[0] === scope)[2]}</small>
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
      <h3>Examples</h3>
      <div class="segmented" role="tablist" aria-label="Example">
        <button type="button" role="tab" aria-selected={tab === 'cli'} class:active={tab === 'cli'} onclick={() => (tab = 'cli')}>CLI</button>
        <button type="button" role="tab" aria-selected={tab === 'curl'} class:active={tab === 'curl'} onclick={() => (tab = 'curl')}>curl</button>
        <button type="button" role="tab" aria-selected={tab === 'ci'} class:active={tab === 'ci'} onclick={() => (tab = 'ci')}>CI</button>
      </div>
    </div>
    <pre class="code">{examples[tab]}</pre>
    <div class="row">
      <button class="btn small" onclick={() => copy(examples[tab], 'ex')}><Icon name="copy" size={14} /> {copied === 'ex' ? 'Copied' : 'Copy'}</button>
      <a class="btn ghost small" href="{repo}/blob/main/docs/API.md" target="_blank" rel="noopener">API reference</a>
    </div>
    {#if tab === 'cli'}<small class="faint"><code>sp login</code> asks for the token. In CI, set <code>SP_URL</code> and <code>SP_TOKEN</code> instead.</small>{/if}
    {#if insecure}
      <p class="warn">This panel is served over plain HTTP, so tokens would travel unencrypted. Set a panel domain under General to use HTTPS first.</p>
    {/if}
  </div>

  <h3>API tokens</h3>
  {#if apiTokens.length === 0}
    <p class="muted small">No API tokens yet. Tokens for AI assistants are under Connect AI.</p>
  {:else}
    <ul class="list">
      {#each apiTokens as t (t.id)}
        <li>
          <div class="grow">
            <div class="name">{t.name}</div>
            <div class="faint small">{#if t.username && t.username !== data.user?.username}{t.username} · {/if}Created {timeAgo(t.createdAt)} · {t.lastUsedAt ? `Last used ${timeAgo(t.lastUsedAt)}` : 'Never used'}</div>
          </div>
          <span class="scope {t.scope}">{scopeLabel[t.scope] || t.scope}</span>
          <button class="btn ghost small" onclick={() => revoke(t)}>Revoke</button>
        </li>
      {/each}
    </ul>
  {/if}
</section>

<style>
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
</style>
