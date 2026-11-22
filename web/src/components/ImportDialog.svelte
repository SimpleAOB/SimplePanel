<script>
  // Paste (or load) an export to create a project or a service.
  import { withLeftoverCheck, post } from '../lib/leftover.svelte.js';
  import { go, loadProjects } from '../lib/state.svelte.js';
  import Modal from './Modal.svelte';

  // projectId set: import services into that project. Unset: import a project.
  let { projectId = 0, onclose } = $props();
  let text = $state('');
  let name = $state('');
  let deploy = $state(true);
  let error = $state('');
  let warnings = $state([]);
  let busy = $state(false);
  let result = $state(null);

  function finish() {
    onclose();
    const r = result;
    if (r) go(projectId && r.appIds.length === 1 ? `/p/${r.projectId}/a/${r.appIds[0]}` : `/p/${r.projectId}`);
  }

  let parsed = $derived.by(() => {
    try {
      return JSON.parse(text);
    } catch {
      return null;
    }
  });
  let summary = $derived.by(() => {
    if (!parsed) return '';
    if (parsed.kind === 'project' && parsed.project)
      return `Project "${parsed.project.name}" with ${parsed.project.services?.length ?? 0} service(s)`;
    if (parsed.kind === 'service' && parsed.service) return `Service "${parsed.service.name}" (${parsed.service.type})`;
    return 'Not a SimplePanel export';
  });
  let wrongKind = $derived(
    parsed && ((projectId && parsed.kind === 'project') || (!projectId && parsed.kind === 'service')),
  );

  async function loadFile(e) {
    const f = e.currentTarget.files?.[0];
    if (!f) return;
    if (f.size > 512 * 1024) {
      error = 'That file is too large to be a SimplePanel export.';
      return;
    }
    text = await f.text();
  }

  async function submit(e) {
    e.preventDefault();
    error = '';
    warnings = [];
    busy = true;
    try {
      const res = await withLeftoverCheck(post, '/import', { document: text, projectId, name: name.trim(), deploy });
      await loadProjects();
      result = res;
      if (res.warnings.length) {
        warnings = res.warnings; // keep the dialog open so the notes can be read
        return;
      }
      finish();
    } catch (err) {
      error = err.message;
    } finally {
      busy = false;
    }
  }
</script>

<Modal title={projectId ? 'Import a service' : 'Import a project'} {onclose}>
  {#if warnings.length}
    <p>Imported, with notes:</p>
    <ul class="warnings">
      {#each warnings as w}<li>{w}</li>{/each}
    </ul>
    <div class="actions">
      <button class="btn primary" onclick={finish}>Done</button>
    </div>
  {:else}
    <form onsubmit={submit}>
      <p class="muted">Paste JSON exported from SimplePanel, or load a .json file.</p>
      <textarea bind:value={text} rows="12" spellcheck="false" placeholder={'{ "simplepanel": 1, "kind": "…" }'} aria-label="JSON to import" required></textarea>
      <div class="row between">
        <span class="faint small">{summary}</span>
        <label class="btn small file">Load file<input type="file" accept=".json,application/json" onchange={loadFile} /></label>
      </div>
      {#if wrongKind}
        <p class="error">
          {projectId ? 'This is a whole project. Use Import project on the Overview page.' : 'This is a single service. Import it from inside a project.'}
        </p>
      {/if}
      <div class="row">
        <label class="field">
          <span>{projectId ? 'Service name' : 'Project name'} <span class="faint">(optional)</span></span>
          <input type="text" bind:value={name} placeholder="keep the exported name" />
        </label>
      </div>
      <label class="check"><input type="checkbox" bind:checked={deploy} /> Deploy after import</label>
      {#if error}<p class="error">{error}</p>{/if}
      <div class="actions">
        <button type="button" class="btn ghost" onclick={onclose}>Cancel</button>
        <button class="btn primary" disabled={busy || !text.trim() || wrongKind}>{busy ? 'Importing…' : 'Import'}</button>
      </div>
    </form>
  {/if}
</Modal>

<style>
  form {
    display: flex;
    flex-direction: column;
    gap: var(--sp-space-3);
  }
  .between {
    justify-content: space-between;
    align-items: center;
  }
  .small {
    font-size: var(--sp-text-sm);
  }
  .file {
    position: relative;
    overflow: hidden;
  }
  .file input {
    position: absolute;
    inset: 0;
    opacity: 0;
    cursor: pointer;
  }
  .check {
    display: flex;
    gap: var(--sp-space-2);
    align-items: center;
    font-size: var(--sp-text-sm);
  }
  .warnings {
    margin: 0;
    padding-left: var(--sp-space-5);
    color: var(--sp-warning);
    font-size: var(--sp-text-sm);
  }
  .actions {
    display: flex;
    justify-content: flex-end;
    gap: var(--sp-space-2);
  }
</style>
