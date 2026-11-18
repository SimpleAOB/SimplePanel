<script>
  // A query box with syntax highlighting: a transparent textarea over a
  // highlighted copy of its text. No editor library; text is rendered as
  // plain spans, never as HTML.
  import { highlight } from '../lib/highlight.js';

  let {
    value = $bindable(''),
    language = 'sql',
    engine = '',
    placeholder = '',
    label = 'Query',
    rows = 8,
    maxRows = 18, // grows with the text up to this many lines
    onrun = null, // (text) => void, Ctrl/Cmd+Enter
    textarea = $bindable(null),
  } = $props();

  let pre = $state(null);
  let tokens = $derived(highlight(language, value, engine));
  // The overlay needs a trailing newline to match the textarea's height.
  let lines = $derived(value.split('\n').length);

  function sync() {
    if (pre && textarea) {
      pre.scrollTop = textarea.scrollTop;
      pre.scrollLeft = textarea.scrollLeft;
    }
  }

  function replaceSelection(text, selectFrom, selectTo) {
    const el = textarea;
    const start = el.selectionStart;
    const end = el.selectionEnd;
    value = value.slice(0, start) + text + value.slice(end);
    queueMicrotask(() => {
      el.selectionStart = selectFrom ?? start + text.length;
      el.selectionEnd = selectTo ?? start + text.length;
    });
  }

  function keydown(e) {
    if ((e.ctrlKey || e.metaKey) && e.key === 'Enter') {
      e.preventDefault();
      const sel = value.slice(textarea.selectionStart, textarea.selectionEnd);
      onrun?.(sel.trim() ? sel : value);
      return;
    }
    if (e.key === 'Tab' && !e.ctrlKey && !e.metaKey && !e.altKey) {
      const start = textarea.selectionStart;
      const end = textarea.selectionEnd;
      const lineStart = value.lastIndexOf('\n', start - 1) + 1;
      if (e.shiftKey) {
        e.preventDefault();
        if (value.startsWith('  ', lineStart)) {
          value = value.slice(0, lineStart) + value.slice(lineStart + 2);
          queueMicrotask(() => (textarea.selectionStart = textarea.selectionEnd = Math.max(lineStart, start - 2)));
        }
        return;
      }
      if (start === end) {
        e.preventDefault();
        replaceSelection('  ');
      }
      // With a selection, Tab leaves the box (keyboard users can get out).
      return;
    }
    if (e.key === 'Enter' && !e.shiftKey) {
      // Keep the current line's indentation.
      const start = textarea.selectionStart;
      const lineStart = value.lastIndexOf('\n', start - 1) + 1;
      const indent = value.slice(lineStart).match(/^[ \t]*/)[0];
      if (indent) {
        e.preventDefault();
        replaceSelection('\n' + indent);
      }
    }
  }
</script>

<div class="editor" style:--rows={Math.max(rows, Math.min(lines + 1, maxRows))}>
  <pre bind:this={pre} class="hl" aria-hidden="true">{#each tokens as tk, i (i)}<span class="t-{tk.t}">{tk.v}</span>{/each}{'\n'}</pre>
  <textarea
    bind:this={textarea}
    bind:value
    onscroll={sync}
    oninput={sync}
    onkeydown={keydown}
    spellcheck="false"
    autocapitalize="off"
    autocomplete="off"
    wrap="off"
    aria-label={label}
    {placeholder}
    data-lines={lines}
  ></textarea>
</div>

<style>
  .editor {
    position: relative;
    height: calc(var(--rows) * 1.55em + 2 * var(--sp-space-3));
    min-height: 96px;
    resize: vertical;
    overflow: hidden;
    border: 1px solid var(--sp-border-strong);
    border-radius: var(--sp-radius-md);
    background: var(--sp-bg-code);
    font-family: var(--sp-font-mono);
    font-size: var(--sp-text-sm);
  }
  .editor:focus-within {
    border-color: var(--sp-accent);
    box-shadow: 0 0 0 3px var(--sp-focus-ring);
  }
  .hl,
  textarea {
    position: absolute;
    inset: 0;
    margin: 0;
    padding: var(--sp-space-3);
    font: inherit;
    line-height: 1.55;
    tab-size: 2;
    white-space: pre;
    overflow: auto;
    border: 0;
    letter-spacing: normal;
    word-spacing: normal;
  }
  .hl {
    color: var(--sp-text);
    pointer-events: none;
    overflow: hidden;
  }
  textarea {
    width: 100%;
    height: 100%;
    background: transparent;
    color: transparent;
    caret-color: var(--sp-text);
    resize: none;
    outline: none;
    box-shadow: none;
  }
  textarea::placeholder {
    color: var(--sp-text-faint);
  }
  textarea::selection {
    background: var(--sp-syn-selection);
    color: transparent;
  }
  .t-kw {
    color: var(--sp-syn-keyword);
    font-weight: var(--sp-weight-medium);
  }
  .t-str {
    color: var(--sp-syn-string);
  }
  .t-num {
    color: var(--sp-syn-number);
  }
  .t-com {
    color: var(--sp-syn-comment);
    font-style: italic;
  }
  .t-fn {
    color: var(--sp-syn-function);
  }
  .t-op {
    color: var(--sp-syn-operator);
  }
  .t-key {
    color: var(--sp-syn-key);
  }
  .t-lit {
    color: var(--sp-syn-literal);
  }
  .t-ident {
    color: var(--sp-syn-ident);
  }
  .t-var {
    color: var(--sp-syn-literal);
  }
</style>
