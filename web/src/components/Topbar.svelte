<script>
  // Header bar for the main column; holds the mobile menu button.
  import { ui } from '../lib/state.svelte.js';
  import Icon from './Icon.svelte';
  let { children, actions } = $props();
</script>

<header class="topbar">
  <button class="btn ghost icon menu" aria-label="Open menu" onclick={() => (ui.navOpen = true)}>
    <Icon name="menu" />
  </button>
  <div class="title">{@render children?.()}</div>
  {#if actions}<div class="actions">{@render actions()}</div>{/if}
</header>

<style>
  .topbar {
    height: var(--sp-header-height);
    flex: none;
    display: flex;
    align-items: center;
    gap: var(--sp-space-3);
    padding: 0 var(--sp-space-4);
    border-bottom: 1px solid var(--sp-border);
    position: sticky;
    top: 0;
    background: var(--sp-bg-main);
    z-index: 5;
  }
  .title {
    flex: 1;
    min-width: 0;
    display: flex;
    align-items: center;
    gap: var(--sp-space-2);
    font-weight: var(--sp-weight-bold);
    overflow: hidden;
    white-space: nowrap;
    text-overflow: ellipsis;
  }
  .actions {
    display: flex;
    gap: var(--sp-space-2);
    flex: none;
  }
  .menu {
    display: none;
  }
  @media (max-width: 760px) {
    .menu {
      display: inline-flex;
    }
  }
</style>
