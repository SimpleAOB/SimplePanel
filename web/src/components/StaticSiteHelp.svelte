<script>
  // Explains static sites and fills in the build fields from a preset.
  import Modal from './Modal.svelte';
  import { staticPresets } from '../lib/staticpresets.js';

  let { onapply, onclose } = $props();
</script>

<Modal title="Set up a static site" {onclose}>
  <p class="lead">
    A static site is a website made of files: HTML, CSS, JavaScript and images. SimplePanel copies
    them into a small, locked-down web server. No Dockerfile needed.
  </p>

  <ol class="steps">
    <li><strong>Pick your tool below</strong> and press <em>Use</em>. It fills in the build settings for you.</li>
    <li><strong>Site in a subfolder?</strong> Set <em>Build path</em> to that folder, e.g. <code>/web</code>.</li>
    <li><strong>Add a domain</strong> and deploy. Keep the app port at <code>80</code>.</li>
  </ol>

  <h3 class="h">Common setups</h3>
  <ul class="presets">
    {#each staticPresets as p (p.id)}
      <li class="preset">
        <div class="info">
          <div class="name">{p.name}</div>
          <div class="hint">{p.hint}</div>
          <div class="meta">
            {#if p.buildCommand}
              <span><span class="k">Build</span> <code>{p.buildCommand}</code></span>
              <span><span class="k">Output</span> <code>{p.outputDir}</code></span>
            {:else}
              <span><span class="k">Build</span> none, files are served as they are</span>
            {/if}
            {#if p.spa}<span class="tag">SPA fallback</span>{/if}
            {#if p.buildImage}<span><span class="k">Image</span> <code>{p.buildImage}</code></span>{/if}
          </div>
        </div>
        <button type="button" class="btn small" onclick={() => onapply(p)}>Use</button>
      </li>
    {/each}
  </ul>

  <details class="more">
    <summary>What the settings mean</summary>
    <dl>
      <dt>Build command</dt>
      <dd>Runs once per deploy to produce the site. Leave it empty if the files are ready to serve. Node dependencies install automatically with npm, pnpm or yarn, based on your lockfile.</dd>
      <dt>Output directory</dt>
      <dd>The folder the build writes to, relative to the build path. This is the folder that gets served.</dd>
      <dt>Build image</dt>
      <dd>The image the build runs in. Node 22 by default. Change it for tools that aren't Node, like Hugo.</dd>
      <dt>Single-page app</dt>
      <dd>Turn on for apps with client-side routing (React Router, Vue Router), so a link like <code>/settings</code> loads index.html instead of a 404.</dd>
    </dl>
    <p class="faint">Files starting with a dot are never served (except <code>.well-known</code>), and folders have no file listings.</p>
  </details>
</Modal>

<style>
  .lead {
    color: var(--sp-text-muted);
  }
  .steps {
    margin: 0;
    padding-left: var(--sp-space-5);
    display: flex;
    flex-direction: column;
    gap: var(--sp-space-1);
    font-size: var(--sp-text-sm);
  }
  .h {
    font-size: var(--sp-text-sm);
    margin-top: var(--sp-space-2);
  }
  .presets {
    list-style: none;
    margin: 0;
    padding: 0;
    border: 1px solid var(--sp-border);
    border-radius: var(--sp-radius-md);
  }
  .preset {
    display: flex;
    align-items: center;
    gap: var(--sp-space-3);
    padding: var(--sp-space-3);
  }
  .preset + .preset {
    border-top: 1px solid var(--sp-border);
  }
  .info {
    flex: 1;
    min-width: 0;
  }
  .name {
    font-weight: 600;
  }
  .hint {
    font-size: var(--sp-text-sm);
    color: var(--sp-text-muted);
  }
  .meta {
    display: flex;
    flex-wrap: wrap;
    gap: var(--sp-space-1) var(--sp-space-3);
    margin-top: var(--sp-space-1);
    font-size: var(--sp-text-xs);
    color: var(--sp-text-muted);
  }
  .k {
    color: var(--sp-text-faint);
  }
  .tag {
    color: var(--sp-accent);
  }
  .more summary {
    cursor: pointer;
    font-size: var(--sp-text-sm);
    font-weight: 600;
  }
  dl {
    margin: var(--sp-space-3) 0;
    font-size: var(--sp-text-sm);
  }
  dt {
    font-weight: 600;
  }
  dd {
    margin: 0 0 var(--sp-space-2);
    color: var(--sp-text-muted);
  }
  .faint {
    font-size: var(--sp-text-sm);
  }
</style>
