// Global UI state shared by the layout components.
import { api } from './api.js';

function load(key, fallback) {
  try {
    return localStorage.getItem(key) ?? fallback;
  } catch {
    return fallback;
  }
}
function save(key, value) {
  try {
    localStorage.setItem(key, value);
  } catch {
    /* storage unavailable */
  }
}

export const ui = $state({
  theme: load('sp-theme', 'dark'),
  advanced: load('sp-advanced', '0') === '1',
  // On narrow screens the panel overlays everything, so start closed there.
  panelOpen: load('sp-panel', '1') === '1' && window.innerWidth > 1100,
  panelWidth: Number(load('sp-panel-width', '0')) || 0, // 0: the theme's default
  navOpen: false,
  route: parseHash(),
  // A note to show on the next page, e.g. a delete's warnings.
  flash: '',
});

export const data = $state({
  user: null,
  projects: [],
  apps: [],
  appsFor: 0,
  // Whether Docker is answering the panel (see GET /api/docker).
  docker: { ok: true },
});

export function applyTheme() {
  document.documentElement.dataset.theme = ui.theme;
}

export function toggleTheme() {
  ui.theme = ui.theme === 'dark' ? 'light' : 'dark';
  save('sp-theme', ui.theme);
  applyTheme();
}

export function setAdvanced(v) {
  ui.advanced = v;
  save('sp-advanced', v ? '1' : '0');
}

export function setPanelOpen(v) {
  ui.panelOpen = v;
  save('sp-panel', v ? '1' : '0');
}

// The detail panel can be dragged wider. Keep room for the main column.
export const PANEL_MIN = 320;
export function panelMax() {
  return Math.max(PANEL_MIN, window.innerWidth - 560);
}
export function setPanelWidth(w) {
  ui.panelWidth = w ? Math.round(Math.min(Math.max(w, PANEL_MIN), panelMax())) : 0;
  save('sp-panel-width', String(ui.panelWidth));
}

// Log viewer preferences, remembered per browser.
function loadJSON(key, fallback) {
  try {
    return { ...fallback, ...JSON.parse(load(key, '{}')) };
  } catch {
    return fallback;
  }
}
export const logPrefs = $state(
  loadJSON('sp-log-prefs', { wrap: true, timestamps: true, tail: 500, level: 'all', since: 0, regex: false, caseSensitive: false }),
);
export function saveLogPrefs() {
  save('sp-log-prefs', JSON.stringify(logPrefs));
}

// ---- Routing (hash based) ----
// #/                      overview
// #/settings[/:section]   panel settings
// #/invite/:token         invite or password reset link
// #/p/:pid                project
// #/p/:pid/new            new app
// #/p/:pid/a/:aid[/:tab]  app
function parseHash() {
  const parts = location.hash.replace(/^#\/?/, '').split('/').filter(Boolean);
  const r = { view: 'home', projectId: 0, appId: 0, tab: 'overview', section: '', token: '' };
  if (parts[0] === 'settings') {
    r.view = 'settings';
    r.section = parts[1] || '';
  }
  if (parts[0] === 'invite') {
    r.view = 'invite';
    r.token = parts[1] || '';
  }
  if (parts[0] === 'p' && parts[1]) {
    r.projectId = Number(parts[1]) || 0;
    r.view = 'project';
    if (parts[2] === 'new') {
      r.view = 'new-app';
      r.tab = parts[3] === 'database' ? 'database' : 'app';
    }
    if (parts[2] === 'a' && parts[3]) {
      r.view = 'app';
      r.appId = Number(parts[3]) || 0;
      r.tab = parts[4] || 'overview';
    }
  }
  return r;
}

// A page with unsaved edits sets a guard; it returns true while there is
// something to lose. Leaving that page then asks first.
let leaveGuard = null;
export function setLeaveGuard(fn) {
  leaveGuard = fn;
}

export function startRouter() {
  let lastHash = location.hash;
  window.addEventListener('beforeunload', (e) => {
    if (leaveGuard?.()) e.preventDefault();
  });
  window.addEventListener('hashchange', () => {
    const prev = ui.route;
    const next = parseHash();
    const samePage = next.view === prev.view && next.appId === prev.appId;
    if (!samePage && leaveGuard?.() && !confirm('You have unsaved changes. Leave without saving them?')) {
      history.replaceState(null, '', lastHash);
      return;
    }
    lastHash = location.hash;
    ui.route = parseHash();
    // On a phone the panel covers the page; going to another app or
    // database (from the menu or with Back) shows that page, not the old
    // app's panel.
    if (window.innerWidth <= 1100 && ui.route.appId !== prev.appId) ui.panelOpen = false;
    ui.navOpen = false;
  });
}

export function go(path) {
  location.hash = '#' + path;
}

// ---- Data loading ----
export async function loadProjects() {
  data.projects = await api.get('/projects');
}

export async function loadApps(projectId) {
  if (!projectId) {
    data.apps = [];
    data.appsFor = 0;
    return;
  }
  const apps = await api.get(`/projects/${projectId}/apps`);
  // Ignore stale responses if the user switched projects meanwhile.
  if (ui.route.projectId === projectId) {
    data.apps = apps;
    data.appsFor = projectId;
  }
}

// Docker's health as the server sees it. Reading it never waits on Docker.
export async function loadDocker() {
  try {
    data.docker = await api.get('/docker');
  } catch {
    /* the panel itself is unreachable; other requests say so */
  }
}

export function initials(name) {
  const parts = name.split('-').filter(Boolean);
  if (parts.length >= 2) return (parts[0][0] + parts[1][0]).toUpperCase();
  return name.slice(0, 2).toUpperCase();
}

export function timeAgo(unix) {
  if (!unix) return '';
  const s = Math.max(0, Math.floor(Date.now() / 1000 - unix));
  if (s < 60) return `${s}s ago`;
  if (s < 3600) return `${Math.floor(s / 60)}m ago`;
  if (s < 86400) return `${Math.floor(s / 3600)}h ago`;
  return `${Math.floor(s / 86400)}d ago`;
}

// Right-hand detail panel: app logs or a deployment's build log.
export const panel = $state({ tab: 'logs', deploymentId: 0, run: 0, appId: 0 });

// How a deployment's status reads, and the dot it gets ("running" would
// look like a healthy app).
export const depLabel = (s) => ({ queued: 'waiting', running: 'in progress' })[s] || s;
export const depDot = (s) => (s === 'running' || s === 'queued' ? 'deploying' : s);

export function showDeployment(id) {
  panel.tab = 'deploy';
  panel.deploymentId = id;
  setPanelOpen(true);
}

// Opens the logs of one launch (container start); 0 means all live ones.
export function showRun(number) {
  panel.tab = 'logs';
  panel.run = number;
  setPanelOpen(true);
}

export function appIcon(a) {
  if (a.kind && a.kind !== 'app') return 'db';
  return a.source === 'git' ? 'git' : 'box';
}

// ---- Roles ----
// Project access levels, as the API reports them.
export const LEVEL = { none: 0, view: 1, deploy: 2, manage: 3 };

export function isAdmin() {
  return data.user?.role === 'owner' || data.user?.role === 'admin';
}

export function projectLevel(pid) {
  return data.projects.find((p) => p.id === pid)?.level ?? 0;
}

export const roleLabels = { owner: 'Owner', admin: 'Admin', developer: 'Developer', viewer: 'Viewer' };
