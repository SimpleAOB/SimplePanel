<script>
  // Settings → Git: connect GitHub (a GitHub App this server creates for
  // itself) and GitLab (an access token).
  import { onMount } from 'svelte';
  import { api } from '../../lib/api.js';
  import { ui, timeAgo } from '../../lib/state.svelte.js';
  import Icon from '../Icon.svelte';

  let providers = $state([]);
  let details = $state({}); // id -> { installations, error }
  let err = $state('');
  let loaded = $state(false);
  let ghName = $state('');
  let ghOrg = $state('');
  let ghBusy = $state(false);
  let publicUrl = $state(location.origin);
  // localhost and private addresses can't be reached from GitHub at all.
  let unreachable = $derived.by(() => {
    const h = new URL(publicUrl).hostname;
    return h === 'localhost' || h.endsWith('.local') || /^(127\.|10\.|192\.168\.|172\.(1[6-9]|2\d|3[01])\.)/.test(h) || h === '[::1]';
  });
  let gl = $state({ baseUrl: 'https://gitlab.com', token: '', name: '' });
  let glBusy = $state(false);
  let glErr = $state('');
  let showGitLab = $state(false);
  let confirmDel = $state(0);
  let deleted = $state(null);
  let manifestForm = $state(null);
  let manifest = $state({ url: '', manifest: '' });

  const callbackError = {
    expired: 'The GitHub connection took too long or was started in another browser. Try again.',
    code: 'GitHub sent back an unexpected answer. Try again.',
    convert: "GitHub didn't hand over the new app's keys. Try again; if it keeps failing, check the server log.",
  };

  async function load() {
    try {
      providers = await api.get('/git/providers');
      for (const p of providers.filter((p) => p.kind === 'github')) {
        api.get(`/git/providers/${p.id}`).then((d) => (details[p.id] = d)).catch(() => {});
      }
    } catch (e) {
      err = e.message;
    } finally {
      loaded = true;
    }
  }

  onMount(async () => {
    const q = new URLSearchParams(location.hash.split('?')[1] || '');
    if (q.get('error')) err = callbackError[q.get('error')] || 'Connecting GitHub failed.';
    try {
      const s = await api.get('/system');
      if (s.panelDomain) publicUrl = 'https://' + s.panelDomain;
    } catch {
      /* keep origin */
    }
    const host = new URL(publicUrl).hostname.replace(/^www\./, '');
    ghName = ('SimplePanel ' + host).slice(0, 34);
    await load();
  });

  // GitHub's manifest flow needs a form POST from the browser to github.com.
  async function connectGitHub(e) {
    e.preventDefault();
    err = '';
    ghBusy = true;
    try {
      manifest = await api.post('/git/github/manifest', { name: ghName.trim(), org: ghOrg.trim() });
      await Promise.resolve();
      setTimeout(() => manifestForm.submit(), 0);
    } catch (e2) {
      err = e2.message;
      ghBusy = false;
    }
  }

  async function connectGitLab(e) {
    e.preventDefault();
    glErr = '';
    glBusy = true;
    try {
      await api.post('/git/gitlab', gl);
      gl = { baseUrl: 'https://gitlab.com', token: '', name: '' };
      showGitLab = false;
      await load();
    } catch (e2) {
      glErr = e2.message;
    } finally {
      glBusy = false;
    }
  }

  async function remove(p) {
    err = '';
    try {
      const r = await api.del(`/git/providers/${p.id}`);
      deleted = { ...p, deleteUrl: r.deleteUrl };
      confirmDel = 0;
      await load();
    } catch (e) {
      err = e.message;
    }
  }

  let insecure = $derived(publicUrl.startsWith('http://'));
</script>

<section class="card">
  <h2>Git</h2>
  <p class="muted">
    Connect GitHub or GitLab to pick repositories from a list, deploy on every push, show deploy results on commits, and give each pull request
    its own preview. Without a connection, apps can still use public repositories, an access token, a deploy key or a webhook URL.
  </p>
  {#if err}<p class="error">{err}</p>{/if}
  {#if deleted}
    <p class="ok">
      Disconnected {deleted.name}.
      {#if deleted.deleteUrl}The GitHub App still exists on GitHub: <a href={deleted.deleteUrl} target="_blank" rel="noopener noreferrer">open it</a> and delete it under Advanced if you don't need it.{/if}
    </p>
  {/if}

  {#if loaded && providers.length}
    <ul class="providers">
      {#each providers as p (p.id)}
        <li>
          <div class="head">
            <span class="logo">{p.kind === 'github' ? 'GH' : 'GL'}</span>
            <div class="grow">
              <div class="name">{p.name}</div>
              <div class="faint small">
                {p.kind === 'github' ? 'GitHub App' : 'GitLab'}{p.owner ? ` · ${p.owner}` : ''}{p.kind === 'gitlab' ? ` · ${p.baseUrl}` : ''} · {p.apps} app{p.apps === 1 ? '' : 's'} · connected {timeAgo(p.createdAt)}
              </div>
            </div>
            {#if confirmDel === p.id}
              <button class="btn danger small" onclick={() => remove(p)}>Disconnect</button>
              <button class="btn ghost small" onclick={() => (confirmDel = 0)}>Cancel</button>
            {:else}
              <button class="btn ghost small" onclick={() => (confirmDel = p.id)}>Disconnect</button>
            {/if}
          </div>
          {#if confirmDel === p.id && p.apps}
            <p class="warn">{p.apps} app{p.apps === 1 ? '' : 's'} use this connection. They keep their repository URL but stop deploying on push and can't clone private repositories until you connect them again.</p>
          {/if}
          {#if p.kind === 'github'}
            <div class="installs">
              {#if details[p.id]?.installations?.length}
                <span class="faint small">Installed on</span>
                {#each details[p.id].installations as i (i.id)}
                  <a class="chip" href={i.url} target="_blank" rel="noopener noreferrer">{i.account}</a>
                {/each}
              {:else if details[p.id]?.error}
                <span class="error small">{details[p.id].error}</span>
              {:else if details[p.id]}
                <span class="warn small">Not installed anywhere yet, so it can't see any repositories.</span>
              {/if}
              <a class="btn small" href={p.installUrl} target="_blank" rel="noopener noreferrer"><Icon name="plus" size={14} /> Install on more repositories</a>
            </div>
          {/if}
        </li>
      {/each}
    </ul>
  {/if}
</section>

<form class="card" onsubmit={connectGitHub}>
  <h3><span class="logo">GH</span> Connect GitHub</h3>
  <p class="muted">
    SimplePanel creates a private GitHub App that belongs to you, for this server only. You approve it on GitHub, pick the repositories it may
    read, and come back here. It can read code, and write commit statuses and pull request comments. Nothing goes through anyone else's servers.
  </p>
  <div class="row">
    <label class="field">
      <span>App name</span>
      <input type="text" bind:value={ghName} maxlength="34" required />
      <small>Must be unique on GitHub. You can rename it there later.</small>
    </label>
    {#if ui.advanced}
      <label class="field">
        <span>Organization (optional)</span>
        <input type="text" bind:value={ghOrg} placeholder="your-org" />
        <small>Create the app under an organization instead of your account. You must be an owner.</small>
      </label>
    {/if}
  </div>
  {#if unreachable}
    <p class="warn">
      GitHub can't reach <code>{publicUrl}</code>, so it won't be able to tell SimplePanel about pushes. Set a panel domain under <a href="#/settings/general">General</a> first.
    </p>
  {:else if insecure}
    <p class="warn">
      GitHub will send push events to <code>{publicUrl}</code>, which isn't HTTPS. Set a panel domain under <a href="#/settings/general">General</a> first if you can.
    </p>
  {:else}
    <small class="faint">GitHub sends events to <code>{publicUrl}/api/hooks/github</code>, so this address must be reachable from the internet.</small>
  {/if}
  <div><button class="btn primary" disabled={ghBusy}>{ghBusy ? 'Opening GitHub…' : 'Continue on GitHub'}</button></div>
</form>

<form bind:this={manifestForm} method="post" action={manifest.url} class="hidden" aria-hidden="true">
  <input type="hidden" name="manifest" value={manifest.manifest} />
</form>

<section class="card">
  <div class="row between">
    <h3><span class="logo">GL</span> Connect GitLab</h3>
    {#if !showGitLab}<button class="btn small" onclick={() => (showGitLab = true)}>Connect GitLab</button>{/if}
  </div>
  <p class="muted">For gitlab.com or your own GitLab. SimplePanel uses an access token to list projects, add a webhook to the projects you deploy, and report deploys on commits and merge requests.</p>
  {#if showGitLab}
    <form class="gl" onsubmit={connectGitLab}>
      <div class="row">
        <label class="field">
          <span>GitLab URL</span>
          <input type="url" bind:value={gl.baseUrl} required />
        </label>
        <label class="field">
          <span>Name (optional)</span>
          <input type="text" bind:value={gl.name} placeholder="e.g. Work GitLab" maxlength="60" />
        </label>
      </div>
      <label class="field">
        <span>Access token</span>
        <input type="password" bind:value={gl.token} autocomplete="off" placeholder="glpat-…" required />
        <small>A personal, group or project access token with the <code>api</code> scope, from a user with the Maintainer role. It is stored on this server and never shown again.
          {#if gl.baseUrl}<a href="{gl.baseUrl.replace(/\/+$/, '')}/-/user_settings/personal_access_tokens?name=SimplePanel&scopes=api" target="_blank" rel="noopener noreferrer">Create a token</a>{/if}</small>
      </label>
      {#if glErr}<p class="error">{glErr}</p>{/if}
      <div class="row end">
        <button type="button" class="btn ghost" onclick={() => (showGitLab = false)}>Cancel</button>
        <button class="btn primary" disabled={glBusy}>{glBusy ? 'Checking…' : 'Connect'}</button>
      </div>
    </form>
  {/if}
</section>

<style>
  .hidden {
    display: none;
  }
  h3 {
    display: flex;
    align-items: center;
    gap: var(--sp-space-2);
  }
  .logo {
    display: inline-grid;
    place-items: center;
    width: 28px;
    height: 28px;
    border-radius: var(--sp-radius-md);
    background: var(--sp-bg-active);
    font-size: var(--sp-text-xs);
    font-weight: var(--sp-weight-bold);
    flex: none;
  }
  .providers {
    list-style: none;
    margin: 0;
    padding: 0;
    border: 1px solid var(--sp-border);
    border-radius: var(--sp-radius-md);
  }
  .providers li {
    display: flex;
    flex-direction: column;
    gap: var(--sp-space-2);
    padding: var(--sp-space-3);
  }
  .providers li + li {
    border-top: 1px solid var(--sp-border);
  }
  .head {
    display: flex;
    align-items: center;
    gap: var(--sp-space-3);
  }
  .grow {
    flex: 1;
    min-width: 0;
  }
  .name {
    font-weight: var(--sp-weight-bold);
  }
  .small {
    font-size: var(--sp-text-sm);
  }
  .installs {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: var(--sp-space-2);
    padding-left: calc(28px + var(--sp-space-3));
  }
  .installs .btn {
    margin-left: auto;
  }
  .chip {
    font-size: var(--sp-text-sm);
    padding: 2px var(--sp-space-2);
    border-radius: var(--sp-radius-round);
    border: 1px solid var(--sp-border);
  }
  .warn {
    color: var(--sp-warning, var(--sp-danger));
    font-size: var(--sp-text-sm);
  }
  .gl {
    display: flex;
    flex-direction: column;
    gap: var(--sp-space-3);
  }
  .end {
    justify-content: flex-end;
  }
</style>
