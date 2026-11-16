// Ready-made static site settings for common tools, used by the
// "How to set up a static site" dialog. Empty strings mean "use the default".
export const staticPresets = [
  {
    id: 'html',
    name: 'Plain HTML',
    hint: 'A repository of .html, .css and .js files with an index.html.',
    buildCommand: '',
    outputDir: '',
    buildImage: '',
    spa: false,
  },
  {
    id: 'vite',
    name: 'Vite (React, Vue, Svelte)',
    hint: 'Single-page apps made with Vite.',
    buildCommand: 'npm run build',
    outputDir: 'dist',
    buildImage: '',
    spa: true,
  },
  {
    id: 'cra',
    name: 'Create React App',
    hint: 'Older React apps made with react-scripts.',
    buildCommand: 'npm run build',
    outputDir: 'build',
    buildImage: '',
    spa: true,
  },
  {
    id: 'astro',
    name: 'Astro',
    hint: 'Astro sites with the default static output.',
    buildCommand: 'npm run build',
    outputDir: 'dist',
    buildImage: '',
    spa: false,
  },
  {
    id: 'next',
    name: 'Next.js (static export)',
    hint: "Needs output: 'export' in next.config. Server features need a Dockerfile instead.",
    buildCommand: 'npm run build',
    outputDir: 'out',
    buildImage: '',
    spa: false,
  },
  {
    id: 'docusaurus',
    name: 'Docusaurus',
    hint: 'Documentation sites made with Docusaurus.',
    buildCommand: 'npm run build',
    outputDir: 'build',
    buildImage: '',
    spa: false,
  },
  {
    id: 'eleventy',
    name: 'Eleventy',
    hint: 'Eleventy (11ty) sites.',
    buildCommand: 'npx @11ty/eleventy',
    outputDir: '_site',
    buildImage: '',
    spa: false,
  },
  {
    id: 'hugo',
    name: 'Hugo',
    hint: 'Hugo sites. Uses a Hugo image instead of Node.',
    buildCommand: 'hugo --minify',
    outputDir: 'public',
    buildImage: 'hugomods/hugo:exts',
    spa: false,
  },
];

// applyStaticPreset copies a preset's settings onto an app form value.
export function applyStaticPreset(value, preset) {
  value.buildType = 'static';
  value.buildCommand = preset.buildCommand;
  value.outputDir = preset.outputDir;
  value.buildImage = preset.buildImage;
  value.spa = preset.spa;
}
