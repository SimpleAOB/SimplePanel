// In-panel updates: shared state for the banner, the dialog and Settings.
import { api } from './api.js';

export const upd = $state({
  status: null, // GET /api/update
  open: false, // the update dialog
  progress: null, // { target, phase: working|restarting|done|failed|slow, step, error }
});

export async function loadUpdate() {
  try {
    upd.status = await api.get('/update');
  } catch {
    /* not an admin, or the panel is restarting */
  }
  return upd.status;
}

export async function checkNow() {
  upd.status = await api.post('/update/check');
  return upd.status;
}

const DISMISS_KEY = 'sp-update-dismissed';
export function isDismissed(version) {
  try {
    return localStorage.getItem(DISMISS_KEY) === version;
  } catch {
    return false;
  }
}
export function dismiss(version) {
  try {
    localStorage.setItem(DISMISS_KEY, version);
  } catch {
    /* storage unavailable */
  }
}

// display formats a release version like the server does.
export const display = (v) => (/^\d+\.\d+\.\d+$/.test(v || '') ? 'v' + v : v);

let poll = 0;

// startUpdate asks the server to update, then follows it through the
// restart. Throws the server's error (with .jobsRunning on a 409 caused by
// running jobs).
export async function startUpdate(force = false) {
  const target = display(upd.status.check.latest.version);
  try {
    upd.status = await api.post('/update', { version: upd.status.check.latest.version, force });
  } catch (e) {
    if (e.status === 409 && /running/.test(e.message) && !/already/.test(e.message)) e.jobsRunning = true;
    throw e;
  }
  upd.progress = { target, phase: 'working', step: upd.status.job?.step || 'Starting', error: '' };
  follow(target);
}

// follow polls until the new version answers, or the update fails.
export function follow(target) {
  clearInterval(poll);
  const started = Date.now();
  poll = setInterval(async () => {
    const p = upd.progress;
    if (!p) return clearInterval(poll);
    let s;
    try {
      const res = await fetch('/api/update', { credentials: 'same-origin' });
      if (!res.ok) throw new Error(String(res.status));
      s = await res.json();
    } catch {
      // The old panel has stopped; the new one isn't up yet.
      if (p.phase === 'working') p.phase = 'restarting';
      if (Date.now() - started > 5 * 60 * 1000) p.phase = 'slow';
      return;
    }
    upd.status = s;
    const j = s.job;
    if (s.current === target) {
      p.phase = 'done';
      clearInterval(poll);
      // Load the new version's UI.
      setTimeout(() => location.reload(), 2500);
      return;
    }
    if (j && j.to === target && (j.state === 'failed' || j.state === 'rolled_back')) {
      p.phase = 'failed';
      p.error = j.error;
      p.rolledBack = j.state === 'rolled_back';
      clearInterval(poll);
      return;
    }
    if (j?.step) p.step = j.step;
    if (Date.now() - started > 5 * 60 * 1000) p.phase = 'slow';
  }, 2000);
}
