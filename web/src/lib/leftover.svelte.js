// Old data left by deleted services. A new service (or a new volume) with
// the same name as a deleted one would start on top of its data, so the
// server refuses (409, code "leftover_data") and this asks what to do.
import { api } from './api.js';

// The question, shown by LeftoverPrompt.svelte.
export const leftoverPrompt = $state({ open: false, message: '', volumes: [], canReplace: false, allowKeep: false, resolve: null });

// withLeftoverCheck sends a create/update request. If the server finds old
// data, it asks, and retries with replaceData (delete it) or keepData (use
// it as it is). allowKeep offers "use it", which suits app files but not
// databases (whose new password would be ignored). send(path, body) does
// the request, e.g. guardedPost or put below.
export async function withLeftoverCheck(send, path, body, { allowKeep = false } = {}) {
  try {
    return await send(path, body);
  } catch (e) {
    if (e.status !== 409 || e.data?.code !== 'leftover_data') throw e;
    const choice = await new Promise((resolve) => {
      Object.assign(leftoverPrompt, {
        open: true,
        message: e.message,
        volumes: e.data.volumes || [],
        canReplace: !!e.data.canReplace,
        allowKeep,
        resolve,
      });
    });
    leftoverPrompt.open = false;
    if (choice === 'replace') return await send(path, { ...body, replaceData: true });
    if (choice === 'keep') return await send(path, { ...body, keepData: true });
    throw new Error('Not created: old data is in the way. Choose another name, or delete the old data.');
  }
}

export const post = (p, b) => api.post(p, b);
export const put = (p, b) => api.put(p, b);
