// Helpers to convert between the API's app object and the form model.

export function emptyForm() {
  return {
    name: '',
    source: 'git',
    image: '',
    gitUrl: '',
    gitBranch: '',
    gitToken: '',
    clearGitToken: false,
    gitProviderId: 0,
    gitRepo: '',
    buildPath: '',
    dockerfile: '',
    buildType: 'auto',
    buildImage: '',
    buildCommand: '',
    outputDir: '',
    spa: false,
    env: '',
    replicas: 1,
    cpuLimit: 0,
    memoryMb: 0,
    command: '',
    volumes: [],
    ports: [],
    registryId: null, // null: let the server pick the login for the image's registry
  };
}

export function formFromApp(a) {
  return {
    ...emptyForm(),
    name: a.name,
    source: a.source,
    image: a.image,
    gitUrl: a.gitUrl,
    gitBranch: a.gitBranch,
    gitProviderId: a.gitProviderId || 0,
    gitRepo: a.gitRepo || '',
    buildPath: a.buildPath,
    dockerfile: a.dockerfile,
    buildType: a.buildType || 'auto',
    buildImage: a.buildImage,
    buildCommand: a.buildCommand,
    outputDir: a.outputDir,
    spa: a.spa,
    env: a.env,
    replicas: a.replicas,
    cpuLimit: a.cpuLimit,
    memoryMb: a.memoryMb,
    command: a.command,
    volumes: a.volumes.map((v) => ({ ...v })),
    ports: a.ports.map((p) => ({ ...p })),
    registryId: a.registryId || 0,
  };
}

export function payload(f) {
  return {
    ...f,
    replicas: Number(f.replicas) || 0,
    cpuLimit: Number(f.cpuLimit) || 0,
    memoryMb: Number(f.memoryMb) || 0,
    volumes: f.volumes.filter((v) => v.name || v.path).map((v) => ({ name: v.name.trim(), path: v.path.trim() })),
    ports: f.ports
      .filter((p) => p.published || p.target)
      .map((p) => ({ published: Number(p.published), target: Number(p.target), protocol: p.protocol })),
  };
}
