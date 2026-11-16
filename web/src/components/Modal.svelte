<script>
  import Icon from './Icon.svelte';
  let { title, onclose, children } = $props();
  function key(e) {
    if (e.key === 'Escape') onclose();
  }
</script>

<svelte:window onkeydown={key} />
<div class="backdrop">
  <button class="scrim" aria-label="Close" onclick={onclose}></button>
  <div class="modal card" role="dialog" aria-modal="true" aria-label={title}>
    <header>
      <h2>{title}</h2>
      <button class="btn ghost icon" aria-label="Close" onclick={onclose}><Icon name="x" size={18} /></button>
    </header>
    {@render children()}
  </div>
</div>

<style>
  .backdrop {
    position: fixed;
    inset: 0;
    z-index: 50;
    display: grid;
    place-items: center;
    padding: var(--sp-space-4);
  }
  .scrim {
    position: absolute;
    inset: 0;
    border: 0;
    background: var(--sp-overlay);
  }
  .modal {
    position: relative;
    width: min(680px, 100%);
    max-height: calc(100vh - 2 * var(--sp-space-4));
    overflow: auto;
    display: flex;
    flex-direction: column;
    gap: var(--sp-space-3);
    background: var(--sp-bg-elevated);
    box-shadow: var(--sp-shadow);
    padding: var(--sp-space-5);
  }
  header {
    display: flex;
    justify-content: space-between;
    align-items: center;
  }
</style>
