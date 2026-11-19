import { api } from './api.js';

// Private container registry presets and image helpers. registryHost
// mirrors the server's docker.RegistryHost.

export const registryPresets = [
  {
    key: 'ghcr',
    label: 'GitHub (GHCR)',
    name: 'GitHub',
    server: 'ghcr.io',
    badge: 'GH',
    userHint: 'your GitHub username',
    tokenLabel: 'Personal access token',
    help: 'Use a personal access token (classic) with only the read:packages scope. GitHub App connections can’t pull from GHCR.',
    link: 'https://github.com/settings/tokens/new?scopes=read:packages&description=SimplePanel',
    linkText: 'Create a token',
  },
  {
    key: 'dockerhub',
    label: 'Docker Hub',
    name: 'Docker Hub',
    server: 'docker.io',
    badge: 'DH',
    userHint: 'your Docker ID',
    tokenLabel: 'Personal access token',
    help: 'Use a personal access token with Read-only access. A login also raises Docker Hub’s pull rate limit.',
    link: 'https://app.docker.com/settings/personal-access-tokens',
    linkText: 'Create a token',
  },
  {
    key: 'gitlab',
    label: 'GitLab',
    name: 'GitLab',
    server: 'registry.gitlab.com',
    badge: 'GL',
    userHint: 'deploy token username',
    tokenLabel: 'Deploy token',
    help: 'Use a deploy token with the read_registry scope (project or group › Settings › Repository › Deploy tokens). Self-hosted GitLab: turn on Advanced view to change the address.',
    link: 'https://docs.gitlab.com/user/project/deploy_tokens/',
    linkText: 'About deploy tokens',
  },
  {
    key: 'other',
    label: 'Other',
    name: '',
    server: '',
    badge: 'CR',
    userHint: 'username',
    tokenLabel: 'Password or token',
    help: 'Any registry that works with docker login, such as Harbor, Quay, Gitea or your own registry:2.',
  },
];

export function presetFor(server) {
  return registryPresets.find((p) => p.server && p.server === server) || registryPresets[registryPresets.length - 1];
}

const hubAliases = ['index.docker.io', 'registry-1.docker.io', 'registry.hub.docker.com', 'hub.docker.com'];

// registryHost returns the registry an image pulls from: the first path
// part if it has a dot or port or is "localhost", otherwise Docker Hub.
export function registryHost(image) {
  const ref = (image || '').trim();
  const i = ref.indexOf('/');
  if (i < 0) return 'docker.io';
  const first = ref.slice(0, i);
  if (!/[.:]/.test(first) && first !== 'localhost') return 'docker.io';
  const h = first.toLowerCase();
  return hubAliases.includes(h) ? 'docker.io' : h;
}

// loadRegistries fetches the saved logins (id, name and registry for
// everyone; usernames only for admins). Cached for a few seconds so every
// form on a page shares one request.
let cache = null;
let cachedAt = 0;
export function loadRegistries() {
  if (!cache || Date.now() - cachedAt > 10000) {
    cachedAt = Date.now();
    cache = api.get('/registries').catch((e) => {
      cache = null;
      throw e;
    });
  }
  return cache;
}

// forgetRegistries drops the cache after a login is added or changed.
export function forgetRegistries() {
  cache = null;
}
