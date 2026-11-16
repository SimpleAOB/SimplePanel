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
  navOpen: false,
  route: parseHash(),
});

export const data = $state({
  user: null,
  projects: [],
  apps: [],
  appsFor: 0,
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

// ---- Routing (hash based) ----
// #/                      overview
// #/settings              panel settings
// #/p/:pid                project
// #/p/:pid/new            new app
// #/p/:pid/a/:aid[/:tab]  app
function parseHash() {
  const parts = location.hash.replace(/^#\/?/, '').split('/').filter(Boolean);
  const r = { view: 'home', projectId: 0, appId: 0, tab: 'overview' };
  if (parts[0] === 'settings') r.view = 'settings';
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

export function startRouter() {
  window.addEventListener('hashchange', () => {
    ui.route = parseHash();
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
export const panel = $state({ tab: 'logs', deploymentId: 0 });

export function showDeployment(id) {
  panel.tab = 'deploy';
  panel.deploymentId = id;
  setPanelOpen(true);
}

export function appIcon(a) {
  if (a.kind && a.kind !== 'app') return 'db';
  return a.source === 'git' ? 'git' : 'box';
}
