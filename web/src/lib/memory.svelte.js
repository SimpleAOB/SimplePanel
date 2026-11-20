// Server memory: capacity info for the memory pickers, and the "server is
// nearly out of memory" check that deploys, starts and restarts go through.
import { api } from './api.js';

export function fmtMB(mb) {
  if (mb >= 1024) {
    const gb = mb / 1024;
    return `${Number.isInteger(gb) ? gb : gb.toFixed(1)} GB`;
  }
  return `${mb} MB`;
}

export function loadCapacity(appId = 0) {
  return api.get('/capacity' + (appId ? `?app=${appId}` : ''));
}

// The "deploy anyway?" question, shown by MemoryPrompt.svelte.
export const memPrompt = $state({ open: false, message: '', resolve: null });

// guardedPost posts to a deploy/start/restart endpoint. If the server
// refuses because it's nearly out of memory and the user may override,
// it asks, and retries with ?force=1 on "Deploy anyway".
export async function guardedPost(path, body) {
  try {
    return await api.post(path, body);
  } catch (e) {
    if (e.status !== 409 || e.data?.code !== 'memory_pressure' || !e.data.canForce) throw e;
    const ok = await new Promise((resolve) => {
      memPrompt.message = e.message;
      memPrompt.resolve = resolve;
      memPrompt.open = true;
    });
    memPrompt.open = false;
    if (!ok) throw new Error('Not started: the server is nearly out of memory.');
    return await api.post(path + (path.includes('?') ? '&' : '?') + 'force=1', body);
  }
}
