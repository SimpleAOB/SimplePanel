<script>
  import { copyText } from '../lib/clipboard.js';
  // The database Query tab: a read-only table browser and query box.
  // Queries run on the server as a read-only database user; see
  // internal/deploy/dbquery.go.
  import { onMount } from 'svelte';
  import { api } from '../lib/api.js';
  import { ui, data, timeAgo } from '../lib/state.svelte.js';
  import Icon from './Icon.svelte';
  import CodeEditor from './CodeEditor.svelte';
  import ResultsGrid from './ResultsGrid.svelte';

  let { aid, appName, engine, canDeploy = false } = $props();

  const PAGE = 100;
  const examples = {
    sql: 'SELECT *\nFROM my_table\nWHERE created_at > now() - interval \'7 days\'\nORDER BY id DESC',
    mysql: 'SELECT *\nFROM my_table\nWHERE created_at > NOW() - INTERVAL 7 DAY\nORDER BY id DESC',
    mongo: 'db.my_collection.find({ status: "active" }).sort({ _id: -1 }).limit(50)',
    redis: 'HGETALL user:1',
  };

  function load(key, fallback) {
    try {
      return JSON.parse(localStorage.getItem(key)) ?? fallback;
    } catch {
      return fallback;
    }
  }
  function store(key, v) {
    try {
      localStorage.setItem(key, JSON.stringify(v));
    } catch {
      /* storage unavailable */
    }
  }

  let info = $state(null);
  let fatal = $state('');
  let language = $derived(info?.language || 'sql');
  let isSQL = $derived(language === 'sql');
  let pg = $derived(engine === 'postgres');

  // Left column
  let side = $state('tables');
  let tables = $state(null); // QueryResult
  let tablesErr = $state('');
  let tablesBusy = $state(false);
  let tableFilter = $state('');
  let redisPattern = $state('*');
  let mongoDb = $state('');
  let expanded = $state(''); // key of the table whose columns are shown
  let columnsFor = $state({}); // key -> rows | 'loading' | error string
  let saved = $state([]);
  let history = $state([]);

  // Editor
  let query = $state(load(`sp-query-draft-${aid}`, ''));
  let limits = $state(load('sp-query-limits', { timeout: 30, rows: 1000 }));
  let savingName = $state(null); // string while the save form is open
  let saveErr = $state('');

  // Results
  let res = $state(null); // { columns, rows, rowCount, truncated, ms, limit, explain, label, browse }
  let runErr = $state('');
  let running = $state(false);
  let browse = $state(null); // { t, page, sort, filter }
  let browseFilter = $state('');
  let exportOpen = $state(false);
  let copied = $state('');

  $effect(() => store(`sp-query-draft-${aid}`, query));
  $effect(() => store('sp-query-limits', limits));

  onMount(async () => {
    try {
      info = await api.get(`/apps/${aid}/query`);
      mongoDb = info.database || '';
    } catch (e) {
      fatal = e.message;
      return;
    }
    loadTables();
    if (ui.advanced) loadSide();
  });

  $effect(() => {
    if (!ui.advanced && side !== 'tables') side = 'tables';
  });

  async function loadSide() {
    try {
      [saved, history] = await Promise.all([api.get(`/apps/${aid}/query/saved`), api.get(`/apps/${aid}/query/history`)]);
    } catch {
      /* shown when opened */
    }
  }

  async function loadTables() {
    tablesBusy = true;
    tablesErr = '';
    try {
      const p = language === 'redis' ? `?pattern=${encodeURIComponent(redisPattern || '*')}` : '';
      tables = await api.get(`/apps/${aid}/query/tables${p}`);
      if (language === 'mongo') {
        const dbs = mongoDbs();
        if (dbs.length && !dbs.includes(mongoDb)) mongoDb = dbs[0];
      }
    } catch (e) {
      tablesErr = e.message;
    } finally {
      tablesBusy = false;
    }
  }

  // Tables as objects: { key, schema, name, type, rows }
  let tableList = $derived.by(() => {
    if (!tables) return [];
    const c = tables.columns;
    const at = (row, n) => {
      const i = c.indexOf(n);
      return i < 0 ? '' : row[i];
    };
    const list = tables.rows.map((r) => {
      const schema = at(r, 'schema') || '';
      const name = at(r, 'name');
      return { key: schema ? `${schema}.${name}` : name, schema, name, type: at(r, 'type') || '', rows: at(r, 'approx_rows') };
    });
    // SQL databases already sort; Mongo and Redis (SCAN) don't.
    return language === 'sql' ? list : list.sort((a, b) => a.key.localeCompare(b.key));
  });
  function mongoDbs() {
    return [...new Set(tableList.map((t) => t.schema))];
  }
  let visibleTables = $derived.by(() => {
    const f = tableFilter.trim().toLowerCase();
    return tableList.filter((t) => (language !== 'mongo' || t.schema === mongoDb) && (!f || t.key.toLowerCase().includes(f)));
  });
  let multiSchema = $derived(pg && new Set(tableList.map((t) => t.schema)).size > 1);

  // ---- Quoting ----
  const qid = (s) => (pg ? `"${s.replaceAll('"', '""')}"` : '`' + s.replaceAll('`', '``') + '`');
  const tableRef = (t) => (pg ? `${qid(t.schema || 'public')}.${qid(t.name)}` : qid(t.name));
  const js = (s) => JSON.stringify(s);
  const redisArg = (s) => (/^[^\s"'\\]+$/.test(s) ? s : `"${s.replaceAll('\\', '\\\\').replaceAll('"', '\\"').replaceAll('\n', '\\n')}"`);

  function browseQuery(b) {
    const off = b.page * PAGE;
    if (language === 'mongo') {
      const sort = b.sort ? `.sort({ ${js(b.sort.col)}: ${b.sort.dir === 'asc' ? 1 : -1} })` : '';
      return `db.getSiblingDB(${js(b.t.schema)}).getCollection(${js(b.t.name)}).find(${b.filter || '{}'})${sort}.skip(${off}).limit(${PAGE + 1})`;
    }
    const where = b.filter ? `\nWHERE ${b.filter}` : '';
    const order = b.sort ? `\nORDER BY ${qid(b.sort.col)} ${b.sort.dir === 'asc' ? 'ASC' : 'DESC'}` : '';
    return `SELECT *\nFROM ${tableRef(b.t)}${where}${order}\nLIMIT ${PAGE + 1} OFFSET ${off}`;
  }

  async function execute(q, { explain = false, source = 'editor', limit, label = '' } = {}) {
    running = true;
    runErr = '';
    exportOpen = false;
    try {
      const r = await api.post(`/apps/${aid}/query`, {
        query: q,
        explain,
        source,
        timeout: Number(limits.timeout) || 30,
        limit: limit ?? (Number(limits.rows) || 1000),
        database: language === 'mongo' ? mongoDb : '',
      });
      res = { ...r.result, ms: r.ms, limit: r.limit, explain, label, browse: source === 'browse' };
      return true;
    } catch (e) {
      runErr = e.message;
      return false;
    } finally {
      running = false;
      if (source !== 'browse' && ui.advanced) {
        api.get(`/apps/${aid}/query/history`).then((h) => (history = h)).catch(() => {});
      }
    }
  }

  function run(text = query, explain = false) {
    if (!text.trim()) return;
    browse = null;
    execute(text, { explain, label: explain ? 'Query plan' : 'Query result' });
  }

  async function openTable(t) {
    if (language === 'redis') return openKey(t.name);
    browseFilter = '';
    browse = { t, page: 0, sort: null, filter: '' };
    loadBrowse();
    if (isSQL) toggleColumns(t, true);
  }

  function loadBrowse() {
    if (!browse) return;
    execute(browseQuery(browse), { source: 'browse', limit: PAGE, label: browse.t.key });
  }

  function setBrowseSort(s) {
    browse = { ...browse, sort: s, page: 0 };
    loadBrowse();
  }
  function setPage(p) {
    browse = { ...browse, page: p };
    loadBrowse();
  }
  function applyFilter(e) {
    e.preventDefault();
    browse = { ...browse, filter: browseFilter.trim(), page: 0 };
    loadBrowse();
  }

  // Redis: look up the key's type, then show its value.
  async function openKey(key) {
    browse = null;
    running = true;
    runErr = '';
    let cmd;
    try {
      const r = await api.post(`/apps/${aid}/query`, { query: `TYPE ${redisArg(key)}`, source: 'browse' });
      const type = r.result.rows[0]?.[0];
      const k = redisArg(key);
      cmd = {
        string: `GET ${k}`,
        hash: `HGETALL ${k}`,
        list: `LRANGE ${k} 0 ${PAGE - 1}`,
        set: `SMEMBERS ${k}`,
        zset: `ZRANGE ${k} 0 -1 WITHSCORES`,
        stream: `XRANGE ${k} - + COUNT ${PAGE}`,
      }[type];
      if (!cmd) {
        running = false;
        res = { columns: ['key', 'type'], rows: [[key, type || 'none']], rowCount: 1, ms: r.ms, label: key, browse: true };
        return;
      }
    } catch (e) {
      running = false;
      runErr = e.message;
      return;
    }
    await execute(cmd, { source: 'browse', label: key });
    if (res) res.redisCmd = cmd;
  }

  async function toggleColumns(t, open = false) {
    if (!isSQL) return;
    if (expanded === t.key && !open) {
      expanded = '';
      return;
    }
    expanded = t.key;
    if (columnsFor[t.key] && typeof columnsFor[t.key] !== 'string') return;
    columnsFor[t.key] = 'loading';
    try {
      const r = await api.get(`/apps/${aid}/query/columns?table=${encodeURIComponent(pg ? `${t.schema}.${t.name}` : t.name)}`);
      columnsFor[t.key] = r.rows.map((row) => ({ name: row[0], type: row[1], key: row[4] || '' }));
    } catch (e) {
      columnsFor[t.key] = e.message;
    }
  }

  function toEditor() {
    query = res?.redisCmd || (browse ? browseQuery({ ...browse, page: 0 }).replace(/\nLIMIT \d+ OFFSET 0$/, '\nLIMIT 100').replace(/\.skip\(0\)\.limit\(\d+\)$/, '.limit(100)') : query);
  }

  // ---- Saved queries and history ----
  async function saveQuery(e) {
    e.preventDefault();
    saveErr = '';
    try {
      await api.post(`/apps/${aid}/query/saved`, { name: savingName, query });
      savingName = null;
      saved = await api.get(`/apps/${aid}/query/saved`);
      side = 'saved';
    } catch (e2) {
      saveErr = e2.message;
    }
  }
  async function removeSaved(s) {
    try {
      await api.del(`/saved-queries/${s.id}`);
      saved = saved.filter((x) => x.id !== s.id);
    } catch (e) {
      runErr = e.message;
    }
  }
  async function clearHistory() {
    await api.del(`/apps/${aid}/query/history`).catch(() => {});
    history = [];
  }
  function pick(q) {
    query = q;
  }

  // ---- Export ----
  function objects() {
    return res.rows.map((r) => Object.fromEntries(res.columns.map((c, i) => [c, r[i] ?? null])));
  }
  function csv() {
    const cell = (v) => {
      if (v === null || v === undefined) return '';
      const s = String(v);
      return /[",\n\r]/.test(s) ? `"${s.replaceAll('"', '""')}"` : s;
    };
    return [res.columns.map(cell).join(','), ...res.rows.map((r) => r.map(cell).join(','))].join('\r\n') + '\r\n';
  }
  function fileBase() {
    const what = (res.label && res.label !== 'Query result' ? res.label : 'query').replace(/[^\w.-]+/g, '_');
    const d = new Date();
    const stamp = `${d.getFullYear()}${String(d.getMonth() + 1).padStart(2, '0')}${String(d.getDate()).padStart(2, '0')}-${String(d.getHours()).padStart(2, '0')}${String(d.getMinutes()).padStart(2, '0')}`;
    return `${appName}-${what}-${stamp}`;
  }
  function download(kind) {
    const text = kind === 'csv' ? csv() : JSON.stringify(objects(), null, 2) + '\n';
    const url = URL.createObjectURL(new Blob([text], { type: kind === 'csv' ? 'text/csv' : 'application/json' }));
    const a = document.createElement('a');
    a.href = url;
    a.download = `${fileBase()}.${kind}`;
    a.click();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
    exportOpen = false;
  }
  async function copyAs(kind) {
    try {
      await copyText(kind === 'csv' ? csv() : JSON.stringify(objects(), null, 2));
      copied = kind;
      setTimeout(() => (copied = ''), 1200);
    } catch {
      /* clipboard unavailable */
    }
    exportOpen = false;
  }

  let planText = $derived.by(() => {
    if (!res?.explain || res.columns.length !== 1) return '';
    const lines = res.rows.map((r) => r[0] ?? '');
    if (lines.length === 1) {
      try {
        return JSON.stringify(JSON.parse(lines[0]), null, 2);
      } catch {
        /* plain text plan */
      }
    }
    return lines.join('\n');
  });

  let placeholder = $derived(examples[language === 'sql' && !pg ? 'mysql' : language]);
  let langLabel = $derived({ sql: pg ? 'PostgreSQL' : engine === 'mariadb' ? 'MariaDB' : 'MySQL', mongo: 'MongoDB', redis: 'Redis' }[language]);
  const fmtRows = (n) => {
    if (n === '' || n === null || n === undefined) return '';
    const x = Number(n);
    if (isNaN(x) || x < 0) return '';
    return x >= 10000 ? `~${(x / 1000).toFixed(x >= 100000 ? 0 : 1)}k` : `~${x}`;
  };
</script>

{#if fatal}
  <section class="card"><p class="error">{fatal}</p></section>
{:else if !info}
  <section class="card"><p class="muted">Loading…</p></section>
{:else}
  <div class="qv">
    <aside class="side card">
      {#if ui.advanced}
        <div class="segmented side-tabs" role="tablist">
          <button role="tab" aria-selected={side === 'tables'} class:active={side === 'tables'} onclick={() => (side = 'tables')}>{language === 'redis' ? 'Keys' : language === 'mongo' ? 'Collections' : 'Tables'}</button>
          <button role="tab" aria-selected={side === 'saved'} class:active={side === 'saved'} onclick={() => (side = 'saved')}>Saved</button>
          <button role="tab" aria-selected={side === 'history'} class:active={side === 'history'} onclick={() => (side = 'history')}>History</button>
        </div>
      {:else}
        <div class="row between">
          <h3>{language === 'redis' ? 'Keys' : language === 'mongo' ? 'Collections' : 'Tables'}</h3>
          <button class="btn ghost icon small" title="Refresh" aria-label="Refresh" disabled={tablesBusy} onclick={loadTables}><Icon name="refresh" size={14} /></button>
        </div>
      {/if}

      {#if side === 'tables'}
        {#if language === 'mongo' && mongoDbs().length > 1}
          <select bind:value={mongoDb} aria-label="Database">
            {#each mongoDbs() as d (d)}<option value={d}>{d}</option>{/each}
          </select>
        {/if}
        {#if language === 'redis'}
          <form class="row tight" onsubmit={(e) => { e.preventDefault(); loadTables(); }}>
            <input type="text" class="mono" bind:value={redisPattern} placeholder="user:*" aria-label="Key pattern" />
            <button class="btn small">Scan</button>
          </form>
        {:else}
          <input type="search" bind:value={tableFilter} placeholder="Filter" aria-label="Filter tables" />
        {/if}
        {#if tablesErr}
          <p class="error small">{tablesErr}</p>
        {:else if !tables}
          <p class="faint small">Loading…</p>
        {:else if visibleTables.length === 0}
          <p class="faint small">{tableFilter ? 'No matches.' : language === 'redis' ? 'No keys match.' : 'Nothing here yet.'}</p>
        {/if}
        <ul class="tlist">
          {#each visibleTables as t (t.key)}
            {@const active = (browse && browse.t.key === t.key) || (language === 'redis' && res?.label === t.name)}
            <li>
              <div class="trow" class:active>
                {#if isSQL}
                  <button class="caret" aria-label="Show columns of {t.name}" aria-expanded={expanded === t.key} onclick={() => toggleColumns(t)}>
                    <span class:open={expanded === t.key}>›</span>
                  </button>
                {/if}
                <button class="tname" onclick={() => openTable(t)} title={t.key}>
                  <Icon name={language === 'redis' ? 'key' : 'db'} size={13} />
                  <span class="nm">{multiSchema && t.schema !== 'public' ? `${t.schema}.` : ''}{t.name}</span>
                  {#if t.type === 'VIEW' || t.type === 'view'}<span class="tag">view</span>{/if}
                  <span class="cnt">{fmtRows(t.rows)}</span>
                </button>
              </div>
              {#if expanded === t.key}
                {@const cols = columnsFor[t.key]}
                {#if cols === 'loading'}
                  <p class="faint small cols">Loading…</p>
                {:else if typeof cols === 'string'}
                  <p class="error small cols">{cols}</p>
                {:else if cols}
                  <ul class="cols">
                    {#each cols as c (c.name)}
                      <li><span class="mono">{c.name}</span>{#if c.key === 'PRI'}<span class="tag">key</span>{/if}<span class="faint">{c.type}</span></li>
                    {/each}
                  </ul>
                {/if}
              {/if}
            </li>
          {/each}
        </ul>
        {#if tables?.truncated}<p class="faint small">Showing the first {tables.rowCount}. Narrow it with a pattern.</p>{/if}
      {:else if side === 'saved'}
        {#if saved.length === 0}
          <p class="faint small">No saved queries yet. Write one and press Save. Everyone who can query this database sees them.</p>
        {/if}
        <ul class="plist">
          {#each saved as s (s.id)}
            <li>
              <button class="pitem" onclick={() => pick(s.query)} title="Put in the query box">
                <span class="pname">{s.name}</span>
                <span class="pq mono">{s.query}</span>
                <span class="faint tiny">{s.username || 'removed user'} · {timeAgo(s.createdAt)}</span>
              </button>
              {#if s.userId === data.user?.id || canDeploy}
                <button class="btn ghost icon small" title="Remove {s.name}" aria-label="Remove {s.name}" onclick={() => removeSaved(s)}><Icon name="trash" size={14} /></button>
              {/if}
            </li>
          {/each}
        </ul>
      {:else}
        {#if history.length === 0}
          <p class="faint small">Queries you run here are listed for you only.</p>
        {:else}
          <div class="row end"><button class="btn ghost small" onclick={clearHistory}>Clear</button></div>
        {/if}
        <ul class="plist">
          {#each history as h (h.id)}
            <li>
              <button class="pitem" onclick={() => pick(h.query)} title="Put in the query box">
                <span class="pq mono"><span class="dot" class:failed={!h.ok} class:succeeded={h.ok}></span>{h.query}</span>
                <span class="faint tiny">{h.ok ? `${h.rows} row${h.rows === 1 ? '' : 's'} · ${h.ms} ms` : h.error} · {timeAgo(h.createdAt)}</span>
              </button>
            </li>
          {/each}
        </ul>
      {/if}
    </aside>

    <div class="main">
      <section class="card editor-card">
        <div class="row between">
          <h3>Query <span class="lang">{langLabel}</span></h3>
          <span class="ro"><Icon name="lock" size={13} /> Read-only</span>
        </div>
        <CodeEditor bind:value={query} {language} {engine} {placeholder} label="{langLabel} query" rows={ui.advanced ? 8 : 6} onrun={(t) => run(t)} />
        <div class="row toolbar">
          <button class="btn primary" disabled={running || !query.trim()} onclick={() => run()}><Icon name="play" size={14} /> {running ? 'Running…' : 'Run'}</button>
          {#if ui.advanced && info.explain}
            <button class="btn" disabled={running || !query.trim()} onclick={() => run(query, true)} title="Show how the database would run this query">Explain</button>
          {/if}
          {#if ui.advanced}
            <button class="btn" disabled={!query.trim()} onclick={() => { saveErr = ''; savingName = savingName === null ? '' : null; }}>Save</button>
          {/if}
          <span class="faint hint">Ctrl+Enter runs the query, or the selected part of it.</span>
          {#if ui.advanced}
            <span class="limits">
              <label title="Stop the query after this many seconds">Timeout <input type="number" min="1" max={info.maxTimeout} bind:value={limits.timeout} /> s</label>
              <label title="Return at most this many rows">Rows <input type="number" min="1" max={info.maxRows} step="100" bind:value={limits.rows} /></label>
            </span>
          {/if}
        </div>
        {#if savingName !== null}
          <form class="row tight saveform" onsubmit={saveQuery}>
            <!-- svelte-ignore a11y_autofocus -->
            <input type="text" bind:value={savingName} maxlength="80" placeholder="Name, e.g. Paid orders this week" aria-label="Query name" autofocus required />
            <button class="btn primary small">Save query</button>
            <button type="button" class="btn ghost small" onclick={() => (savingName = null)}>Cancel</button>
          </form>
          {#if saveErr}<p class="error small">{saveErr}</p>{/if}
        {/if}
        {#if !ui.advanced}
          <small class="faint">Runs as a read-only user, stops after {info.defaultTimeout} seconds and returns up to {info.defaultRows.toLocaleString()} rows. Turn on Advanced view for saved queries, history, Explain and limits.</small>
        {/if}
      </section>

      <section class="card results">
        {#if browse}
          <form class="row tight filter" onsubmit={applyFilter}>
            <span class="faint fl">{language === 'mongo' ? 'find(' : 'WHERE'}</span>
            <input type="text" class="mono" bind:value={browseFilter} placeholder={language === 'mongo' ? '{ status: "paid" }' : "status = 'paid'"} aria-label="Filter rows" />
            {#if language === 'mongo'}<span class="faint fl">)</span>{/if}
            <button class="btn small">Filter</button>
          </form>
        {/if}
        <div class="row between rhead">
          <div class="rtitle">
            {#if res}
              <strong class="mono">{res.label}</strong>
              <span class="faint">
                {#if res.browse && browse}
                  {res.rowCount === 0 ? 'No rows' : `Rows ${browse.page * PAGE + 1}–${browse.page * PAGE + res.rowCount}`}
                {:else}
                  {res.rowCount.toLocaleString()} row{res.rowCount === 1 ? '' : 's'}
                {/if}
                · {res.ms} ms
              </span>
            {:else}
              <strong>Results</strong>
            {/if}
          </div>
          {#if res}
            <div class="row tight ractions">
              {#if browse}
                <button class="btn small" disabled={running || browse.page === 0} onclick={() => setPage(browse.page - 1)}>Previous</button>
                <button class="btn small" disabled={running || !res.truncated} onclick={() => setPage(browse.page + 1)}>Next</button>
              {/if}
              {#if res.browse}
                <button class="btn ghost small" onclick={toEditor} title="Copy this into the query box">Open in query box</button>
              {/if}
              <div class="menu-wrap">
                <button class="btn small" disabled={!res.columns.length} aria-haspopup="menu" aria-expanded={exportOpen} onclick={() => (exportOpen = !exportOpen)}><Icon name="download" size={14} /> Export</button>
                {#if exportOpen}
                  <div class="menu" role="menu">
                    <button role="menuitem" onclick={() => download('csv')}>Download CSV</button>
                    <button role="menuitem" onclick={() => download('json')}>Download JSON</button>
                    <button role="menuitem" onclick={() => copyAs('csv')}>Copy as CSV</button>
                    <button role="menuitem" onclick={() => copyAs('json')}>Copy as JSON</button>
                    <p class="faint tiny">Exports the {res.rowCount.toLocaleString()} rows shown.</p>
                  </div>
                {/if}
              </div>
              {#if copied}<span class="ok small">Copied</span>{/if}
            </div>
          {/if}
        </div>

        {#if runErr}
          <pre class="qerr">{runErr}</pre>
        {/if}
        {#if res && !runErr}
          {#if res.truncated && !res.browse}
            <p class="note small">Showing the first {res.limit.toLocaleString()} rows. {ui.advanced ? 'Raise the row limit above, or' : 'Turn on Advanced view to raise the limit, or'} narrow the query.</p>
          {/if}
          {#if planText}
            <pre class="plan">{planText}</pre>
          {:else if res.columns.length === 0}
            <p class="muted small">The query ran and returned no columns.</p>
          {:else if res.rows.length === 0}
            <p class="muted small">No rows.</p>
          {:else}
            <ResultsGrid columns={res.columns} rows={res.rows} sort={browse?.sort} onsort={browse && language !== 'redis' ? setBrowseSort : null} offset={browse ? browse.page * PAGE : 0} />
          {/if}
        {:else if !res && !runErr}
          <p class="muted small empty">
            {#if running}Running…{:else}Pick a {language === 'redis' ? 'key' : language === 'mongo' ? 'collection' : 'table'} on the left to browse it, or write a query and press Run.{/if}
          </p>
        {/if}
      </section>
    </div>
  </div>
{/if}

<svelte:window onclick={(e) => { if (exportOpen && !e.target.closest?.('.menu-wrap')) exportOpen = false; }} />

<style>
  .qv {
    display: grid;
    grid-template-columns: 250px minmax(0, 1fr);
    gap: var(--sp-space-4);
    align-items: start;
  }
  .side {
    display: flex;
    flex-direction: column;
    gap: var(--sp-space-2);
    position: sticky;
    top: var(--sp-space-4);
    max-height: calc(100vh - 160px);
    overflow: auto;
    padding: var(--sp-space-3);
  }
  .side h3 {
    margin: 0;
  }
  .side-tabs {
    width: 100%;
  }
  .side-tabs button {
    flex: 1;
  }
  .main {
    display: flex;
    flex-direction: column;
    gap: var(--sp-space-4);
    min-width: 0;
  }
  .card {
    display: flex;
    flex-direction: column;
    gap: var(--sp-space-3);
  }
  h3 {
    display: flex;
    align-items: center;
    gap: var(--sp-space-2);
  }
  .lang {
    font-size: var(--sp-text-xs);
    font-weight: var(--sp-weight-medium);
    color: var(--sp-text-muted);
    border: 1px solid var(--sp-border-strong);
    border-radius: var(--sp-radius-round);
    padding: 1px var(--sp-space-2);
  }
  .ro {
    display: inline-flex;
    align-items: center;
    gap: var(--sp-space-1);
    font-size: var(--sp-text-xs);
    font-weight: var(--sp-weight-bold);
    color: var(--sp-success);
  }
  .toolbar {
    align-items: center;
    gap: var(--sp-space-2);
  }
  .hint {
    font-size: var(--sp-text-xs);
  }
  .limits {
    margin-left: auto;
    display: flex;
    gap: var(--sp-space-3);
    font-size: var(--sp-text-xs);
    color: var(--sp-text-muted);
  }
  .limits label {
    display: flex;
    align-items: center;
    gap: var(--sp-space-1);
  }
  .limits input {
    width: 76px;
    height: 28px;
    padding: 0 var(--sp-space-2);
  }
  .row.tight {
    gap: var(--sp-space-2);
    flex-wrap: nowrap;
    align-items: center;
  }
  .row.tight input {
    flex: 1;
    min-width: 0;
  }
  .between {
    justify-content: space-between;
    align-items: center;
  }
  .end {
    justify-content: flex-end;
  }
  .small {
    font-size: var(--sp-text-sm);
  }
  .tiny {
    font-size: var(--sp-text-xs);
  }
  .tlist,
  .cols,
  .plist {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  .trow {
    display: flex;
    align-items: center;
    border-radius: var(--sp-radius-sm);
  }
  .trow:hover {
    background: var(--sp-bg-hover);
  }
  .trow.active {
    background: var(--sp-bg-active);
  }
  .caret {
    all: unset;
    width: 18px;
    text-align: center;
    color: var(--sp-text-faint);
    cursor: pointer;
    flex: none;
  }
  .caret span {
    display: inline-block;
    transition: transform var(--sp-transition);
  }
  .caret span.open {
    transform: rotate(90deg);
  }
  .caret:focus-visible,
  .tname:focus-visible,
  .pitem:focus-visible {
    outline: 2px solid var(--sp-focus-ring);
  }
  .tname {
    all: unset;
    box-sizing: border-box;
    flex: 1;
    min-width: 0;
    display: flex;
    align-items: center;
    gap: var(--sp-space-2);
    padding: 5px var(--sp-space-2) 5px 2px;
    font-size: var(--sp-text-sm);
    color: var(--sp-text);
    cursor: pointer;
  }
  .nm {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .cnt {
    font-size: var(--sp-text-xs);
    color: var(--sp-text-faint);
  }
  .tag {
    font-size: 10px;
    text-transform: uppercase;
    letter-spacing: 0.03em;
    color: var(--sp-text-muted);
    border: 1px solid var(--sp-border-strong);
    border-radius: var(--sp-radius-sm);
    padding: 0 4px;
  }
  .cols {
    margin: 0 0 var(--sp-space-2) 28px;
    border-left: 1px solid var(--sp-border);
    padding-left: var(--sp-space-2);
  }
  .cols li {
    display: flex;
    gap: var(--sp-space-2);
    align-items: center;
    font-size: var(--sp-text-xs);
    padding: 2px 0;
  }
  .cols li .faint {
    margin-left: auto;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    min-width: 0;
    max-width: 55%;
  }
  .cols li .mono {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .plist li {
    display: flex;
    align-items: flex-start;
    gap: var(--sp-space-1);
    border-top: 1px solid var(--sp-border);
  }
  .plist li:first-child {
    border-top: 0;
  }
  .pitem {
    all: unset;
    box-sizing: border-box;
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
    gap: 2px;
    padding: var(--sp-space-2) var(--sp-space-1);
    cursor: pointer;
    border-radius: var(--sp-radius-sm);
  }
  .pitem:hover {
    background: var(--sp-bg-hover);
  }
  .pname {
    font-size: var(--sp-text-sm);
    font-weight: var(--sp-weight-medium);
  }
  .pq {
    font-size: var(--sp-text-xs);
    color: var(--sp-text-muted);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    display: flex;
    align-items: center;
    gap: var(--sp-space-2);
  }
  .pq .dot {
    flex: none;
  }
  .filter .fl {
    font-family: var(--sp-font-mono);
    font-size: var(--sp-text-xs);
  }
  .rhead {
    gap: var(--sp-space-2);
    flex-wrap: wrap;
  }
  .rtitle {
    display: flex;
    gap: var(--sp-space-2);
    align-items: baseline;
    flex-wrap: wrap;
    font-size: var(--sp-text-sm);
  }
  .ractions {
    flex-wrap: wrap !important;
    justify-content: flex-end;
  }
  .menu-wrap {
    position: relative;
  }
  .menu {
    position: absolute;
    right: 0;
    top: calc(100% + 4px);
    z-index: 5;
    min-width: 190px;
    background: var(--sp-bg-elevated);
    border: 1px solid var(--sp-border-strong);
    border-radius: var(--sp-radius-md);
    box-shadow: var(--sp-shadow);
    padding: var(--sp-space-1);
    display: flex;
    flex-direction: column;
  }
  .menu button {
    all: unset;
    padding: 6px var(--sp-space-3);
    font-size: var(--sp-text-sm);
    border-radius: var(--sp-radius-sm);
    cursor: pointer;
  }
  .menu button:hover,
  .menu button:focus-visible {
    background: var(--sp-bg-hover);
  }
  .menu p {
    margin: var(--sp-space-1) var(--sp-space-3);
  }
  .qerr {
    margin: 0;
    padding: var(--sp-space-3);
    border: 1px solid var(--sp-danger);
    border-radius: var(--sp-radius-md);
    color: var(--sp-danger);
    background: var(--sp-bg-code);
    font-family: var(--sp-font-mono);
    font-size: var(--sp-text-xs);
    white-space: pre-wrap;
    word-break: break-word;
  }
  .plan {
    margin: 0;
    padding: var(--sp-space-3);
    max-height: 60vh;
    overflow: auto;
    background: var(--sp-bg-code);
    border: 1px solid var(--sp-border);
    border-radius: var(--sp-radius-md);
    font-family: var(--sp-font-mono);
    font-size: var(--sp-text-xs);
  }
  .note {
    margin: 0;
    color: var(--sp-warning);
  }
  .ok {
    color: var(--sp-success);
  }
  .empty {
    padding: var(--sp-space-5) 0;
    text-align: center;
  }
  @media (max-width: 900px) {
    .qv {
      grid-template-columns: minmax(0, 1fr);
    }
    .side {
      position: static;
      max-height: 320px;
    }
    .limits {
      margin-left: 0;
    }
    .hint {
      display: none;
    }
  }
</style>
