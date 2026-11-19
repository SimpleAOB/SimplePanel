<script>
  import { copyText } from '../lib/clipboard.js';
  // Shows an export as JSON with copy and download.
  import { api } from '../lib/api.js';
  import Modal from './Modal.svelte';

  let { path, filename, title, onclose } = $props();
  let withEnv = $state(false);
  let text = $state('');
  let error = $state('');
  let copied = $state(false);

  $effect(() => {
    const env = withEnv;
    error = '';
    api
      .get(`${path}${env ? '?env=1' : ''}`)
      .then((doc) => (text = JSON.stringify(doc, null, 2)))
      .catch((err) => (error = err.message));
  });

  async function copy() {
    try {
      await copyText(text);
      copied = true;
      setTimeout(() => (copied = false), 1500);
    } catch {
      error = 'Copy is unavailable here; select the text and copy it manually.';
    }
  }

  function download() {
    const url = URL.createObjectURL(new Blob([text + '\n'], { type: 'application/json' }));
    const a = document.createElement('a');
    a.href = url;
    a.download = `${filename}.simplepanel.json`;
    a.click();
    URL.revokeObjectURL(url);
  }
</script>

<Modal {title} {onclose}>
  <p class="muted">
    Paste this into <strong>Import</strong> on any SimplePanel to recreate it. Access tokens, webhook URLs and database
    passwords are never included; new ones are generated on import. Database data is not included.
  </p>
  <label class="check">
    <input type="checkbox" bind:checked={withEnv} />
    Include app environment variables <span class="faint">(they may contain secrets)</span>
  </label>
  {#if error}<p class="error">{error}</p>{/if}
  <textarea readonly rows="16" spellcheck="false" value={text} aria-label="Exported JSON"></textarea>
  <div class="actions">
    <button class="btn" onclick={download} disabled={!text}>Download</button>
    <button class="btn primary" onclick={copy} disabled={!text}>{copied ? 'Copied' : 'Copy JSON'}</button>
  </div>
</Modal>

<style>
  .check {
    display: flex;
    gap: var(--sp-space-2);
    align-items: center;
    font-size: var(--sp-text-sm);
  }
  .actions {
    display: flex;
    justify-content: flex-end;
    gap: var(--sp-space-2);
  }
</style>
