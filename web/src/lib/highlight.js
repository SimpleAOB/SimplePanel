// Small tokenizers for the Query tab's syntax highlighting: SQL, mongosh-style
// queries and Redis commands. They return [{ t: kind, v: text }] so the editor
// can render plain text spans (no HTML is ever built from the query).
// Kinds: kw, str, num, com, fn, op, key, lit, ident, var, plain.

const SQL_KEYWORDS = new Set(
  `select from where and or not in is null like ilike between exists as on join inner left right full outer cross natural using
group by order asc desc nulls first last limit offset fetch next rows only having distinct all any some union intersect except
case when then else end with recursive values table lateral window over partition range preceding following unbounded current row
insert into update set delete create alter drop truncate grant revoke begin commit rollback transaction explain analyze describe show
primary key foreign references index view unique default check constraint returning cast collate interval true false unknown
filter within similar escape for share nowait skip locked straight_join sql_calc_found_rows high_priority databases tables columns status variables`
    .split(/\s+/),
);

const SQL_TYPES = new Set(
  `int integer bigint smallint tinyint decimal numeric real float double precision char varchar text boolean bool date time timestamp
timestamptz json jsonb uuid bytea serial bigserial blob enum`.split(/\s+/),
);

const REDIS_OPTIONS = new Set(
  `MATCH COUNT TYPE WITHSCORES WITHVALUES LIMIT BYSCORE BYLEX REV EX PX NX XX KEEPTTL GET IDX LEN MINMATCHLEN WITHMATCHLEN
STREAMS BLOCK ASC DESC ALPHA STORE BY`.split(/\s+/),
);

const MONGO_CTORS = new Set(['ObjectId', 'ISODate', 'Date', 'NumberLong', 'NumberInt', 'NumberDecimal', 'UUID', 'Timestamp', 'new']);
const MONGO_METHODS = new Set([
  'find', 'findOne', 'aggregate', 'countDocuments', 'estimatedDocumentCount', 'distinct', 'count',
  'sort', 'limit', 'skip', 'projection', 'project', 'pretty', 'toArray', 'getCollection', 'getSiblingDB',
]);

function push(out, t, v) {
  const last = out[out.length - 1];
  if (last && last.t === t && (t === 'plain' || t === 'com')) last.v += v;
  else out.push({ t, v });
}

function readString(s, i, q, backslash = true) {
  let j = i + 1;
  while (j < s.length) {
    if (backslash && s[j] === '\\') {
      j += 2;
      continue;
    }
    if (s[j] === q) {
      if (q === "'" && s[j + 1] === "'") {
        j += 2; // SQL '' escape
        continue;
      }
      return j + 1;
    }
    j++;
  }
  return s.length;
}

const isIdentStart = (c) => /[A-Za-z_$]/.test(c);
const isIdent = (c) => /[A-Za-z0-9_$]/.test(c);
const isDigit = (c) => c >= '0' && c <= '9';

export function highlightSQL(s, engine = 'postgres') {
  const out = [];
  let i = 0;
  while (i < s.length) {
    const c = s[i];
    const two = s.slice(i, i + 2);
    if (two === '--' || (c === '#' && engine !== 'postgres')) {
      let j = s.indexOf('\n', i);
      if (j < 0) j = s.length;
      push(out, 'com', s.slice(i, j));
      i = j;
    } else if (two === '/*') {
      let j = s.indexOf('*/', i + 2);
      j = j < 0 ? s.length : j + 2;
      push(out, 'com', s.slice(i, j));
      i = j;
    } else if (c === "'") {
      const j = readString(s, i, "'", engine !== 'postgres');
      push(out, 'str', s.slice(i, j));
      i = j;
    } else if (c === '"' || c === '`') {
      const j = readString(s, i, c, false);
      push(out, engine === 'postgres' || c === '`' ? 'ident' : 'str', s.slice(i, j));
      i = j;
    } else if (isDigit(c) || (c === '.' && isDigit(s[i + 1] || ''))) {
      let j = i + 1;
      while (j < s.length && /[0-9.eE_xXa-fA-F]/.test(s[j])) j++;
      push(out, 'num', s.slice(i, j));
      i = j;
    } else if (c === '$' && isDigit(s[i + 1] || '')) {
      let j = i + 1;
      while (j < s.length && isDigit(s[j])) j++;
      push(out, 'var', s.slice(i, j));
      i = j;
    } else if (isIdentStart(c)) {
      let j = i + 1;
      while (j < s.length && isIdent(s[j])) j++;
      const w = s.slice(i, j);
      const lw = w.toLowerCase();
      let k = j;
      while (k < s.length && (s[k] === ' ' || s[k] === '\t')) k++;
      if (lw === 'null' || lw === 'true' || lw === 'false') push(out, 'lit', w);
      else if (SQL_KEYWORDS.has(lw)) push(out, 'kw', w);
      else if (s[k] === '(') push(out, 'fn', w);
      else if (SQL_TYPES.has(lw)) push(out, 'kw', w);
      else push(out, 'plain', w);
      i = j;
    } else if ('=<>!+-*/%|&^~:'.includes(c)) {
      let j = i + 1;
      while (j < s.length && '=<>!|&:'.includes(s[j])) j++;
      push(out, 'op', s.slice(i, j));
      i = j;
    } else {
      push(out, 'plain', c);
      i++;
    }
  }
  return out;
}

export function highlightMongo(s) {
  const out = [];
  let i = 0;
  let prevSignificant = '';
  while (i < s.length) {
    const c = s[i];
    if (s.startsWith('//', i)) {
      let j = s.indexOf('\n', i);
      if (j < 0) j = s.length;
      push(out, 'com', s.slice(i, j));
      i = j;
      continue;
    }
    if (c === '"' || c === "'") {
      const j = readString(s, i, c);
      let k = j;
      while (k < s.length && /\s/.test(s[k])) k++;
      const v = s.slice(i, j);
      push(out, s[k] === ':' ? 'key' : v.slice(1).startsWith('$') ? 'op' : 'str', v);
      i = j;
      prevSignificant = 'v';
      continue;
    }
    // A regex literal can only start where a value is expected.
    if (c === '/' && (prevSignificant === '' || ':,([{'.includes(prevSignificant))) {
      let j = i + 1;
      let inClass = false;
      while (j < s.length && s[j] !== '\n') {
        if (s[j] === '\\') {
          j += 2;
          continue;
        }
        if (s[j] === '[') inClass = true;
        else if (s[j] === ']') inClass = false;
        else if (s[j] === '/' && !inClass) break;
        j++;
      }
      j = Math.min(j + 1, s.length);
      while (j < s.length && /[imsxu]/.test(s[j])) j++;
      push(out, 'str', s.slice(i, j));
      i = j;
      prevSignificant = 'v';
      continue;
    }
    if (isDigit(c) || ((c === '-' || c === '.') && isDigit(s[i + 1] || ''))) {
      let j = i + 1;
      while (j < s.length && /[0-9.eE+-]/.test(s[j])) j++;
      push(out, 'num', s.slice(i, j));
      i = j;
      prevSignificant = 'v';
      continue;
    }
    if (isIdentStart(c)) {
      let j = i + 1;
      while (j < s.length && isIdent(s[j])) j++;
      const w = s.slice(i, j);
      let k = j;
      while (k < s.length && /\s/.test(s[k])) k++;
      let t = 'plain';
      if (w === 'db' && prevSignificant === '') t = 'kw';
      else if (s[k] === ':') t = w.startsWith('$') ? 'op' : 'key';
      else if (w === 'true' || w === 'false' || w === 'null' || w === 'undefined') t = 'lit';
      else if (MONGO_CTORS.has(w)) t = 'fn';
      else if (s[k] === '(' && MONGO_METHODS.has(w)) t = 'fn';
      else if (prevSignificant === '.') t = 'ident';
      push(out, t, w);
      i = j;
      prevSignificant = 'v';
      continue;
    }
    if (!/\s/.test(c)) prevSignificant = c;
    push(out, '{}[]():,.'.includes(c) || /\s/.test(c) ? 'plain' : 'op', c);
    i++;
  }
  return out;
}

export function highlightRedis(s) {
  const out = [];
  let i = 0;
  let first = true;
  while (i < s.length) {
    const c = s[i];
    if (c === ' ' || c === '\t' || c === '\n' || c === '\r') {
      push(out, 'plain', c);
      i++;
      continue;
    }
    let j;
    if (c === '"' || c === "'") {
      j = readString(s, i, c, c === '"');
      push(out, 'str', s.slice(i, j));
    } else {
      j = i;
      while (j < s.length && !/\s/.test(s[j])) j++;
      const w = s.slice(i, j);
      if (first) push(out, 'kw', w);
      else if (REDIS_OPTIONS.has(w.toUpperCase())) push(out, 'fn', w);
      else if (/^-?\d+(\.\d+)?$/.test(w)) push(out, 'num', w);
      else push(out, 'plain', w);
    }
    first = false;
    i = j;
  }
  return out;
}

export function highlight(language, text, engine) {
  if (language === 'mongo') return highlightMongo(text);
  if (language === 'redis') return highlightRedis(text);
  return highlightSQL(text, engine);
}
