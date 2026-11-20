<script>
  import Modal from './Modal.svelte';
  import { memPrompt } from '../lib/memory.svelte.js';

  const answer = (ok) => memPrompt.resolve?.(ok);
</script>

{#if memPrompt.open}
  <Modal title="Server is nearly out of memory" onclose={() => answer(false)}>
    <p>{memPrompt.message}</p>
    <p class="muted">
      If you go ahead and memory runs out, the server stops the biggest container to protect itself. On servers
      installed or updated with the current installer, SSH and Docker keep working either way.
    </p>
    <div class="actions">
      <a class="btn ghost" href="#/settings/monitoring" onclick={() => answer(false)}>See memory use</a>
      <button class="btn" onclick={() => answer(false)}>Cancel</button>
      <button class="btn danger" onclick={() => answer(true)}>Deploy anyway</button>
    </div>
  </Modal>
{/if}

<style>
  .actions {
    display: flex;
    justify-content: flex-end;
    gap: var(--sp-space-2);
    flex-wrap: wrap;
    margin-top: var(--sp-space-4);
  }
</style>
