<script>
  import { onMount } from 'svelte';
  import { api, setUnauthorizedHandler } from './lib/api.js';
  import { data, startRouter } from './lib/state.svelte.js';
  import Auth from './components/Auth.svelte';
  import Shell from './components/Shell.svelte';

  let phase = $state('loading'); // loading | setup | login | ready | error
  let loadError = $state('');

  async function boot() {
    try {
      const s = await api.get('/setup');
      if (s.needsSetup) {
        phase = 'setup';
        return;
      }
      try {
        data.user = await api.get('/me');
        phase = 'ready';
      } catch {
        phase = 'login';
      }
    } catch (e) {
      loadError = e.message;
      phase = 'error';
    }
  }

  setUnauthorizedHandler(() => {
    data.user = null;
    phase = 'login';
  });

  onMount(() => {
    startRouter();
    boot();
  });
</script>

{#if phase === 'loading'}
  <div class="center muted">Loading…</div>
{:else if phase === 'error'}
  <div class="center"><p class="error">Can't reach SimplePanel: {loadError}</p></div>
{:else if phase === 'setup' || phase === 'login'}
  <Auth mode={phase} ondone={(u) => { data.user = u; phase = 'ready'; }} />
{:else}
  <Shell />
{/if}

<style>
  .center {
    height: 100%;
    display: grid;
    place-items: center;
    padding: var(--sp-space-4);
  }
</style>
