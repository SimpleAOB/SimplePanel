// Helpers for the log viewer: parsing Docker's timestamped lines, spotting
// log levels, searching and saving logs.

const TS = /^(\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d+)?(?:Z|[+-]\d\d:\d\d)) /;

// Words that mark a line's level. Kept strict (whole words, common
// formats) so ordinary text like "0 errors" isn't flagged.
const ERROR =
  /\[(?:error|err|fatal|crit(?:ical)?|panic|emerg)\]|\b(?:ERROR|ERR|FATAL|CRIT(?:ICAL)?|PANIC|EMERG(?:ENCY)?|ALERT|SEVERE)\b|\blevel[=:]"?(?:error|fatal|crit|panic)|"level":\s*"(?:error|fatal|critical|panic)"|\b[Ee]rror:|^Traceback \(most recent call last\)|\bUnhandled(?:Promise)?Rejection|\b[A-Z]\w*(?:Error|Exception)\b(?=[:\s])/;
const WARN = /\[warn(?:ing)?\]|\b(?:WARN(?:ING)?)\b|\blevel[=:]"?warn|"level":\s*"warn(?:ing)?"|\b[Ww]arning:/;
const DEBUG = /\[(?:debug|trace)\]|\b(?:DEBUG|TRACE)\b|\blevel[=:]"?(?:debug|trace)|"level":\s*"(?:debug|trace)"/;

const BRACKET = /\[[a-z]+\]/i;

export function levelOf(text) {
  // "[ERROR]" and "[error]" are both common; check bracketed tags lowercased.
  const tag = BRACKET.exec(text.slice(0, 40))?.[0].toLowerCase() || '';
  if (ERROR.test(text) || ERROR.test(tag)) return 'error';
  if (WARN.test(text) || WARN.test(tag)) return 'warn';
  if (DEBUG.test(text) || DEBUG.test(tag)) return 'debug';
  return '';
}

// Splits log text into lines of { ts, text, level }. Indented lines right
// after an error (stack traces) count as part of it, so a whole trace
// stays highlighted.
// Terminal colour codes, which builds print and browsers can't show.
const ANSI = /\x1b\[[0-9;?]*[ -\/]*[@-~]/g;
export function stripAnsi(s) {
  return s ? s.replace(ANSI, '') : s;
}

export function parseLog(raw) {
  const out = [];
  if (!raw) return out;
  const lines = stripAnsi(raw).split('\n');
  if (lines[lines.length - 1] === '') lines.pop();
  let prev = '';
  for (const l of lines) {
    const m = TS.exec(l);
    let ts = null;
    let text = l;
    if (m) {
      ts = new Date(m[1]);
      text = l.slice(m[0].length);
    }
    let level = levelOf(text);
    if (!level && prev === 'error' && /^\s+\S/.test(text)) level = 'error';
    out.push({ ts, raw: l, text, level });
    prev = level;
  }
  return out;
}

const pad = (n, w = 2) => String(n).padStart(w, '0');

// Short local time for a log line, with the date when it isn't today.
export function formatTs(d) {
  if (!d || isNaN(d)) return '';
  const now = new Date();
  const t = `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}.${pad(d.getMilliseconds(), 3)}`;
  if (d.toDateString() === now.toDateString()) return t;
  return `${d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' })} ${t}`;
}

// Builds a matcher for the search box. Returns null for an empty query and
// { error } for a bad regular expression.
export function matcher(query, { regex = false, caseSensitive = false } = {}) {
  if (!query) return null;
  const flags = caseSensitive ? 'g' : 'gi';
  try {
    return { re: new RegExp(regex ? query : query.replace(/[.*+?^${}()|[\]\\]/g, '\\$&'), flags) };
  } catch (err) {
    return { error: err.message };
  }
}

// Splits text into [{ text, hit }] parts around the matcher's matches.
export function highlight(text, re) {
  if (!re) return [{ text, hit: false }];
  const parts = [];
  let last = 0;
  re.lastIndex = 0;
  let m;
  while ((m = re.exec(text))) {
    if (m[0] === '') {
      re.lastIndex++;
      continue;
    }
    if (m.index > last) parts.push({ text: text.slice(last, m.index), hit: false });
    parts.push({ text: m[0], hit: true });
    last = m.index + m[0].length;
  }
  if (last < text.length) parts.push({ text: text.slice(last), hit: false });
  return parts.length ? parts : [{ text, hit: false }];
}

export function lineMatches(line, re) {
  if (!re) return true;
  re.lastIndex = 0;
  return re.test(line.text);
}

export const LEVELS = {
  all: () => true,
  warn: (l) => l.level === 'warn' || l.level === 'error',
  error: (l) => l.level === 'error',
  'no-debug': (l) => l.level !== 'debug',
};

// Saves text as a file in the browser.
export function saveText(name, text) {
  const url = URL.createObjectURL(new Blob([text], { type: 'text/plain;charset=utf-8' }));
  const a = document.createElement('a');
  a.href = url;
  a.download = name;
  document.body.appendChild(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}

// Starts a download of a same-origin URL (the server names the file).
export function download(url) {
  const a = document.createElement('a');
  a.href = url;
  a.download = '';
  document.body.appendChild(a);
  a.click();
  a.remove();
}

export function stamp() {
  const d = new Date();
  return `${d.getFullYear()}${pad(d.getMonth() + 1)}${pad(d.getDate())}-${pad(d.getHours())}${pad(d.getMinutes())}${pad(d.getSeconds())}`;
}

export function bytes(n) {
  if (n < 1024) return `${n} B`;
  if (n < 1 << 20) return `${(n / 1024).toFixed(1)} KB`;
  return `${(n / (1 << 20)).toFixed(1)} MB`;
}

// How a launch ended, in words.
export function runLabel(r) {
  switch (r.state) {
    case 'running':
      return 'Running';
    case 'new':
    case 'pending':
    case 'assigned':
    case 'accepted':
    case 'preparing':
    case 'ready':
    case 'starting':
      return 'Starting';
    case 'failed':
      return r.exitCode != null ? `Crashed · exit ${r.exitCode}` : 'Failed';
    case 'rejected':
      return "Couldn't start";
    case 'complete':
      return `Exited${r.exitCode != null ? ` · ${r.exitCode}` : ''}`;
    case 'shutdown':
      return 'Stopped';
    case 'orphaned':
      return 'Lost';
    default:
      return r.state;
  }
}

export function runDot(r) {
  if (r.state === 'running') return 'running';
  if (r.state === 'failed' || r.state === 'rejected' || r.state === 'orphaned') return 'failed';
  if (['new', 'pending', 'assigned', 'accepted', 'preparing', 'ready', 'starting'].includes(r.state)) return 'starting';
  return '';
}

export function duration(sec) {
  if (sec == null || sec < 0) return '';
  if (sec < 60) return `${sec}s`;
  if (sec < 3600) return `${Math.floor(sec / 60)}m ${sec % 60}s`;
  if (sec < 86400) return `${Math.floor(sec / 3600)}h ${Math.floor((sec % 3600) / 60)}m`;
  return `${Math.floor(sec / 86400)}d ${Math.floor((sec % 86400) / 3600)}h`;
}
