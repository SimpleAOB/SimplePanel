<script>
  // Scrolling log output: level colours, local timestamps, search
  // highlighting and follow mode. Used by the logs panel and cron runs.
  import { tick } from 'svelte';
  import { parseLog, formatTs, highlight, lineMatches, LEVELS } from '../lib/logs.js';

  let {
    text = '',
    empty = 'No logs yet.',
    re = null, // search matcher (RegExp with the g flag) or null
    level = 'all',
    wrap = true,
    timestamps = true,
    follow = $bindable(true),
    shown = $bindable([]), // lines currently shown, for "download shown"
    total = $bindable(0),
    compact = false,
  } = $props();

  let box = $state();
  let lines = $derived(parseLog(text));
  let visible = $derived.by(() => {
    const keep = LEVELS[level] || LEVELS.all;
    return lines.filter((l) => keep(l) && lineMatches(l, re));
  });
  let filtering = $derived(!!re || level !== 'all');

  $effect(() => {
    shown = visible;
    total = lines.length;
  });

  // Keep the newest line in view while following.
  $effect(() => {
    visible;
    if (follow) toBottom();
  });

  async function toBottom() {
    await tick();
    if (box) box.scrollTop = box.scrollHeight;
  }

  // Scrolling up pauses follow; scrolling back to the end resumes it.
  function onScroll() {
    const atEnd = box.scrollHeight - box.scrollTop - box.clientHeight < 24;
    if (atEnd !== follow) follow = atEnd;
  }

  function jump() {
    follow = true;
    toBottom();
  }
</script>

<div class="wrap" class:compact>
  <!-- Focusable so the log can be scrolled with the keyboard. -->
  <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
  <div
    class="log"
    class:nowrap={!wrap}
    bind:this={box}
    onscroll={onScroll}
    role="log"
    aria-live="off"
    tabindex="0"
  >
    {#if lines.length === 0}
      <p class="empty">{empty}</p>
    {:else if visible.length === 0}
      <p class="empty">No lines match{filtering ? ' the filter' : ''}.</p>
    {:else}
      {#each visible as l, i (i)}
        <div class="line" class:lv-error={l.level === 'error'} class:lv-warn={l.level === 'warn'} class:lv-debug={l.level === 'debug'}>
          {#if timestamps && l.ts}<span class="ts" title={l.ts.toISOString()}>{formatTs(l.ts)}</span>{/if}<span class="text"
            >{#each highlight(l.text, re) as p}{#if p.hit}<mark>{p.text}</mark>{:else}{p.text}{/if}{/each}</span
          >
        </div>
      {/each}
    {/if}
  </div>
  {#if !follow && visible.length > 0}
    <button class="jump btn small" onclick={jump}>Jump to latest ↓</button>
  {/if}
</div>

<style>
  .wrap {
    position: relative;
    flex: 1;
    min-height: 0;
    display: flex;
  }
  .log {
    flex: 1;
    min-width: 0;
    min-height: 0;
    overflow: auto;
    padding: var(--sp-space-2) 0;
    background: var(--sp-bg-code);
    color: var(--sp-text);
    font-family: var(--sp-font-mono);
    font-size: var(--sp-text-xs);
    line-height: 1.6;
  }
  .log:focus-visible {
    outline: 2px solid var(--sp-focus-ring);
    outline-offset: -2px;
  }
  .line {
    display: flex;
    gap: var(--sp-space-3);
    padding: 0 var(--sp-space-3);
    border-left: 2px solid transparent;
    content-visibility: auto;
    contain-intrinsic-size: auto 19px;
  }
  .line:hover {
    background: var(--sp-bg-hover);
  }
  .text {
    flex: 1;
    min-width: 0;
    white-space: pre-wrap;
    overflow-wrap: anywhere;
  }
  .nowrap .line {
    width: max-content;
    min-width: 100%;
  }
  .nowrap .text {
    white-space: pre;
    overflow-wrap: normal;
  }
  .ts {
    flex: none;
    color: var(--sp-text-faint);
    user-select: none;
  }
  .line.lv-error {
    border-left-color: var(--sp-danger);
    background: color-mix(in srgb, var(--sp-danger) 8%, transparent);
  }
  .line.lv-error .text {
    color: var(--sp-danger);
  }
  .line.lv-warn {
    border-left-color: var(--sp-warning);
  }
  .line.lv-warn .text {
    color: var(--sp-warning);
  }
  .line.lv-debug .text {
    color: var(--sp-text-muted);
  }
  mark {
    background: color-mix(in srgb, var(--sp-accent) 35%, transparent);
    color: inherit;
    border-radius: 2px;
    box-shadow: 0 0 0 1px color-mix(in srgb, var(--sp-accent) 60%, transparent);
  }
  .empty {
    margin: 0;
    padding: var(--sp-space-2) var(--sp-space-3);
    color: var(--sp-text-faint);
    font-family: var(--sp-font-sans);
  }
  .jump {
    position: absolute;
    right: var(--sp-space-4);
    bottom: var(--sp-space-3);
    box-shadow: var(--sp-shadow);
    background: var(--sp-bg-elevated);
  }
  .compact .log {
    border-radius: var(--sp-radius-md);
    border: 1px solid var(--sp-border);
    background: var(--sp-bg-main);
  }
</style>
