<script>
  import { onMount } from 'svelte';
  import { api } from '../../lib/api.js';
  import Updates from './Updates.svelte';
  import DomainStatus from '../DomainStatus.svelte';

  let panelDomain = $state('');
  let wildcard = $state('');
  let msg = $state('');
  let err = $state('');
  let wmsg = $state('');
  let werr = $state('');
  let ip = $state('');
  let savedPanel = $state('');
  let savedWildcard = $state('');

  onMount(async () => {
    const s = await api.get('/system');
    panelDomain = s.panelDomain || '';
    wildcard = s.wildcardDomain || '';
    savedPanel = panelDomain;
    savedWildcard = wildcard;
    ip = s.serverIp || location.hostname;
  });

  async function saveDomain(e) {
    e.preventDefault();
    msg = err = '';
    try {
      await api.put('/settings', { panelDomain });
      savedPanel = panelDomain;
      msg = panelDomain
        ? `Saved. Once DNS for ${panelDomain} points at this server, open https://${panelDomain}.`
        : 'Saved. The panel is only reachable on port 3000.';
    } catch (e2) {
      err = e2.message;
    }
  }

  async function saveWildcard(e) {
    e.preventDefault();
    wmsg = werr = '';
    try {
      const r = await api.put('/settings', { wildcardDomain: wildcard });
      wildcard = r.wildcardDomain ?? wildcard;
      savedWildcard = wildcard;
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
  {#if savedPanel}{#key savedPanel}<DomainStatus host={savedPanel} serverIp={ip} />{/key}{/if}
  {#if msg}<p class="ok">{msg}</p>{/if}
  {#if err}<p class="error">{err}</p>{/if}
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
  {#if savedWildcard}{#key savedWildcard}<DomainStatus host={'check.' + savedWildcard} https={false} serverIp={ip} />{/key}{/if}
  {#if wmsg}<p class="ok">{wmsg}</p>{/if}
  {#if werr}<p class="error">{werr}</p>{/if}
</form>
