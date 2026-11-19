<script>
  import { copyText } from '../lib/clipboard.js';
  // Overview card for apps built from Git: where the code comes from,
  // deploy on push, pull request previews, the webhook URL and deploy key.
  import { onMount } from 'svelte';
  import { api } from '../lib/api.js';
  import { ui, go, timeAgo, isAdmin } from '../lib/state.svelte.js';
  import { formFromApp, payload } from '../lib/appform.js';
  import Icon from './Icon.svelte';

  let { detail, aid, pid, canDeploy, onchange, onrotate } = $props();

  let app = $derived(detail.app);
  let linked = $derived(!!detail.provider);
  let previews = $state([]);
  let busy = $state('');
  let err = $state('');
  let copied = $state('');
  // Form value: starts from the saved overrides. The card is keyed by app.
  let previewEnv = $state((() => detail.app.previewEnv || '')());
  let keyUrl = $state('');
  let webhook = $derived(location.origin + (detail.webhookPath || ''));
  const kindLabel = { github: 'GitHub', gitlab: 'GitLab' };

  async function loadPreviews() {
    try {
      previews = await api.get(`/apps/${aid}/previews`);
    } catch {
      previews = [];
    }
  }
  onMount(() => {
    loadPreviews();
    const t = setInterval(loadPreviews, 10000);
    return () => clearInterval(t);
  });

  async function save(fields, key) {
    busy = key;
    err = '';
    try {
      await api.put(`/apps/${aid}`, { ...payload(formFromApp(app)), ...fields });
      await onchange();
    } catch (e) {
      err = e.message;
    } finally {
      busy = '';
    }
  }

  async function removePreview(p) {
    busy = 'p' + p.id;
    try {
      await api.del(`/apps/${p.id}`);
      await loadPreviews();
      await onchange();
    } catch (e) {
      err = e.message;
    } finally {
      busy = '';
    }
  }

  async function makeKey() {
    busy = 'key';
    err = '';
    try {
      const r = await api.post(`/apps/${aid}/deploy-key`);
      keyUrl = r.addUrl;
      await onchange();
    } catch (e) {
      err = e.message;
    } finally {
      busy = '';
    }
  }

  async function dropKey() {
    busy = 'key';
    try {
      await api.del(`/apps/${aid}/deploy-key`);
      await onchange();
    } catch (e) {
      err = e.message;
    } finally {
      busy = '';
    }
  }

  async function copy(text, key) {
    try {
      await copyText(text);
      copied = key;
      setTimeout(() => (copied = ''), 1500);
    } catch {
      /* clipboard unavailable */
    }
  }
</script>

<section class="card">
  <div class="row between">
    <h3>Git</h3>
    {#if linked}
      <span class="pill on"><Icon name="plug" size={12} /> {kindLabel[detail.provider.kind]}</span>
    {/if}
  </div>
  {#if err}<p class="error">{err}</p>{/if}

  {#if linked}
    <p class="muted">
      Connected to <a href={detail.repoUrl} target="_blank" rel="noopener noreferrer" class="mono">{app.gitRepo}</a>
      through {detail.provider.name}. Branch <code>{app.gitBranch || 'default'}</code>.
    </p>
  {/if}

  <div class="toggles">
    {#if linked}
      <label class="toggle-row">
        <span>
          <strong>Deploy on push</strong>
          <small class="faint">Every push to <code>{app.gitBranch || 'the default branch'}</code> deploys, and the commit shows the deploy's status.</small>
        </span>
        <input type="checkbox" checked={app.autoDeploy} disabled={!canDeploy || busy === 'auto'} onchange={(e) => save({ autoDeploy: e.currentTarget.checked }, 'auto')} />
      </label>
    {/if}
    <label class="toggle-row">
      <span>
        <strong>Pull request previews</strong>
        <small class="faint">
          Each pull request into <code>{app.gitBranch || 'the default branch'}</code> gets its own copy of this app at its own address, updated on every push and removed when the pull request closes.
          Pull requests from forks are skipped.
        </small>
      </span>
      <input type="checkbox" checked={app.previews} disabled={!canDeploy || busy === 'prev'} onchange={(e) => save({ previews: e.currentTarget.checked }, 'prev')} />
    </label>
  </div>

  {#if app.previews && !detail.wildcard}
    <p class="warn">Previews need a wildcard domain for their addresses. Set one under <a href="#/settings/general">Settings → General</a>.</p>
  {/if}
  {#if app.previews && !linked}
    <p class="faint small">Without a GitHub or GitLab connection, add the webhook URL below to the repository with pull request (merge request) events turned on.</p>
  {/if}

  {#if previews.length}
    <ul class="previews">
      {#each previews as p (p.id)}
        <li>
          <span class="dot {p.status.state}"></span>
          <button class="linkish" onclick={() => go(`/p/${pid}/a/${p.id}`)}>
            <strong>#{p.prNumber}</strong> {p.prTitle || p.branch}
          </button>
          <span class="faint small mono">{p.branch}</span>
          {#if p.url}<a href={p.url} target="_blank" rel="noopener noreferrer" class="small">Open</a>{/if}
          {#if p.prUrl}<a href={p.prUrl} target="_blank" rel="noopener noreferrer" class="small faint">PR</a>{/if}
          {#if canDeploy}<button class="btn ghost small" disabled={busy === 'p' + p.id} onclick={() => removePreview(p)}>Remove</button>{/if}
        </li>
      {/each}
    </ul>
  {/if}

  {#if !linked && canDeploy}
    <div class="field">
      <span>Deploy on every push</span>
      <div class="row tight">
        <input type="text" readonly value={webhook} class="mono" aria-label="Webhook URL" />
        <button class="btn" onclick={() => copy(webhook, 'hook')}>{copied === 'hook' ? 'Copied' : 'Copy'}</button>
        <button class="btn" disabled={!!busy} onclick={onrotate}>Regenerate</button>
      </div>
      <small class="faint">
        {#if isAdmin()}<a href="#/settings/git">Connect GitHub or GitLab</a> to set this up for you, or paste{:else}Paste{/if} this webhook URL into the repository's webhook settings (GitHub: content type <code>application/json</code>, push and pull request events) or POST to it from CI. Keep it secret.
      </small>
    </div>
  {/if}

  {#if ui.advanced && canDeploy}
    {#if app.previews}
      <div class="field">
        <span>Preview environment overrides</span>
        <textarea rows="3" bind:value={previewEnv} spellcheck="false" placeholder="DATABASE_URL=postgres://…/shop_preview&#10;APP_ENV=preview"></textarea>
        <div class="row end">
          <small class="faint grow">Previews get this app's variables, with these lines replacing or adding to them.</small>
          <button class="btn small" disabled={busy === 'penv'} onclick={() => save({ previewEnv }, 'penv')}>Save</button>
        </div>
      </div>
    {/if}

    {#if !linked}
      <div class="field">
        <span>Deploy key</span>
        {#if app.deployKeyPub}
          <div class="row tight">
            <input type="text" readonly value={app.deployKeyPub} class="mono" aria-label="Public deploy key" />
            <button class="btn" onclick={() => copy(app.deployKeyPub, 'key')}>{copied === 'key' ? 'Copied' : 'Copy'}</button>
            <button class="btn ghost" disabled={busy === 'key'} onclick={dropKey}>Remove</button>
          </div>
          <small class="faint">
            Add this public key to the repository as a read-only deploy key{#if keyUrl || detail.deployKeyUrl} (<a href={keyUrl || detail.deployKeyUrl} target="_blank" rel="noopener noreferrer">open the repository's deploy keys</a>){/if},
            and use the SSH URL (<code>git@…</code>) in Settings. The private key never leaves this server.
          </small>
        {:else}
          <div><button class="btn small" disabled={busy === 'key'} onclick={makeKey}><Icon name="key" size={14} /> Create deploy key</button></div>
          <small class="faint">For private repositories without an access token: SimplePanel clones over SSH with a key only this app uses.</small>
        {/if}
      </div>
    {/if}
  {/if}
</section>

<style>
  .card {
    display: flex;
    flex-direction: column;
    gap: var(--sp-space-3);
  }
  .between {
    justify-content: space-between;
    align-items: center;
  }
  .pill {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    font-size: var(--sp-text-xs);
    font-weight: var(--sp-weight-bold);
    padding: 2px var(--sp-space-2);
    border-radius: var(--sp-radius-round);
    border: 1px solid var(--sp-border);
    color: var(--sp-text-muted);
  }
  .pill.on {
    color: var(--sp-success);
    border-color: var(--sp-success);
  }
  .toggles {
    display: flex;
    flex-direction: column;
    border: 1px solid var(--sp-border);
    border-radius: var(--sp-radius-md);
  }
  .toggle-row {
    display: flex;
    align-items: center;
    gap: var(--sp-space-4);
    padding: var(--sp-space-3);
    cursor: pointer;
  }
  .toggle-row + .toggle-row {
    border-top: 1px solid var(--sp-border);
  }
  .toggle-row > span {
    flex: 1;
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  .toggle-row input {
    width: 18px;
    height: 18px;
    accent-color: var(--sp-accent);
    flex: none;
  }
  .warn {
    color: var(--sp-warning, var(--sp-danger));
    font-size: var(--sp-text-sm);
  }
  .previews {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: var(--sp-space-1);
  }
  .previews li {
    display: flex;
    align-items: center;
    gap: var(--sp-space-3);
    padding: var(--sp-space-2) var(--sp-space-3);
    border: 1px solid var(--sp-border);
    border-radius: var(--sp-radius-md);
    background: var(--sp-bg-main);
    flex-wrap: wrap;
  }
  .linkish {
    flex: 1;
    min-width: 0;
    border: 0;
    background: none;
    color: var(--sp-text);
    font: inherit;
    text-align: left;
    cursor: pointer;
    padding: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .linkish:hover {
    text-decoration: underline;
  }
  .row.tight {
    flex-wrap: nowrap;
    align-items: center;
  }
  .row.tight input {
    flex: 1;
  }
  .end {
    align-items: center;
  }
  .grow {
    flex: 1;
  }
  .small {
    font-size: var(--sp-text-sm);
  }
  @media (max-width: 560px) {
    .row.tight {
      flex-wrap: wrap;
    }
  }
</style>
