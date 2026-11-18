<script>
  // A read-only results table: sticky header, row numbers, NULL shown as
  // such, click a cell to see its full value. Sorting is either done here
  // (onsort not given) or by the caller (server-side, for the table browser).
  import Icon from './Icon.svelte';

  let { columns = [], rows = [], sort = null, onsort = null, offset = 0 } = $props();

  let localSort = $state(null); // { col, dir }
  let selected = $state(null); // { r, c }
  let copied = $state(false);

  let activeSort = $derived(onsort ? sort : localSort);
  let view = $derived.by(() => {
    if (onsort || !localSort) return rows;
    const i = columns.indexOf(localSort.col);
    if (i < 0) return rows;
    const dir = localSort.dir === 'desc' ? -1 : 1;
    const num = (v) => (v !== null && v !== '' && !isNaN(Number(v)) ? Number(v) : null);
    return [...rows].sort((a, b) => {
      const x = a[i];
      const y = b[i];
      if (x === null || x === undefined) return 1;
      if (y === null || y === undefined) return -1;
      const nx = num(x);
      const ny = num(y);
      if (nx !== null && ny !== null) return (nx - ny) * dir;
      return String(x).localeCompare(String(y)) * dir;
    });
  });

  $effect(() => {
    // New results: forget the selection and local sort.
    rows;
    selected = null;
    localSort = null;
  });

  function clickHeader(col) {
    const cur = activeSort?.col === col ? activeSort.dir : null;
    const next = cur === null ? { col, dir: 'asc' } : cur === 'asc' ? { col, dir: 'desc' } : null;
    if (onsort) onsort(next);
    else localSort = next;
  }

  let selValue = $derived(selected ? view[selected.r]?.[selected.c] : undefined);
  let pretty = $derived.by(() => {
    if (typeof selValue !== 'string') return selValue;
    const t = selValue.trim();
    if ((t.startsWith('{') && t.endsWith('}')) || (t.startsWith('[') && t.endsWith(']'))) {
      try {
        return JSON.stringify(JSON.parse(t), null, 2);
      } catch {
        /* not JSON */
      }
    }
    return selValue;
  });

  async function copyValue() {
    try {
      await navigator.clipboard.writeText(selValue === null ? 'NULL' : String(selValue));
      copied = true;
      setTimeout(() => (copied = false), 1200);
    } catch {
      /* clipboard unavailable */
    }
  }
</script>

<div class="wrap">
  <div class="scroll">
    <table>
      <thead>
        <tr>
          <th class="num">#</th>
          {#each columns as col, ci (ci)}
            <th>
              <button class="head" onclick={() => clickHeader(col)} title="Sort by {col}">
                <span>{col}</span>
                {#if activeSort?.col === col}<span class="arrow">{activeSort.dir === 'asc' ? '▲' : '▼'}</span>{/if}
              </button>
            </th>
          {/each}
        </tr>
      </thead>
      <tbody>
        {#each view as row, ri (ri)}
          <tr>
            <td class="num">{offset + ri + 1}</td>
            {#each columns as _, ci (ci)}
              {@const v = row[ci]}
              <td
                class:null={v === null || v === undefined}
                class:sel={selected?.r === ri && selected?.c === ci}
                onclick={() => (selected = { r: ri, c: ci })}
                title={v === null || v === undefined ? 'NULL' : String(v).length > 60 ? 'Click to see the full value' : ''}
              >{v === null || v === undefined ? 'NULL' : v}</td>
            {/each}
          </tr>
        {/each}
      </tbody>
    </table>
  </div>
  {#if selected && selValue !== undefined}
    <div class="inspect">
      <div class="row between">
        <span class="faint small"><strong>{columns[selected.c]}</strong> · row {offset + selected.r + 1}</span>
        <span class="row tight">
          <button class="btn ghost small" onclick={copyValue}><Icon name="copy" size={14} /> {copied ? 'Copied' : 'Copy value'}</button>
          <button class="btn ghost icon small" aria-label="Close" onclick={() => (selected = null)}><Icon name="x" size={14} /></button>
        </span>
      </div>
      <pre class:null={selValue === null}>{selValue === null ? 'NULL' : pretty}</pre>
    </div>
  {/if}
</div>

<style>
  .wrap {
    display: flex;
    flex-direction: column;
    min-height: 0;
    border: 1px solid var(--sp-border);
    border-radius: var(--sp-radius-md);
    overflow: hidden;
    background: var(--sp-bg-code);
  }
  .scroll {
    overflow: auto;
    max-height: 60vh;
  }
  table {
    border-collapse: separate;
    border-spacing: 0;
    font-family: var(--sp-font-mono);
    font-size: var(--sp-text-xs);
    min-width: 100%;
  }
  th {
    position: sticky;
    top: 0;
    z-index: 1;
    background: var(--sp-bg-elevated);
    border-bottom: 1px solid var(--sp-border-strong);
    text-align: left;
    padding: 0;
    white-space: nowrap;
  }
  .head {
    all: unset;
    box-sizing: border-box;
    display: flex;
    align-items: center;
    gap: var(--sp-space-1);
    width: 100%;
    padding: var(--sp-space-2) var(--sp-space-3);
    font-weight: var(--sp-weight-bold);
    color: var(--sp-text);
    cursor: pointer;
  }
  .head:hover {
    background: var(--sp-bg-hover);
  }
  .head:focus-visible {
    outline: 2px solid var(--sp-focus-ring);
    outline-offset: -2px;
  }
  .arrow {
    font-size: 9px;
    color: var(--sp-accent);
  }
  td {
    padding: 5px var(--sp-space-3);
    border-bottom: 1px solid var(--sp-border);
    border-right: 1px solid var(--sp-border);
    white-space: nowrap;
    max-width: 360px;
    overflow: hidden;
    text-overflow: ellipsis;
    color: var(--sp-text);
    cursor: default;
  }
  tbody tr:hover td {
    background: var(--sp-bg-hover);
  }
  td.null {
    color: var(--sp-text-faint);
    font-style: italic;
  }
  td.sel {
    outline: 2px solid var(--sp-accent);
    outline-offset: -2px;
  }
  .num {
    color: var(--sp-text-faint);
    text-align: right;
    position: sticky;
    left: 0;
    background: var(--sp-bg-code);
    border-right: 1px solid var(--sp-border-strong);
    padding: 5px var(--sp-space-2);
    min-width: 28px;
  }
  th.num {
    z-index: 2;
    background: var(--sp-bg-elevated);
    font-weight: var(--sp-weight-bold);
  }
  .inspect {
    border-top: 1px solid var(--sp-border-strong);
    padding: var(--sp-space-2) var(--sp-space-3);
    background: var(--sp-bg-panel);
  }
  .inspect pre {
    margin: var(--sp-space-1) 0 0;
    max-height: 200px;
    overflow: auto;
    font-family: var(--sp-font-mono);
    font-size: var(--sp-text-xs);
    white-space: pre-wrap;
    word-break: break-word;
  }
  .inspect pre.null {
    color: var(--sp-text-faint);
    font-style: italic;
  }
  .small {
    font-size: var(--sp-text-xs);
  }
  .between {
    justify-content: space-between;
    align-items: center;
  }
  .row.tight {
    gap: var(--sp-space-1);
    flex-wrap: nowrap;
  }
</style>
