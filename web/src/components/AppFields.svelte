<script>
  // Source + runtime fields for an app. `value` is a plain object bound
  // by the parent. Advanced fields only show when Advanced view is on.
  import { ui } from '../lib/state.svelte.js';

  let { value = $bindable(), hasToken = false, showRuntime = true, database = false } = $props();

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
      <small>{database ? 'Change the tag to switch versions. Major upgrades may need a manual data migration.' : 'Public images from Docker Hub, GHCR or any registry.'}</small>
    </label>
  {:else}
    <label class="field">
      <span>Repository URL</span>
      <input type="url" bind:value={value.gitUrl} placeholder="https://github.com/user/repo" required />
      <small>The repository needs a Dockerfile.</small>
    </label>
    {#if ui.advanced}
      <div class="row">
        <label class="field">
          <span>Branch</span>
          <input type="text" bind:value={value.gitBranch} placeholder="default branch" />
        </label>
        <label class="field">
          <span>Access token</span>
          <input type="password" bind:value={value.gitToken} autocomplete="off" placeholder={hasToken ? 'saved (leave blank to keep)' : 'for private repositories'} />
        </label>
      </div>
      {#if hasToken}
        <label class="check"><input type="checkbox" bind:checked={value.clearGitToken} /> Remove saved access token</label>
      {/if}
      <div class="row">
        <label class="field">
          <span>Build path</span>
          <input type="text" bind:value={value.buildPath} placeholder="/ (repository root)" />
        </label>
        <label class="field">
          <span>Dockerfile</span>
          <input type="text" bind:value={value.dockerfile} placeholder="Dockerfile" />
        </label>
      </div>
    {/if}
  {/if}

  {#if showRuntime && ui.advanced}
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

<style>
  .fields {
    display: flex;
    flex-direction: column;
    gap: var(--sp-space-4);
  }
  .segmented {
    align-self: flex-start;
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
  .check {
    display: flex;
    gap: var(--sp-space-2);
    align-items: center;
    font-size: var(--sp-text-sm);
    color: var(--sp-text-muted);
  }
</style>
