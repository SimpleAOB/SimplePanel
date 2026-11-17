# SimplePanel

A free, self-hosted server panel. Install it on a fresh VPS with one command,
then deploy apps from a Git repository or a Docker image, put them on your
domains with automatic HTTPS, and create Postgres or MySQL databases in one
click.

> Status: early development (v0.1).

## Install

On a fresh Ubuntu or Debian server, as root:

```sh
curl -fsSL https://raw.githubusercontent.com/SimpleAOB/SimplePanel/main/install.sh | sudo sh
```

Then open `http://<your-server-ip>:3000` and create the admin account. Do it
straight away: the first visitor claims the server.

The installer sets up Docker, Docker Swarm, Traefik and SimplePanel. It
downloads everything before changing the server, and it is safe to run
again: to upgrade, or to finish an install that stopped part way.

| Variable        | Default                                  | Purpose                                   |
| --------------- | ---------------------------------------- | ----------------------------------------- |
| `SP_IMAGE`      | latest release, else `:edge` (main)      | SimplePanel image to run                  |
| `TRAEFIK_IMAGE` | `traefik:v3.6`                           | Traefik image                             |
| `ACME_EMAIL`    | _(none)_                                 | Email for Let's Encrypt expiry notices    |
| `SP_PORT`       | `3000`                                   | Port the panel listens on                 |

Example: `curl -fsSL … | sudo ACME_EMAIL=you@example.com sh`

## What you can do in v0.1

- **Deploy from Git**: any public or private (token) HTTPS repository. With
  a `Dockerfile` it is built as-is; without one it is served as a **static
  site**. Pick a branch, a build path and a Dockerfile path in the advanced
  view.
- **Static sites**: plain HTML repos work with no setup. For site generators
  (Vite, Astro, Hugo…) set a build command and output directory, e.g.
  `npm run build` → `dist`, and turn on the single-page-app fallback if
  needed. Files are served by nginx with dotfiles hidden and no directory
  listings.
- **Deploy a Docker image** from Docker Hub, GHCR or any registry.
- **Domains with automatic HTTPS** via Let's Encrypt.
- **Databases**: PostgreSQL, MySQL and MariaDB with generated credentials, a
  persistent volume and a copyable connection URL. Databases are only
  reachable from apps in the same project.
- **Zero-downtime deploys**: new containers start before old ones stop.
- **Deploy webhooks** for CI/CD (see below).
- **Export and import as JSON**: copy or download a whole project or a
  single service, then paste it into any SimplePanel to recreate it.
- **Logs**: live app logs and build logs side by side.
- **Connect an AI (MCP)**: a built-in MCP server lets assistants such as
  Claude Code check on apps, read logs, change settings and deploy (see
  below).
- **Advanced view**: replicas, CPU and memory limits, volumes, published
  ports and command overrides, hidden until you need them.

## Deploy webhooks

Every app has a secret webhook URL (Overview tab, with Advanced view on).

```sh
# Redeploy from CI/CD
curl -X POST https://panel.example.com/api/hooks/deploy/<token>

# Image apps: deploy a specific tag (it is saved as the app's new image)
curl -X POST "https://panel.example.com/api/hooks/deploy/<token>?tag=1.4.2"
```

You can also paste the URL into a GitHub or GitLab repository's webhook
settings. Push events to the app's branch (or the repository's default
branch) trigger a deploy; other branches and events are ignored.

Treat the URL like a password. Regenerate it from the UI if it leaks.

## Connect an AI (MCP)

SimplePanel has a built-in [MCP](https://modelcontextprotocol.io) server at
`/mcp`. Create a token in **Settings → Connect an AI**, then for Claude Code:

```sh
claude mcp add --transport http simplepanel https://panel.example.com/mcp \
  --header "Authorization: Bearer sp_…"
```

Other apps (Claude Desktop, Cursor, VS Code…) take the same URL and header;
Settings shows a ready-made config.

Each token has an access level, and the AI only sees the tools it allows:

| Access    | Tools |
| --------- | ----- |
| Read-only | list projects, service details, logs, deployments and build logs, export, database queries (see below) |
| Manage    | + create projects, apps and databases, change settings and env vars, domains, deploy, restart/stop/start, import |
| Full      | + delete services and projects |

The AI never sees secrets: environment variable values, database passwords,
Git tokens and webhook URLs are left out of every answer, so it asks you
when it needs one. Every change and every database query is listed under
**AI activity** in Settings.

**Database queries.** On a database's page, **Allow AI read-only access**
creates a separate database user that can only read that database's data.
The AI can then list tables, describe them and run queries through it.
Writes are refused, queries stop after 10 seconds and results are capped at
500 rows. It is off by default; turning it off deletes the user.

## Export and import

Use **Export** on a project page (or **Export as JSON** in a service's
Settings tab) to get a JSON document you can copy or download. Paste it into
**Import project** on the Overview page, or **Import** on a project page for
a single service. You can rename it on the way in and deploy it right away.

Exports never contain secrets: Git access tokens, webhook URLs and database
passwords are left out and regenerated on import. App environment variables
are only included if you tick the box, because they often hold API keys.
Database contents are not exported.

## Development

Requirements: Go 1.26+, Node 22+, Docker with Swarm enabled
(`docker swarm init`).

```sh
make test     # go vet + unit tests
make build    # builds the UI and bin/simplepanel
make run      # runs against your local Docker
cd web && npm run dev   # UI dev server on :5173, proxies /api to :3000
```

The UI's colours, fonts, spacing and radii all live in
[`web/src/theme.css`](web/src/theme.css). Dark is the default theme, with a
matching light theme.

See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for how it fits together.
