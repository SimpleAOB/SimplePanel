<script>
  import Modal from './Modal.svelte';
  import { leftoverPrompt } from '../lib/leftover.svelte.js';

  let sure = $state(false);
  $effect(() => {
    if (leftoverPrompt.open) sure = false;
  });
  const answer = (c) => leftoverPrompt.resolve?.(c);
</script>

{#if leftoverPrompt.open}
  <Modal title="Old data found" onclose={() => answer('')}>
    <p>{leftoverPrompt.message}</p>
    <p class="muted vols">
      Docker {leftoverPrompt.volumes.length === 1 ? 'volume' : 'volumes'}: {leftoverPrompt.volumes.join(', ')}
    </p>
    {#if leftoverPrompt.canReplace}
      <label class="check"><input type="checkbox" bind:checked={sure} /> I understand the old data will be deleted for good</label>
    {:else}
      <p class="muted">Only someone who can manage this project can delete the old data. Choose another name, or ask an admin.</p>
    {/if}
    <div class="actions">
      <button class="btn" onclick={() => answer('')}>Choose another name</button>
      {#if leftoverPrompt.allowKeep}
        <button class="btn" onclick={() => answer('keep')}>Use the old data</button>
      {/if}
      {#if leftoverPrompt.canReplace}
        <button class="btn danger" disabled={!sure} onclick={() => answer('replace')}>Delete old data and continue</button>
      {/if}
    </div>
  </Modal>
{/if}

<style>
  .vols {
    font-size: var(--sp-text-sm);
    overflow-wrap: anywhere;
  }
  .check {
    display: flex;
    align-items: center;
    gap: var(--sp-space-2);
    font-size: var(--sp-text-sm);
  }
  .actions {
    display: flex;
    justify-content: flex-end;
    gap: var(--sp-space-2);
    flex-wrap: wrap;
    margin-top: var(--sp-space-4);
  }
</style>
