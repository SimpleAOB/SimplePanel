<script>
  // Small time-series chart: 2px lines with a light area wash, hairline
  // grid, a crosshair tooltip that lists every series, and a table view.
  import { clock } from '../lib/format.js';

  let {
    title,
    series, // [{ key, label, color }]
    points, // [{ t, [key]: number }]
    format = (v) => String(v),
    max = 0, // fixed top of the y axis (e.g. 100 for percent); 0 = auto
    limit = 0, // optional reference line (e.g. a memory limit)
    limitLabel = '',
    long = false, // show dates on the x axis
    height = 150,
  } = $props();

  let width = $state(0);
  let hover = $state(-1);

  const pad = { top: 8, right: 8, bottom: 22, left: 56 };
  let w = $derived(Math.max(0, width - pad.left - pad.right));
  let h = $derived(height - pad.top - pad.bottom);

  let t0 = $derived(points.length ? points[0].t : 0);
  let t1 = $derived(points.length ? points[points.length - 1].t : 1);
  let peak = $derived(Math.max(0, ...points.flatMap((p) => series.map((s) => p[s.key] || 0))));
  let top = $derived(niceTop(max || Math.max(peak, limit && limit < peak * 3 ? limit : 0) * 1.1 || 1));
  let ticks = $derived([0, top / 2, top]);

  function niceTop(v) {
    if (max) return max;
    const e = Math.pow(10, Math.floor(Math.log10(v)));
    for (const m of [1, 2, 2.5, 5, 10]) if (m * e >= v) return m * e;
    return 10 * e;
  }
  const x = (t) => (t1 === t0 ? w : ((t - t0) / (t1 - t0)) * w);
  const y = (v) => h - Math.min(1, (v || 0) / top) * h;

  // Gaps longer than 3 samples break the line (the service was down).
  function paths(key) {
    let line = '';
    let area = '';
    let seg = [];
    const lone = [];
    const flush = () => {
      if (!seg.length) return;
      if (seg.length === 1) {
        lone.push(seg[0]);
        seg = [];
        return;
      }
      line += 'M' + seg.map((p) => `${x(p.t).toFixed(1)},${y(p[key]).toFixed(1)}`).join('L');
      area +=
        `M${x(seg[0].t).toFixed(1)},${h}L` +
        seg.map((p) => `${x(p.t).toFixed(1)},${y(p[key]).toFixed(1)}`).join('L') +
        `L${x(seg[seg.length - 1].t).toFixed(1)},${h}Z`;
      seg = [];
    };
    // Typical spacing (median), so a pair of samples taken close together
    // after a restart doesn't make every normal interval look like a gap.
    const diffs = points.slice(1).map((p, i) => p.t - points[i].t).filter((d) => d > 0).sort((a, b) => a - b);
    const step = diffs.length ? Math.max(diffs[Math.floor(diffs.length / 2)], 30) : 0;
    points.forEach((p, i) => {
      if (i > 0 && step && p.t - points[i - 1].t > step * 3) flush();
      seg.push(p);
    });
    flush();
    return { line, area, lone };
  }

  let xTicks = $derived.by(() => {
    if (!points.length || !w) return [];
    const n = Math.max(2, Math.min(5, Math.floor(w / 110)));
    return Array.from({ length: n }, (_, i) => t0 + ((t1 - t0) * i) / (n - 1));
  });

  function move(e) {
    if (!points.length) return;
    const r = e.currentTarget.getBoundingClientRect();
    const px = e.clientX - r.left - pad.left;
    const t = t0 + (px / w) * (t1 - t0);
    let best = 0;
    for (let i = 1; i < points.length; i++) if (Math.abs(points[i].t - t) < Math.abs(points[best].t - t)) best = i;
    hover = best;
  }
  function key(e) {
    if (!points.length) return;
    if (e.key === 'ArrowLeft') hover = Math.max(0, (hover < 0 ? points.length : hover) - 1);
    else if (e.key === 'ArrowRight') hover = Math.min(points.length - 1, hover + 1);
    else return;
    e.preventDefault();
  }

  let hp = $derived(hover >= 0 ? points[hover] : null);
  let last = $derived(points[points.length - 1]);
  let tableRows = $derived.by(() => {
    const step = Math.max(1, Math.ceil(points.length / 24));
    return points.filter((_, i) => i % step === 0 || i === points.length - 1);
  });
</script>

<figure class="chart">
  <figcaption>
    <span class="title">{title}</span>
    {#if last}
      <span class="now">
        {#each series as s, i (s.key)}
          {#if series.length > 1}<span class="key" style="background:{s.color}"></span><span class="muted">{s.label}</span>{/if}
          <strong>{format(last[s.key] || 0)}</strong>{#if i < series.length - 1}<span class="sep"></span>{/if}
        {/each}
      </span>
    {/if}
  </figcaption>

  <div class="plot" bind:clientWidth={width} style="height:{height}px">
    {#if !points.length}
      <p class="empty faint">No data yet. Samples are taken every 30 seconds.</p>
    {:else if width}
      <!-- svelte-ignore a11y_no_noninteractive_tabindex, a11y_no_noninteractive_element_interactions (focusable so arrow keys move the crosshair) -->
      <svg
        {width}
        {height}
        role="img"
        aria-label="{title} over time"
        tabindex="0"
        onpointermove={move}
        onpointerleave={() => (hover = -1)}
        onkeydown={key}
        onblur={() => (hover = -1)}
      >
        <g transform="translate({pad.left},{pad.top})">
          {#each ticks as tv}
            <line class="grid" x1="0" x2={w} y1={y(tv)} y2={y(tv)} />
            <text class="tick" x="-8" y={y(tv)} dy="0.32em" text-anchor="end">{format(tv)}</text>
          {/each}
          {#each xTicks as tt, i}
            <text class="tick" x={x(tt)} y={h + 16} text-anchor={i === 0 ? 'start' : i === xTicks.length - 1 ? 'end' : 'middle'}>{clock(tt, long)}</text>
          {/each}
          {#if limit && limit <= top}
            <line class="limit" x1="0" x2={w} y1={y(limit)} y2={y(limit)} />
            <text class="tick" x={w} y={y(limit) - 5} text-anchor="end">{limitLabel}</text>
          {/if}
          {#each series as s (s.key)}
            {@const p = paths(s.key)}
            <path d={p.area} fill={s.color} style="fill-opacity: var(--sp-series-fill)" stroke="none" />
            <path d={p.line} fill="none" stroke={s.color} stroke-width="2" stroke-linejoin="round" stroke-linecap="round" />
            {#each p.lone as q (q.t)}<circle cx={x(q.t)} cy={y(q[s.key])} r="2.5" fill={s.color} />{/each}
          {/each}
          {#if hp}
            <line class="cross" x1={x(hp.t)} x2={x(hp.t)} y1="0" y2={h} />
            {#each series as s (s.key)}
              <circle cx={x(hp.t)} cy={y(hp[s.key])} r="4" fill={s.color} class="dot" />
            {/each}
          {/if}
        </g>
      </svg>
      {#if hp}
        <div class="tip" style="left:{Math.min(width - 150, Math.max(0, pad.left + x(hp.t) + 10))}px">
          <div class="faint">{clock(hp.t, long)}</div>
          {#each series as s (s.key)}
            <div class="tiprow">
              <span class="linekey" style="background:{s.color}"></span>
              <strong>{format(hp[s.key] || 0)}</strong>
              {#if series.length > 1}<span class="muted">{s.label}</span>{/if}
            </div>
          {/each}
        </div>
      {/if}
    {/if}
  </div>

  {#if points.length}
    <details>
      <summary class="faint">Table</summary>
      <table>
        <thead><tr><th>Time</th>{#each series as s (s.key)}<th>{s.label}</th>{/each}</tr></thead>
        <tbody>
          {#each tableRows as p (p.t)}
            <tr><td>{clock(p.t, long)}</td>{#each series as s (s.key)}<td>{format(p[s.key] || 0)}</td>{/each}</tr>
          {/each}
        </tbody>
      </table>
    </details>
  {/if}
</figure>

<style>
  .chart {
    margin: 0;
    display: flex;
    flex-direction: column;
    gap: var(--sp-space-2);
    min-width: 0;
  }
  figcaption {
    display: flex;
    justify-content: space-between;
    align-items: baseline;
    gap: var(--sp-space-2);
    flex-wrap: wrap;
  }
  .title {
    font-size: var(--sp-text-sm);
    font-weight: var(--sp-weight-bold);
  }
  .now {
    display: flex;
    align-items: center;
    gap: var(--sp-space-2);
    font-size: var(--sp-text-sm);
  }
  .sep {
    width: var(--sp-space-1);
  }
  .key {
    width: 10px;
    height: 10px;
    border-radius: 2px;
  }
  .plot {
    position: relative;
  }
  svg {
    display: block;
    outline: none;
    touch-action: pan-y;
  }
  svg:focus-visible {
    outline: 2px solid var(--sp-focus-ring);
    border-radius: var(--sp-radius-sm);
  }
  .grid {
    stroke: var(--sp-grid);
    stroke-width: 1;
  }
  .limit {
    stroke: var(--sp-text-faint);
    stroke-width: 1;
    stroke-dasharray: 4 3;
  }
  .cross {
    stroke: var(--sp-text-faint);
    stroke-width: 1;
  }
  .dot {
    stroke: var(--sp-bg-panel);
    stroke-width: 2;
  }
  .tick {
    fill: var(--sp-text-faint);
    font-size: 11px;
    font-variant-numeric: tabular-nums;
  }
  .empty {
    height: 100%;
    display: grid;
    place-items: center;
    font-size: var(--sp-text-sm);
    border: 1px dashed var(--sp-border);
    border-radius: var(--sp-radius-md);
  }
  .tip {
    position: absolute;
    top: 0;
    pointer-events: none;
    background: var(--sp-bg-elevated);
    border: 1px solid var(--sp-border-strong);
    border-radius: var(--sp-radius-md);
    box-shadow: var(--sp-shadow);
    padding: var(--sp-space-2) var(--sp-space-3);
    font-size: var(--sp-text-sm);
    min-width: 130px;
  }
  .tiprow {
    display: flex;
    align-items: center;
    gap: var(--sp-space-2);
  }
  .linekey {
    width: 12px;
    height: 2px;
    border-radius: 1px;
  }
  details summary {
    font-size: var(--sp-text-xs);
    cursor: pointer;
    width: max-content;
  }
  table {
    width: 100%;
    border-collapse: collapse;
    font-size: var(--sp-text-xs);
    font-variant-numeric: tabular-nums;
    margin-top: var(--sp-space-2);
  }
  th,
  td {
    text-align: left;
    padding: 2px var(--sp-space-2);
    border-bottom: 1px solid var(--sp-border);
  }
  th {
    color: var(--sp-text-muted);
  }
</style>
