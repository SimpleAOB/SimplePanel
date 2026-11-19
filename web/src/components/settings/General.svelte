<script>
  import { onMount } from 'svelte';
  import { api } from '../../lib/api.js';
  import Updates from './Updates.svelte';

  let panelDomain = $state('');
  let wildcard = $state('');
  let msg = $state('');
  let err = $state('');
  let wmsg = $state('');
  let werr = $state('');
  let ip = $state('');
  let savedDomain = $state('');
  let httpsOnly = $state(false);
  let viaHttps = $state(false);
  let hmsg = $state('');
  let herr = $state('');

  onMount(async () => {
    const s = await api.get('/system');
    panelDomain = s.panelDomain || '';
    savedDomain = panelDomain;
    httpsOnly = !!s.httpsOnly;
    viaHttps = !!s.viaHttps;
    wildcard = s.wildcardDomain || '';
    ip = location.hostname;
  });

  async function saveDomain(e) {
    e.preventDefault();
    msg = err = '';
    try {
      const r = await api.put('/settings', { panelDomain });
      savedDomain = panelDomain;
      if (r.httpsOnly === false) httpsOnly = false;
      msg = panelDomain
        ? `Saved. Once DNS for ${panelDomain} points at this server, open https://${panelDomain}.`
        : 'Saved. The panel is only reachable on port 3000.';
    } catch (e2) {
      err = e2.message;
    }
  }

  async function setHttpsOnly(on) {
    hmsg = herr = '';
    try {
      await api.put('/settings', { httpsOnly: on });
      httpsOnly = on;
      hmsg = on ? `Plain HTTP is off. Use https://${savedDomain} from now on.` : 'Plain HTTP on port 3000 works again.';
    } catch (e2) {
      herr = e2.message;
    }
  }

  async function saveWildcard(e) {
    e.preventDefault();
    wmsg = werr = '';
    try {
      const r = await api.put('/settings', { wildcardDomain: wildcard });
      wildcard = r.wildcardDomain ?? wildcard;
      wmsg = wildcard
        ? `Saved. Apps now get addresses like web-shop.${wildcard}.`
        : 'Saved. Automatic subdomains are off.';
    } catch (e2) {
      werr = e2.message;
    }
  }
</script>

<Updates />

<form class="card" onsubmit={saveDomain}>
  <h2>Panel domain</h2>
  <p class="muted">Serve SimplePanel itself over HTTPS on your own domain. Point an A record at this server first.</p>
  <div class="row">
    <label class="field">
      <span>Domain</span>
      <input type="text" bind:value={panelDomain} placeholder="panel.example.com" />
    </label>
    <button class="btn primary">Save</button>
  </div>
  {#if msg}<p class="ok">{msg}</p>{/if}
  {#if err}<p class="error">{err}</p>{/if}
  {#if savedDomain}
    <div class="https">
      <label class="check">
        <input type="checkbox" checked={httpsOnly} disabled={!httpsOnly && !viaHttps} onchange={(e) => setHttpsOnly(e.currentTarget.checked)} />
        HTTPS only: turn off plain HTTP on port 3000
      </label>
      <small class="faint">
        {#if httpsOnly}
          Passwords and API tokens only travel encrypted. Visits to port 3000 are sent to https://{savedDomain}. Locked out?
          An SSH tunnel to port 3000 still works, and clearing the panel domain turns this off.
        {:else if viaHttps}
          Recommended: sign-ins and AI/API tokens on port 3000 travel unencrypted. You can then close port 3000 in your firewall too.
        {:else}
          Open <a href="https://{savedDomain}/#/settings/general">https://{savedDomain}</a> to turn this on. That proves the HTTPS address works, so you can't lock yourself out.
        {/if}
      </small>
      {#if hmsg}<p class="ok">{hmsg}</p>{/if}
      {#if herr}<p class="error">{herr}</p>{/if}
    </div>
  {/if}
</form>

<form class="card" onsubmit={saveWildcard}>
  <h2>Automatic subdomains</h2>
  <p class="muted">
    Set a domain once and every app gets its own HTTPS address, <code>app-project.yourdomain</code>, without adding domains one by one.
    You can turn it off for single apps on their Overview tab.
  </p>
  <div class="row">
    <label class="field">
      <span>Wildcard domain</span>
      <input type="text" bind:value={wildcard} placeholder="apps.example.com" />
    </label>
    <button class="btn primary">Save</button>
  </div>
  <small class="faint">
    Add a DNS record <code>*.{wildcard || 'apps.example.com'}</code> of type A pointing at this server{ip ? ` (${ip})` : ''}.
    Certificates are issued for each subdomain the first time it's used. Leave empty to turn this off.
  </small>
  {#if wmsg}<p class="ok">{wmsg}</p>{/if}
  {#if werr}<p class="error">{werr}</p>{/if}
</form>

<style>
  .https {
    display: flex;
    flex-direction: column;
    gap: var(--sp-space-1);
    margin-top: var(--sp-space-3);
    padding-top: var(--sp-space-3);
    border-top: 1px solid var(--sp-border);
  }
  .check {
    display: flex;
    gap: var(--sp-space-2);
    align-items: center;
    font-size: var(--sp-text-sm);
  }
</style>
