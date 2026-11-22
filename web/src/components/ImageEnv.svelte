<script>
  // The variables an image declares with ENV, as rows to fill in before
  // the app runs. Each row edits its KEY=value line in `env` (the same
  // text the Environment box holds), so pasting a .env file and typing
  // here stay in step. An empty row keeps the image's own default.
  import { api } from '../lib/api.js';
  import { envValue, setEnvValue, isSystemVar } from '../lib/envtext.js';

  let { image = '', registryId = null, projectId, env = $bindable(''), info = $bindable(null) } = $props();

  let loading = $state(false);
  let error = $state('');
  let showSystem = $state(false);
  let looked = $state('');

  // Only look up names that look finished, and wait for typing to stop:
  // Docker Hub counts every lookup against its pull limit.
  let ref = $derived((image || '').trim());
  let ready = $derived(ref.length > 1 && !/[/:@]$/.test(ref) && !/\s/.test(ref));
  let seq = 0;
  $effect(() => {
    const r = ref;
    const reg = registryId;
    const pid = projectId;
    if (!ready || !pid) {
      info = null;
      error = '';
      loading = false;
      return;
    }
    const mine = ++seq;
    const t = setTimeout(async () => {
      loading = true;
      error = '';
      try {
        const q = new URLSearchParams({ image: r });
        if (reg !== null && reg !== undefined) q.set('registryId', String(reg));
        const res = await api.get(`/projects/${pid}/image-info?${q}`);
        if (mine !== seq) return;
        looked = r;
        if (res.found) {
          info = res;
        } else {
          info = null;
          error = res.error || 'the image could not be read';
        }
      } catch (err) {
        if (mine !== seq) return;
        info = null;
        error = err.message;
      } finally {
        if (mine === seq) loading = false;
      }
    }, 900);
    return () => clearTimeout(t);
  });

  let own = $derived((info?.env || []).filter((v) => !isSystemVar(v.key)));
  let system = $derived((info?.env || []).filter((v) => isSystemVar(v.key)));
  let shortName = $derived(looked.replace(/^.*\//, '').replace(/[:@].*$/, ''));

  function update(key, e) {
    env = setEnvValue(env, key, e.currentTarget.value);
  }
</script>

{#snippet rows(list)}
  {#each list as v (v.key)}
    <label class="var">
      <span class="key mono" title={v.key}>{v.key}</span>
      <input
        type="text"
        value={envValue(env, v.key) ?? ''}
        oninput={(e) => update(v.key, e)}
        placeholder={v.value === '' ? 'Not set by the image' : v.value}
        spellcheck="false"
        autocomplete="off"
      />
    </label>
  {/each}
{/snippet}

{#if ready}
  <div class="image-env" aria-live="polite">
    {#if loading && !info}
      <small class="faint">Reading the settings this image declares…</small>
    {:else if error}
      <small class="faint">Couldn't read the image's settings: {error}. You can still add variables below.</small>
    {:else if info}
      {#if own.length}
        <div class="head">
          <strong>From the image</strong>
          <small class="faint">{shortName} sets {own.length === 1 ? 'this variable' : `these ${own.length} variables`}. Leave a box empty to keep the image's default.</small>
        </div>
        <div class="vars">{@render rows(own)}</div>
      {:else}
        <small class="faint">{shortName} doesn't set any variables of its own. Images can still read others, so check its documentation for the ones it needs.</small>
      {/if}
      {#if system.length}
        <button type="button" class="linkish small-link" onclick={() => (showSystem = !showSystem)}>
          {showSystem ? 'Hide' : 'Show'} {system.length} system {system.length === 1 ? 'variable' : 'variables'} (paths, versions)
        </button>
        {#if showSystem}<div class="vars">{@render rows(system)}</div>{/if}
      {/if}
    {/if}
  </div>
{/if}

<style>
  .image-env {
    display: flex;
    flex-direction: column;
    gap: var(--sp-space-2);
  }
  .head {
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  .head strong {
    font-size: var(--sp-text-sm);
  }
  .vars {
    display: flex;
    flex-direction: column;
    gap: var(--sp-space-2);
  }
  .var {
    display: grid;
    grid-template-columns: minmax(120px, 34%) 1fr;
    align-items: center;
    gap: var(--sp-space-3);
  }
  .key {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    color: var(--sp-text-muted);
  }
  .var input {
    font-family: var(--sp-font-mono);
    font-size: var(--sp-text-sm);
    min-width: 0;
  }
  .small-link {
    align-self: flex-start;
    font-size: var(--sp-text-xs);
  }
  @media (max-width: 520px) {
    .var {
      grid-template-columns: 1fr;
      gap: 2px;
    }
  }
</style>
