<script>
  // Public access to a one-click database: lets database tools such as
  // DBeaver or pgAdmin connect from the internet through SimplePanel's
  // proxy (allowlist, TLS, connection limits). Off by default.
  import { api } from '../lib/api.js';
  import { ui } from '../lib/state.svelte.js';
  import { copyText } from '../lib/clipboard.js';
  import Icon from './Icon.svelte';
  import Modal from './Modal.svelte';

  let { aid, db, pub, onchange } = $props();

  const TOOLS = {
    postgres: 'pgAdmin, DBeaver, TablePlus or psql',
    mysql: 'MySQL Workbench, DBeaver, TablePlus or the mysql client',
    mariadb: 'HeidiSQL, DBeaver, TablePlus or the mariadb client',
    mongo: 'MongoDB Compass, Studio 3T or mongosh',
    redis: 'RedisInsight, Another Redis Desktop Manager or redis-cli',
  };

  let reveal = $state(false);
  let copied = $state('');
  let open = $state(false);
  let busy = $state('');
  let err = $state('');
  let msg = $state('');

  // Dialog state.
  let port = $state('');
  let who = $state('me'); // me | any | list
  let list = $state('');
  let requireTls = $state(true);
  let portState = $state({ ok: true, message: '' });
  let pwMsg = $state('');
  let checkTimer;

  let host = $derived(pub.host || location.hostname);
  let pw = $derived(db.password || '<password>');
  let engine = $derived(db.engine);

  function enc(s) {
    return encodeURIComponent(s);
  }
  // The URL a database tool uses from the internet. TLS is always offered,
  // so the URL asks for it.
  function publicUrl(password) {
    const auth = `${enc(db.user)}:${password === '<password>' ? password : enc(password)}@${host}:${pub.port}`;
    switch (engine) {
      case 'postgres':
        return `postgres://${auth}/${db.database}?sslmode=require`;
      case 'mysql':
      case 'mariadb':
        return `mysql://${auth}/${db.database}?ssl-mode=REQUIRED`;
      case 'mongo':
        return `mongodb://${auth}/${db.database}?authSource=admin&tls=true&tlsAllowInvalidCertificates=true&directConnection=true`;
      case 'redis':
        return `rediss://${auth}/${db.database || 0}`;
    }
    return '';
  }
  function command(password) {
    switch (engine) {
      case 'postgres':
        return `psql "${publicUrl(password)}"`;
      case 'mysql':
        return `mysql --ssl-mode=REQUIRED -h ${host} -P ${pub.port} -u ${db.user} -p ${db.database}`;
      case 'mariadb':
        return `mariadb --ssl -h ${host} -P ${pub.port} -u ${db.user} -p ${db.database}`;
      case 'mongo':
        return `mongosh "${publicUrl(password)}"`;
      case 'redis':
        return `redis-cli --tls --insecure -u ${publicUrl(password)}`;
    }
    return '';
  }
  const mask = (s) => s.replace(`:${enc(db.password)}@`, ':••••••@');
  let url = $derived(pub.enabled ? publicUrl(pw) : '');
  let cmd = $derived(pub.enabled ? command(pw) : '');

  let allowText = $derived.by(() => {
    if (!pub.allow?.length) return 'Anyone who has the password';
    if (pub.allow.length === 1 && pub.allow[0] === pub.yourIp) return `Only your IP address (${pub.yourIp})`;
    return pub.allow.join(', ');
  });

  async function copy(text, key) {
    await copyText(text);
    copied = key;
    setTimeout(() => copied === key && (copied = ''), 1500);
  }

  function startEdit() {
    err = msg = pwMsg = '';
    port = String(pub.enabled ? pub.port : pub.suggestedPort || db.port);
    requireTls = pub.enabled ? pub.requireTls : true;
    const allow = pub.allow || [];
    if (!pub.enabled && pub.yourIp) {
      who = 'me';
      list = allow.join('\n');
    } else if (!allow.length) {
      who = 'any';
      list = '';
    } else if (allow.length === 1 && allow[0] === pub.yourIp) {
      who = 'me';
      list = '';
    } else {
      who = 'list';
      list = allow.join('\n');
    }
    portState = { ok: true, message: '' };
    open = true;
    checkPort();
  }

  function checkPort() {
    clearTimeout(checkTimer);
    const n = Number(port);
    if (pub.enabled && n === pub.port) {
      portState = { ok: true, message: '' };
      return;
    }
    if (!Number.isInteger(n) || n < 1024 || n > 65535) {
      portState = { ok: false, message: 'Choose a port between 1024 and 65535.' };
      return;
    }
    portState = { ok: true, message: 'Checking…' };
    checkTimer = setTimeout(async () => {
      try {
        const r = await api.get(`/apps/${aid}/public/port?port=${n}`);
        if (Number(port) !== n) return;
        portState = r.ok ? { ok: true, message: `Port ${n} is free.` } : { ok: false, message: r.message[0].toUpperCase() + r.message.slice(1) + '.' };
      } catch (e) {
        portState = { ok: false, message: e.message };
      }
    }, 300);
  }

  async function save(e) {
    e.preventDefault();
    err = '';
    busy = 'save';
    let allow = '';
    if (who === 'me') allow = pub.yourIp;
    else if (who === 'list') allow = list;
    try {
      await api.put(`/apps/${aid}/public`, { enabled: true, port: Number(port), allow, requireTls });
      open = false;
      msg = '';
      await onchange();
    } catch (e2) {
      err = e2.message;
    } finally {
      busy = '';
    }
  }

  async function turnOff() {
    err = msg = '';
    busy = 'off';
    try {
      await api.put(`/apps/${aid}/public`, { enabled: false });
      msg = 'Public access is off. Open connections were closed.';
      await onchange();
    } catch (e) {
      err = e.message;
    } finally {
      busy = '';
    }
  }

  async function newPassword() {
    pwMsg = err = '';
    busy = 'pw';
    try {
      const r = await api.post(`/apps/${aid}/password`);
      pwMsg = r.updated?.length
        ? `New password set. Updated and redeploying: ${r.updated.join(', ')}.`
        : 'New password set. No apps in this project used the old one.';
      await onchange();
    } catch (e) {
      err = e.message;
    } finally {
      busy = '';
    }
  }

  // Advanced view: recent connections from the proxy's log.
  let entries = $state(null);
  async function loadLog() {
    busy = 'log';
    try {
      entries = (await api.get(`/apps/${aid}/public/log`)).entries.reverse();
    } catch (e) {
      err = e.message;
    } finally {
      busy = '';
    }
  }
  const when = (t) => new Date(t).toLocaleString([], { dateStyle: 'short', timeStyle: 'medium' });
</script>

<section class="card" class:live={pub.enabled}>
  <div class="row between">
    <h3><Icon name="globe" size={16} /> Public access</h3>
    <span class="pill" class:public={pub.enabled}>{pub.enabled ? `On · port ${pub.port}` : 'Off'}</span>
  </div>

  {#if !pub.enabled}
    <p class="muted">
      Only apps in this project can reach this database. Turn this on to connect from your own computer with tools such as {TOOLS[engine] || 'your database tools'}.
    </p>
    {#if msg}<p class="ok">{msg}</p>{/if}
    {#if err}<p class="error">{err}</p>{/if}
    {#if pub.canChange}
      <div><button class="btn" onclick={startEdit}>Allow connections from the internet…</button></div>
    {:else}
      <small class="faint">Only owners and admins can open a database to the internet.</small>
    {/if}
  {:else}
    {#if pub.state === 'failing'}
      <p class="error">{pub.stateMessage}</p>
    {:else if pub.state === 'missing'}
      <p class="error">The public proxy isn't running. Open Change and save to start it again.</p>
    {/if}
    <p class="muted">Database tools can connect from the internet. Connections are encrypted{pub.requireTls ? '' : ' when the tool asks for it'}.</p>
    <dl class="conn">
      <dt>Host</dt>
      <dd>
        <code>{host}</code>
        <button class="btn ghost icon small" title="Copy host" aria-label="Copy host" onclick={() => copy(host, 'host')}><Icon name="copy" size={14} /></button>
        {#if copied === 'host'}<span class="ok">Copied</span>{/if}
      </dd>
      <dt>Port</dt>
      <dd>
        <code>{pub.port}</code>
        <button class="btn ghost icon small" title="Copy port" aria-label="Copy port" onclick={() => copy(String(pub.port), 'port')}><Icon name="copy" size={14} /></button>
        {#if copied === 'port'}<span class="ok">Copied</span>{/if}
      </dd>
      <dt>Allowed from</dt>
      <dd><span class="small">{allowText}</span></dd>
    </dl>
    <div class="field">
      <span>Connection URL</span>
      <div class="row tight">
        <input type="text" readonly class="mono" value={reveal || !db.password ? url : mask(url)} aria-label="Public connection URL" />
        {#if db.password}<button class="btn ghost icon" title={reveal ? 'Hide password' : 'Show password'} aria-label={reveal ? 'Hide password' : 'Show password'} onclick={() => (reveal = !reveal)}><Icon name="eye" size={16} /></button>{/if}
        <button class="btn" onclick={() => copy(url, 'url')}>{copied === 'url' ? 'Copied' : 'Copy'}</button>
      </div>
    </div>
    <div class="field">
      <span>Command line</span>
      <div class="row tight">
        <input type="text" readonly class="mono" value={reveal || !db.password ? cmd : mask(cmd)} aria-label="Command line" />
        <button class="btn" onclick={() => copy(cmd, 'cmd')}>{copied === 'cmd' ? 'Copied' : 'Copy'}</button>
      </div>
      <small>
        {#if engine === 'mysql' || engine === 'mariadb'}In a database tool, set SSL/TLS to Required.{:else if engine === 'postgres'}In a database tool, set SSL mode to Require.{:else}In a database tool, turn on TLS and allow the self-signed certificate (or trust the certificate under Advanced view).{/if}
      </small>
    </div>
    <small class="faint">Can't connect? Your hosting provider's firewall may also need port {pub.port} opened, for example a DigitalOcean Cloud Firewall or an AWS security group.</small>

    {#if ui.advanced}
      <div class="adv">
        <dl class="conn">
          <dt>Encryption</dt>
          <dd class="small">
            {pub.requireTls ? 'Required. Unencrypted connections are refused.' : 'Optional. Unencrypted connections are allowed.'}
            {#if pub.serverTls}The database encrypts with its own certificate.{/if}
          </dd>
          {#if pub.fingerprint}
            <dt>Certificate</dt>
            <dd class="fp">
              <code class="small">SHA-256 {pub.fingerprint}</code>
              <a class="btn ghost small" href="/api/apps/{aid}/public/cert" download><Icon name="download" size={14} /> Download</a>
            </dd>
          {/if}
          <dt>Limits</dt>
          <dd class="small">60 new connections a minute and 32 open connections per address.</dd>
        </dl>
        {#if pub.fingerprint}
          <small class="faint">
            To check the certificate instead of skipping the check, trust the downloaded file:
            {#if engine === 'postgres'}<code>sslmode=verify-ca&amp;sslrootcert=FILE</code>{:else if engine === 'mongo'}<code>tlsCAFile=FILE</code> in place of <code>tlsAllowInvalidCertificates=true</code>{:else}<code>--cacert FILE</code> in place of <code>--insecure</code>{/if}.
          </small>
        {/if}
        {#if pub.canChange}
          {#if entries === null}
            <div><button class="btn ghost small" disabled={busy === 'log'} onclick={loadLog}>Show recent connections</button></div>
          {:else}
            <div class="row between">
              <strong class="small">Recent connections</strong>
              <button class="btn ghost small" disabled={busy === 'log'} onclick={loadLog}><Icon name="refresh" size={14} /> Refresh</button>
            </div>
            {#if entries.length === 0}
              <small class="faint">No connections yet.</small>
            {:else}
              <ul class="log">
                {#each entries as e, i (i)}
                  <li class:bad={e.event !== 'connected'}>
                    <span class="faint">{when(e.time)}</span>
                    <code>{e.ip}</code>
                    <span>{e.event === 'connected' ? `Connected (${e.detail})` : e.event === 'refused' ? `Refused: ${e.detail}` : e.detail}</span>
                  </li>
                {/each}
              </ul>
            {/if}
          {/if}
        {/if}
      </div>
    {/if}

    {#if err}<p class="error">{err}</p>{/if}
    {#if pub.canChange}
      <div class="row end">
        <button class="btn" onclick={startEdit}>Change…</button>
        <button class="btn danger" disabled={busy === 'off'} onclick={turnOff}>{busy === 'off' ? 'Turning off…' : 'Turn off'}</button>
      </div>
    {/if}
  {/if}
</section>

{#if open}
  <Modal title={pub.enabled ? 'Change public access' : 'Allow connections from the internet'} onclose={() => (open = false)}>
    <form class="dlg" onsubmit={save}>
      {#if !pub.enabled}
        <p class="notice">
          <strong>Anyone on the internet will be able to reach this database and try to sign in.</strong>
          Bots scan for open database ports all day, so keep the password strong and let in only the addresses you need.
          Apps in this project keep connecting privately, as before.
        </p>
      {/if}

      {#if pub.passwordWeak}
        <div class="pwbox">
          <p class="error">The password {pub.passwordWeak}. A database on the internet needs a strong password.</p>
          <div class="row">
            <button type="button" class="btn primary" disabled={!!busy} onclick={newPassword}><Icon name="key" size={14} /> {busy === 'pw' ? 'Changing…' : 'Generate a strong password'}</button>
          </div>
          <small class="faint">
            Sets a new random password now. Apps in this project that use the old one are updated and redeployed.{engine === 'redis' ? ' Redis restarts for a moment.' : ''}
          </small>
        </div>
      {:else}
        <p class="good"><Icon name="lock" size={14} /> The password is strong enough.</p>
      {/if}
      {#if pwMsg}<p class="ok">{pwMsg}</p>{/if}

      <label class="field">
        <span>Port</span>
        <input type="number" min="1024" max="65535" bind:value={port} oninput={checkPort} class="portin" required />
        {#if portState.message}<small class:okmsg={portState.ok} class:bad={!portState.ok}>{portState.message}</small>{/if}
        {#if !pub.enabled}<small>{db.port} is the usual port for this database, so tools fill it in by default. Any free port works.</small>{/if}
      </label>

      <div class="field">
        <span>Who can connect</span>
        <div class="choices" role="radiogroup" aria-label="Who can connect">
          {#if pub.yourIp}
            <label class="choice"><input type="radio" bind:group={who} value="me" /> <span><span><strong>Only my IP address</strong> <code>{pub.yourIp}</code></span><small>Safest. If your address changes, update it here.</small></span></label>
          {/if}
          <label class="choice"><input type="radio" bind:group={who} value="any" /> <span><strong>Anyone who has the password</strong><small>For changing or unknown addresses.</small></span></label>
          {#if ui.advanced || who === 'list'}
            <label class="choice"><input type="radio" bind:group={who} value="list" /> <span><strong>These addresses</strong><small>IP addresses or ranges such as 203.0.113.0/24, one per line.</small></span></label>
          {/if}
        </div>
        {#if who === 'list'}
          <textarea bind:value={list} rows="3" placeholder={'203.0.113.7\n198.51.100.0/24'} aria-label="Allowed addresses" spellcheck="false"></textarea>
        {/if}
      </div>

      {#if ui.advanced}
        <label class="check">
          <input type="checkbox" bind:checked={requireTls} />
          <span>Require encryption (TLS)<small>{pub.serverTls ? 'The database uses its own certificate.' : 'SimplePanel encrypts with a certificate made for this database.'} Turn off only for tools that can't use TLS.</small></span>
        </label>
      {/if}

      {#if err}<p class="error">{err}</p>{/if}
      <div class="row end">
        <button type="button" class="btn ghost" onclick={() => (open = false)}>Cancel</button>
        <button class="btn primary" disabled={!!busy || !!pub.passwordWeak || !portState.ok || (who === 'list' && !list.trim())}>
          {busy === 'save' ? 'Opening…' : pub.enabled ? 'Save' : `Open port ${port || ''}`}
        </button>
      </div>
    </form>
  </Modal>
{/if}

<style>
  .card {
    display: flex;
    flex-direction: column;
    gap: var(--sp-space-3);
  }
  .card.live {
    border-color: color-mix(in srgb, var(--sp-warning) 55%, var(--sp-border));
  }
  h3 {
    display: inline-flex;
    align-items: center;
    gap: var(--sp-space-2);
  }
  .row.between {
    justify-content: space-between;
    align-items: center;
  }
  .row.end {
    justify-content: flex-end;
    align-items: center;
  }
  .row.tight {
    flex-wrap: nowrap;
    align-items: center;
  }
  .row.tight input {
    flex: 1;
  }
  .pill {
    font-size: var(--sp-text-xs);
    font-weight: var(--sp-weight-bold);
    padding: 2px var(--sp-space-2);
    border-radius: var(--sp-radius-round);
    background: var(--sp-bg-main);
    color: var(--sp-text-muted);
    border: 1px solid var(--sp-border);
    white-space: nowrap;
  }
  .pill.public {
    color: var(--sp-warning);
    border-color: var(--sp-warning);
  }
  .conn {
    display: grid;
    grid-template-columns: max-content minmax(0, 1fr);
    gap: var(--sp-space-2) var(--sp-space-4);
    margin: 0;
    align-items: center;
  }
  .conn dt {
    color: var(--sp-text-muted);
    font-size: var(--sp-text-sm);
  }
  .conn dd {
    margin: 0;
    display: flex;
    align-items: center;
    gap: var(--sp-space-2);
    min-width: 0;
    flex-wrap: wrap;
  }
  .conn code {
    overflow-wrap: anywhere;
  }
  .small {
    font-size: var(--sp-text-sm);
  }
  .ok {
    color: var(--sp-success);
    font-size: var(--sp-text-sm);
    margin: 0;
  }
  .adv {
    display: flex;
    flex-direction: column;
    gap: var(--sp-space-3);
    padding-top: var(--sp-space-3);
    border-top: 1px solid var(--sp-border);
  }
  .fp code {
    font-size: var(--sp-text-xs);
  }
  .log {
    list-style: none;
    margin: 0;
    padding: var(--sp-space-2) var(--sp-space-3);
    background: var(--sp-bg-main);
    border-radius: var(--sp-radius-md);
    font-size: var(--sp-text-xs);
    max-height: 200px;
    overflow: auto;
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  .log li {
    display: flex;
    gap: var(--sp-space-2);
    flex-wrap: wrap;
  }
  .log li.bad span:last-child {
    color: var(--sp-danger);
  }
  .dlg {
    display: flex;
    flex-direction: column;
    gap: var(--sp-space-4);
  }
  .dlg .notice {
    margin: 0;
    display: flex;
    flex-direction: column;
    gap: var(--sp-space-1);
  }
  .pwbox {
    display: flex;
    flex-direction: column;
    gap: var(--sp-space-2);
  }
  .pwbox .error {
    margin: 0;
  }
  .good {
    margin: 0;
    display: inline-flex;
    align-items: center;
    gap: var(--sp-space-2);
    color: var(--sp-success);
    font-size: var(--sp-text-sm);
  }
  .portin {
    max-width: 140px;
  }
  small.okmsg {
    color: var(--sp-success);
  }
  small.bad {
    color: var(--sp-danger);
  }
  .choices {
    display: flex;
    flex-direction: column;
    gap: var(--sp-space-2);
  }
  .choice,
  .check {
    display: flex;
    align-items: flex-start;
    gap: var(--sp-space-2);
    padding: var(--sp-space-2) var(--sp-space-3);
    border: 1px solid var(--sp-border);
    border-radius: var(--sp-radius-md);
    background: var(--sp-bg-main);
    cursor: pointer;
    font-size: var(--sp-text-sm);
  }
  .choice:has(input:checked) {
    border-color: var(--sp-accent);
  }
  .choice input,
  .check input {
    margin-top: 3px;
  }
  .choice span,
  .check span {
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  .choice small,
  .check small {
    color: var(--sp-text-faint);
    font-size: var(--sp-text-xs);
  }
  .choice code {
    font-size: var(--sp-text-xs);
  }
  @media (max-width: 560px) {
    .row.tight {
      flex-wrap: wrap;
    }
    .conn {
      grid-template-columns: minmax(0, 1fr);
      gap: var(--sp-space-1);
    }
    .conn dd {
      margin-bottom: var(--sp-space-2);
    }
  }
</style>
