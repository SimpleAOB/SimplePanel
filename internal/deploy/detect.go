package deploy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
)

// DockerfileHelpURL explains how to add a Dockerfile, with examples for
// common languages.
const DockerfileHelpURL = "https://github.com/SimpleAOB/SimplePanel/blob/main/docs/DOCKERFILES.md"

// Detection is what a repository without a Dockerfile looks like.
type Detection struct {
	// Kind is "static" (a site generator SimplePanel can build), "server"
	// (an app that needs a Dockerfile) or "" (plain files, or unknown).
	Kind string
	// Name is the tool or language, e.g. "Vite" or "Node.js".
	Name string
	// Build settings for Kind "static".
	BuildCommand, OutputDir, BuildImage string
	SPA                                 bool
	// Reason says what gave it away, for Kind "server".
	Reason string
}

type packageJSON struct {
	Scripts         map[string]string `json:"scripts"`
	Dependencies    map[string]string `json:"dependencies"`
	DevDependencies map[string]string `json:"devDependencies"`
}

var nextExportRe = regexp.MustCompile(`output\s*:\s*['"]export['"]`)

func exists(dir, name string) bool {
	_, err := os.Stat(filepath.Join(dir, name))
	return err == nil
}

// DetectProject looks at a build directory that has no Dockerfile and tells
// apart site generators (built as static sites) from server apps (which
// would otherwise be served as raw source files).
func DetectProject(dir string) Detection {
	hasIndex := exists(dir, "index.html")
	if b, err := os.ReadFile(filepath.Join(dir, "package.json")); err == nil {
		var pkg packageJSON
		_ = json.Unmarshal(b, &pkg)
		has := func(name string) bool {
			_, a := pkg.Dependencies[name]
			_, b := pkg.DevDependencies[name]
			return a || b
		}
		_, hasBuild := pkg.Scripts["build"]
		static := func(name, out string, spa bool) Detection {
			return Detection{Kind: "static", Name: name, BuildCommand: "npm run build", OutputDir: out, SPA: spa}
		}
		switch {
		case has("next"):
			for _, f := range []string{"next.config.js", "next.config.mjs", "next.config.ts", "next.config.cjs"} {
				if c, err := os.ReadFile(filepath.Join(dir, f)); err == nil && nextExportRe.Match(c) && hasBuild {
					return static("Next.js (static export)", "out", false)
				}
			}
			return Detection{Kind: "server", Name: "Next.js", Reason: "it uses Next.js, which runs a Node.js server"}
		case has("nuxt"), has("@sveltejs/kit"), has("@remix-run/node"), has("@remix-run/serve"),
			has("express"), has("fastify"), has("koa"), has("@nestjs/core"), has("hono"), has("@adonisjs/core"):
			return Detection{Kind: "server", Name: "Node.js", Reason: "package.json lists a Node.js server framework"}
		case has("astro") && hasBuild:
			return static("Astro", "dist", false)
		case has("@docusaurus/core") && hasBuild:
			return static("Docusaurus", "build", false)
		case has("react-scripts") && hasBuild:
			return static("Create React App", "build", true)
		case has("@angular/core") && hasBuild:
			return Detection{Kind: "static", Name: "Angular", BuildCommand: "npm run build", OutputDir: "dist", SPA: true}
		case has("vite") && hasBuild:
			return static("Vite", "dist", true)
		case has("@11ty/eleventy"):
			return Detection{Kind: "static", Name: "Eleventy", BuildCommand: "npx @11ty/eleventy", OutputDir: "_site"}
		}
		if _, ok := pkg.Scripts["start"]; ok && !hasIndex {
			return Detection{Kind: "server", Name: "Node.js", Reason: "package.json has a start script and there is no index.html"}
		}
	}
	if !hasIndex {
		if (exists(dir, "hugo.toml") || exists(dir, "hugo.yaml") || exists(dir, "config.toml")) && exists(dir, "content") {
			return Detection{Kind: "static", Name: "Hugo", BuildCommand: "hugo --minify", OutputDir: "public", BuildImage: "hugomods/hugo:exts"}
		}
		for _, s := range []struct{ file, name string }{
			{"requirements.txt", "Python"}, {"pyproject.toml", "Python"}, {"Pipfile", "Python"},
			{"go.mod", "Go"}, {"Gemfile", "Ruby"}, {"composer.json", "PHP"}, {"index.php", "PHP"},
			{"Cargo.toml", "Rust"}, {"pom.xml", "Java"}, {"build.gradle", "Java"},
		} {
			if exists(dir, s.file) {
				return Detection{Kind: "server", Name: s.name, Reason: "it has a " + s.file + " and no index.html"}
			}
		}
	}
	return Detection{}
}
