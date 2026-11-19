<script>
  // Renders release notes (Markdown from GitHub) as plain text blocks:
  // headings, bullet lists and paragraphs. No HTML from the notes is ever
  // rendered, so a release can't inject markup into the panel.
  let { notes = '' } = $props();

  const inline = (s) =>
    s
      .replace(/\[([^\]]+)\]\([^)]+\)/g, '$1') // links: keep the text
      .replace(/(\*\*|__|`)/g, '')
      .trim();

  let blocks = $derived.by(() => {
    const out = [];
    let para = [];
    const flush = () => {
      if (para.length) out.push({ t: 'p', text: inline(para.join(' ')) });
      para = [];
    };
    for (const raw of notes.split(/\r?\n/)) {
      const line = raw.trim();
      let m;
      if (!line || /^<!--.*-->$/.test(line)) {
        flush();
      } else if ((m = line.match(/^#{1,6}\s+(.*)/))) {
        flush();
        out.push({ t: 'h', text: inline(m[1]) });
      } else if ((m = line.match(/^[-*+]\s+(.*)/))) {
        flush();
        const last = out[out.length - 1];
        if (last?.t === 'ul') last.items.push(inline(m[1]));
        else out.push({ t: 'ul', items: [inline(m[1])] });
      } else {
        para.push(line);
      }
    }
    flush();
    return out;
  });
</script>

<div class="notes">
  {#each blocks as b, i (i)}
    {#if b.t === 'h'}
      <h4>{b.text}</h4>
    {:else if b.t === 'ul'}
      <ul>
        {#each b.items as item, k (k)}<li>{item}</li>{/each}
      </ul>
    {:else}
      <p>{b.text}</p>
    {/if}
  {:else}
    <p class="faint">This release has no notes.</p>
  {/each}
</div>

<style>
  .notes {
    font-size: var(--sp-text-sm);
    line-height: var(--sp-line-height);
    overflow-wrap: anywhere;
  }
  h4 {
    margin: var(--sp-space-3) 0 var(--sp-space-1);
    font-size: var(--sp-text-sm);
  }
  h4:first-child {
    margin-top: 0;
  }
  ul {
    margin: 0 0 var(--sp-space-2);
    padding-left: var(--sp-space-5);
  }
  li {
    margin: 2px 0;
  }
  p {
    margin: 0 0 var(--sp-space-2);
  }
</style>
