// Turns a deployment's failure reason into a plain-language title and a
// next step. `kind` lets the page offer the right buttons.

export const DOCKERFILE_HELP = 'https://github.com/SimpleAOB/SimplePanel/blob/main/docs/DOCKERFILES.md';

export function explainFailure(reason = '') {
  const r = reason.toLowerCase();
  if (r.includes('repository is private') || r.includes('access token can') || r.includes('could not read username')) {
    return {
      kind: 'git-auth',
      title: "SimplePanel couldn't read the repository",
      hint: 'It is private, or the URL is wrong. Connect GitHub or GitLab to pick private repositories, or paste an access token below.',
    };
  }
  if (r.includes('deploy key')) {
    return { kind: 'git-key', title: 'The repository refused the deploy key', hint: "Add the deploy key shown on the Overview to the repository's settings, then deploy again." };
  }
  if (r.includes("branch") && r.includes("doesn't exist")) {
    return { kind: 'branch', title: "That branch doesn't exist", hint: "Check the branch name in the app's Settings tab." };
  }
  if (r.includes('looks like a') && r.includes('dockerfile')) {
    return {
      kind: 'server-app',
      title: 'This app needs a Dockerfile',
      hint: cap(reason.split(' (')[0]) + '. Apps that run a server are built from a Dockerfile in the repository.',
    };
  }
  if (r.includes('no index.html')) {
    return { kind: 'no-dockerfile', title: 'Nothing to serve', hint: 'The repository has no Dockerfile and no index.html. Check the repository URL and the build path, or add a Dockerfile.' };
  }
  if (r.includes('no dockerfile') || (r.includes('no ') && r.includes('found in'))) {
    return { kind: 'no-dockerfile', title: 'No Dockerfile in the repository', hint: 'Add a Dockerfile, or set the build type to Static site in the Settings tab if this is a website of plain files.' };
  }
  if (r.includes('build path') && r.includes('does not exist')) {
    return { kind: 'build-path', title: "The build path doesn't exist", hint: "Check the build path in the app's Settings tab (Advanced view)." };
  }
  if (r.startsWith('pull ') && (r.includes('not found') || r.includes('manifest unknown') || r.includes('does not exist'))) {
    return { kind: 'image', title: "That image doesn't exist", hint: 'Check the image name and tag for typos. Docker Hub images look like nginx:latest, others like ghcr.io/owner/app:tag.' };
  }
  if (r.startsWith('pull ') && (r.includes('denied') || r.includes('unauthorized') || r.includes('authentication'))) {
    return { kind: 'registry', title: 'The registry refused to send the image', hint: 'If the image is private, add a login under Settings › Registries and pick it in the app’s settings.' };
  }
  if (r.includes('keeps failing to start') || r.includes('failed to start and swarm rolled back')) {
    return {
      kind: 'crash',
      title: 'The app crashes when it starts',
      hint: 'These are the last lines it printed before it stopped. A missing setting in the Environment tab, such as a database URL, is the usual cause.',
    };
  }
  if (r.startsWith('build:')) {
    return { kind: 'build', title: 'The build failed', hint: 'These are the last lines the build printed. The error is usually among them; the full build log has the rest.' };
  }
  if (r.includes('out of memory') || r.includes('oom')) {
    return { kind: 'memory', title: 'The app ran out of memory', hint: 'Give it a higher memory limit, or free memory on the server.' };
  }
  return { kind: 'other', title: 'The last deploy failed', hint: reason ? cap(reason) + '.' : 'The build log shows what went wrong.' };
}

function cap(s) {
  return s.charAt(0).toUpperCase() + s.slice(1);
}
