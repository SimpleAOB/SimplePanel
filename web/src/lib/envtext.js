// Read and change single variables in the KEY=value text the Environment
// box holds, keeping every other line (comments, order, pasted .env
// files) exactly as the user wrote it. Mirrors the server's ParseEnv.

function lineKey(line) {
  const m = line.match(/^\s*(?:export\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*=/);
  return m ? m[1] : null;
}

function unquote(v) {
  const t = v.trim();
  if (t.length >= 2 && ((t[0] === '"' && t.endsWith('"')) || (t[0] === "'" && t.endsWith("'")))) return t.slice(1, -1);
  return v;
}

// envValue returns the value the text gives key (the last line wins, as
// Docker does with repeated variables), or undefined when it isn't set.
export function envValue(text, key) {
  let out;
  for (const line of (text || '').split('\n')) {
    if (lineKey(line) === key) out = unquote(line.slice(line.indexOf('=') + 1).replace(/\r$/, ''));
  }
  return out;
}

// setEnvValue sets key in the text. An empty value removes the variable,
// so the image's own default applies.
export function setEnvValue(text, key, value) {
  const lines = (text || '').split('\n');
  const idx = [];
  lines.forEach((l, i) => lineKey(l) === key && idx.push(i));
  if (value === '') {
    const drop = new Set(idx);
    return lines.filter((_, i) => !drop.has(i)).join('\n');
  }
  // Values with surrounding spaces or quotes would be changed by .env
  // unquoting, so quote them.
  const needsQuotes = value !== value.trim() || /^["']/.test(value);
  const line = `${key}=${needsQuotes ? JSON.stringify(value) : value}`;
  if (idx.length) {
    lines[idx[idx.length - 1]] = line;
    const drop = new Set(idx.slice(0, -1));
    return lines.filter((_, i) => !drop.has(i)).join('\n');
  }
  const t = text || '';
  return t === '' ? line : t.endsWith('\n') ? t + line : `${t}\n${line}`;
}

// Variables almost every base image sets for itself (paths, versions,
// checksums). They are shown on request, not by default.
const systemKeys = new Set(['PATH', 'HOME', 'HOSTNAME', 'LANG', 'LANGUAGE', 'TERM', 'SHLVL', 'DEBIAN_FRONTEND', 'GOPATH', 'GOTOOLCHAIN', 'NVM_DIR', 'LD_LIBRARY_PATH', 'PYTHONDONTWRITEBYTECODE', 'PYTHONUNBUFFERED', 'PYTHONIOENCODING', 'GPG_KEY', 'GPG_KEYS', 'GOSU_VERSION', 'TINI_VERSION', 'container']);
const systemPatterns = [/^LC_/, /_VERSION$/, /_RELEASE$/, /_SHA(1|256|512)?(SUM)?$/, /_CHECKSUM$/, /_GPG_KEYS?$/, /_DEPS$/, /_MAJOR$/, /_MINOR$/, /^JAVA_HOME$/, /^PIP_/, /^PHPIZE_/, /_DOWNLOAD_URL$/, /^RUBY_DOWNLOAD/, /^BUNDLE_/, /^GEM_HOME$/];

export function isSystemVar(key) {
  return systemKeys.has(key) || systemPatterns.some((re) => re.test(key));
}
