<script>
  // Source + runtime fields for an app. `value` is a plain object bound
  // by the parent. Advanced fields only show when Advanced view is on.
  import { onMount } from 'svelte';
  import { ui, isAdmin } from '../lib/state.svelte.js';
  import { applyStaticPreset } from '../lib/staticpresets.js';
  import { loadRegistries, registryHost } from '../lib/registries.js';
  import Icon from './Icon.svelte';
  import StaticSiteHelp from './StaticSiteHelp.svelte';
  import RepoPicker from './RepoPicker.svelte';

  // more: show this form's advanced fields without turning on Advanced
  // view everywhere.
  let { value = $bindable(), hasToken = false, showRuntime = true, database = false, more = $bindable(false) } = $props();
  let adv = $derived(ui.advanced || more);

  let helpOpen = $state(false);

  // Registry logins for private images. The login follows the image's
  // registry: change the image to another registry and the matching login
  // (or none) is picked, so a login is never sent to the wrong place.
  let registries = $state(null);
  onMount(() => {
    loadRegistries()
      .then((r) => (registries = r))
      .catch(() => (registries = []));
  });
  let host = $derived(registryHost(value.image));
  let matching = $derived((registries || []).filter((r) => r.server === host));
  let lastHost = registryHost(value.image);
  $effect(() => {
    if (value.source !== 'image' || database || registries === null) return;
    const h = host;
    const cur = registries.find((r) => r.id === value.registryId);
    const fits = cur && cur.server === h;
    if (value.registryId === null || (value.registryId && !fits) || (!value.registryId && h !== lastHost)) {
      const next = matching.length ? matching[0].id : 0;
      if (value.registryId !== next) value.registryId = next;
    }
    lastHost = h;
  });
  let imageTyped = $derived(/[/:]/.test(value.image || '') || (value.image || '').length > 3);

  let showStatic = $state(false);
  // Fill the build fields from a preset, and show them so the user can check.
  function usePreset(preset) {
    applyStaticPreset(value, preset);
    showStatic = true;
    helpOpen = false;
  }

  function addVolume() {
    value.volumes = [...value.volumes, { name: '', path: '' }];
  }
  function addPort() {
    value.ports = [...value.ports, { published: null, target: null, protocol: 'tcp' }];
  }
</script>

<div class="fields">
  {#if !database}
  <div class="field">
    <span>Source</span>
    <div class="segmented" role="radiogroup" aria-label="Source">
      <button type="button" role="radio" aria-checked={value.source === 'git'} class:active={value.source === 'git'} onclick={() => (value.source = 'git')}>Git repository</button>
      <button type="button" role="radio" aria-checked={value.source === 'image'} class:active={value.source === 'image'} onclick={() => (value.source = 'image')}>Docker image</button>
    </div>
  </div>
  {/if}

  {#if value.source === 'image'}
    <label class="field">
      <span>Image</span>
      <input type="text" bind:value={value.image} placeholder="nginx:latest or ghcr.io/user/app:tag" required />
      <small>{database ? 'Change the tag to switch versions. Major upgrades may need a manual data migration.' : 'Public or private images from Docker Hub, GHCR or any registry.'}</small>
    </label>
    {#if !database && imageTyped}
      {#if matching.length}
        <label class="field">
          <span class="with-icon"><Icon name="lock" size={14} /> Registry login</span>
          <select bind:value={value.registryId}>
            {#each matching as r (r.id)}
              <option value={r.id}>{r.name}</option>
            {/each}
            <option value={0}>None (public image)</option>
          </select>
          <small>
            {#if value.registryId}Every server signs in to <span class="mono">{host}</span> with this login to pull the image.{:else}Pulls from <span class="mono">{host}</span> without signing in.{/if}
          </small>
        </label>
      {:else if registries !== null}
        <small class="hint">
          Private image?
          {#if isAdmin()}<a href="#/settings/registries">Add a login for <span class="mono">{host}</span></a> under Settings › Registries.{:else}Ask an admin to add a login for <span class="mono">{host}</span> under Settings › Registries.{/if}
        </small>
      {/if}
    {/if}
  {:else}
    <RepoPicker bind:value />
    {#if !value.gitProviderId}
      <label class="field">
        <span>Branch <span class="opt">(optional)</span></span>
        <input type="text" bind:value={value.gitBranch} placeholder="The repository's default branch" spellcheck="false" />
      </label>
    {/if}
    <div class="field">
      <div class="label-row">
        <span>Build</span>
        {#if value.buildType !== 'dockerfile'}
          <button type="button" class="help-link" onclick={() => (helpOpen = true)}>
            <Icon name="help" size={14} /> How to set up a static site
          </button>
        {/if}
      </div>
      <div class="segmented" role="radiogroup" aria-label="Build type">
        {#each [['auto', 'Automatic'], ['dockerfile', 'Dockerfile'], ['static', 'Static site']] as [key, label]}
          <button type="button" role="radio" aria-checked={value.buildType === key} class:active={value.buildType === key} onclick={() => (value.buildType = key)}>{label}</button>
        {/each}
      </div>
      <small>
        {#if value.buildType === 'auto'}
          Uses the repository's Dockerfile if it has one. Without one, site generators like Vite or Hugo are built for you and plain files are served as a website. Apps that run a server need a Dockerfile.
        {:else if value.buildType === 'dockerfile'}
          Builds the Dockerfile in the repository.
        {:else}
          Serves HTML, CSS and JS files with a small nginx server on port 80.
          {#if !adv}Using Vite, Astro, Hugo or similar? <button type="button" class="inline-link" onclick={() => (helpOpen = true)}>Pick your tool</button> to add a build step.{/if}
        {/if}
      </small>
    </div>
    {#if adv}
      {#if !value.gitProviderId}
      <div class="row">
        <label class="field">
          <span>Access token</span>
          <input type="password" bind:value={value.gitToken} autocomplete="off" placeholder={hasToken ? 'saved (leave blank to keep)' : 'for private repositories'} />
        </label>
      </div>
      {/if}
      {#if hasToken && !value.gitProviderId}
        <label class="check"><input type="checkbox" bind:checked={value.clearGitToken} /> Remove saved access token</label>
      {/if}
      <div class="row">
        <label class="field">
          <span>Build path</span>
          <input type="text" bind:value={value.buildPath} placeholder="/ (repository root)" />
        </label>
        {#if value.buildType !== 'static'}
          <label class="field">
            <span>Dockerfile</span>
            <input type="text" bind:value={value.dockerfile} placeholder="Dockerfile" />
          </label>
        {/if}
      </div>
    {/if}
    {#if value.buildType !== 'dockerfile' && (adv || showStatic || (value.buildType === 'static' && (value.buildCommand || value.outputDir)))}
      <div class="static card">
        <h3 class="sub-title">Static site build {#if value.buildType === 'auto'}<span class="faint">(used when there's no Dockerfile)</span>{/if}</h3>
        <div class="row">
          <label class="field">
            <span>Build command</span>
            <input type="text" bind:value={value.buildCommand} placeholder="none: serve files as they are" class="mono" />
          </label>
          <label class="field">
            <span>Output directory</span>
            <input type="text" bind:value={value.outputDir} placeholder={value.buildCommand ? 'e.g. dist' : '/ (build path)'} />
          </label>
        </div>
        <label class="field">
          <span>Build image</span>
          <input type="text" bind:value={value.buildImage} placeholder="node:22-alpine" />
          <small>Runs the build command. Node dependencies install automatically. For Hugo use e.g. hugomods/hugo:exts with <code>hugo --minify</code> and output <code>public</code>.</small>
        </label>
        <label class="check"><input type="checkbox" bind:checked={value.spa} /> Single-page app: send unknown paths to index.html</label>
      </div>
    {/if}
  {/if}

  {#if showRuntime && adv}
    <h3 class="sub">Runtime</h3>
    <div class="row">
      {#if !database}
      <label class="field">
        <span>Replicas</span>
        <input type="number" min="0" max="50" bind:value={value.replicas} />
      </label>
      {/if}
      <label class="field">
        <span>CPU limit (cores)</span>
        <input type="number" min="0" step="0.1" bind:value={value.cpuLimit} placeholder="0 = no limit" />
      </label>
      <label class="field">
        <span>Memory limit (MB)</span>
        <input type="number" min="0" step="64" bind:value={value.memoryMb} placeholder="0 = no limit" />
      </label>
    </div>
    <label class="field">
      <span>Command override</span>
      <input type="text" bind:value={value.command} placeholder="leave blank to use the image's default" />
    </label>

    <div class="field">
      <span>Volumes</span>
      {#each value.volumes as v, i}
        <div class="row tight">
          <input type="text" bind:value={v.name} placeholder="name (e.g. data)" aria-label="Volume name" />
          <input type="text" bind:value={v.path} placeholder="/path/in/container" aria-label="Mount path" />
          <button type="button" class="btn ghost small" onclick={() => (value.volumes = value.volumes.filter((_, j) => j !== i))}>Remove</button>
        </div>
      {/each}
      <div><button type="button" class="btn small" onclick={addVolume}>Add volume</button></div>
      <small>Named volumes keep data across deploys.</small>
    </div>

    <div class="field">
      <span>Published ports</span>
      {#each value.ports as p, i}
        <div class="row tight">
          <input type="number" bind:value={p.published} placeholder="host port" aria-label="Host port" />
          <input type="number" bind:value={p.target} placeholder="container port" aria-label="Container port" />
          <select bind:value={p.protocol} aria-label="Protocol">
            <option value="tcp">TCP</option>
            <option value="udp">UDP</option>
          </select>
          <button type="button" class="btn ghost small" onclick={() => (value.ports = value.ports.filter((_, j) => j !== i))}>Remove</button>
        </div>
      {/each}
      <div><button type="button" class="btn small" onclick={addPort}>Add port</button></div>
      <small>{database ? 'Publishing a database port exposes it to the internet. Only do this with a firewall rule.' : 'Only for non-HTTP services. Websites should use a domain instead.'}</small>
    </div>
  {/if}
</div>

{#if helpOpen}
  <StaticSiteHelp onapply={usePreset} onclose={() => (helpOpen = false)} />
{/if}

<style>
  .fields {
    display: flex;
    flex-direction: column;
    gap: var(--sp-space-4);
  }
  .segmented {
    align-self: flex-start;
  }
  .label-row {
    display: flex;
    justify-content: space-between;
    align-items: baseline;
    gap: var(--sp-space-3);
    flex-wrap: wrap;
  }
  .help-link,
  .inline-link {
    border: 0;
    padding: 0;
    background: none;
    color: var(--sp-accent);
    font: inherit;
    font-size: var(--sp-text-sm);
    cursor: pointer;
  }
  .inline-link {
    font-size: inherit;
  }
  .help-link {
    display: inline-flex;
    align-items: center;
    gap: var(--sp-space-1);
  }
  .help-link:hover,
  .inline-link:hover {
    text-decoration: underline;
  }
  .sub {
    margin-top: var(--sp-space-2);
    padding-top: var(--sp-space-4);
    border-top: 1px solid var(--sp-border);
  }
  .row.tight {
    gap: var(--sp-space-2);
    flex-wrap: nowrap;
    align-items: center;
    margin-bottom: var(--sp-space-1);
  }
  .row.tight input,
  .row.tight select {
    flex: 1 1 0;
  }
  .static {
    display: flex;
    flex-direction: column;
    gap: var(--sp-space-3);
    background: var(--sp-bg-main);
  }
  .sub-title {
    font-size: var(--sp-text-sm);
  }
  .check {
    display: flex;
    gap: var(--sp-space-2);
    align-items: center;
    font-size: var(--sp-text-sm);
    color: var(--sp-text-muted);
  }
  .opt {
    text-transform: none;
    letter-spacing: 0;
    font-weight: normal;
    color: var(--sp-text-faint);
  }
  .with-icon {
    display: inline-flex;
    align-items: center;
    gap: var(--sp-space-1);
  }
  .hint {
    margin-top: calc(-1 * var(--sp-space-2));
    color: var(--sp-text-muted);
    font-size: var(--sp-text-sm);
  }
</style>
