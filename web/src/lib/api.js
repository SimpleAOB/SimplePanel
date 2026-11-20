// Thin JSON client for the SimplePanel API.
// Every mutating request carries the X-SimplePanel header (CSRF protection).

export class ApiError extends Error {
  constructor(status, message, data = null) {
    super(message);
    this.status = status;
    this.data = data; // the whole error body, e.g. { code: 'memory_pressure' }
  }
}

let onUnauthorized = () => {};
export function setUnauthorizedHandler(fn) {
  onUnauthorized = fn;
}

async function request(method, path, body) {
  const opts = { method, credentials: 'same-origin', headers: {} };
  if (method !== 'GET') opts.headers['X-SimplePanel'] = '1';
  if (body !== undefined) {
    opts.headers['Content-Type'] = 'application/json';
    opts.body = JSON.stringify(body);
  }
  const res = await fetch('/api' + path, opts);
  let data = null;
  try {
    data = await res.json();
  } catch {
    /* empty body */
  }
  if (!res.ok) {
    if (res.status === 401 && !path.startsWith('/login') && !path.startsWith('/setup')) onUnauthorized();
    throw new ApiError(res.status, data?.error || res.statusText, data);
  }
  return data;
}

export const api = {
  get: (p) => request('GET', p),
  post: (p, b) => request('POST', p, b ?? {}),
  put: (p, b) => request('PUT', p, b),
  del: (p) => request('DELETE', p),
};
