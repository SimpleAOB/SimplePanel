# SimplePanel

A free, self-hosted server panel. Install it on a fresh VPS with one command,
then deploy apps from a Git repository or a Docker image, put them on your
domains with automatic HTTPS, and create Postgres, MySQL, MongoDB or Redis
databases in one click.

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

Once installed, SimplePanel updates itself: owners and admins see an
"Update available" banner when a new stable release is out, with its release
notes and a one-click update. Swarm swaps the panel's container, apps keep
running, and if the new version fails its health check Swarm puts the old one
back. Turn on Advanced view for automatic updates at an hour you choose.

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
- **Deploy a Docker image** from Docker Hub, GHCR, GitLab or any registry, public or private (saved registry logins, used by every node in the swarm).
- **Domains with automatic HTTPS** via Let's Encrypt.
- **Databases**: PostgreSQL, MySQL, MariaDB, MongoDB and Redis with your own
  or generated credentials, a
  persistent volume and a copyable connection URL. Databases are only
  reachable from apps in the same project.
- **Zero-downtime deploys**: new containers start before old ones stop.
- **Deploy webhooks** for CI/CD (see below).
- **Export and import as JSON**: copy or download a whole project or a
  single service, then paste it into any SimplePanel to recreate it.
- **Logs**: live app logs and build logs in a resizable side panel, with search, error and warning highlighting, and downloads. Every container start (a deploy, a restart, a crash) keeps its own logs, so you can read what a previous run printed after it's gone.
- **Connect an AI (MCP)**: a built-in MCP server lets assistants such as
  Claude Code check on apps, read logs, change settings and deploy (see
  below).
- **Advanced view**: replicas, CPU and memory limits, volumes, published
  ports and command overrides, hidden until you need them.

## New in v0.2

- **Backups to S3-compatible storage** (AWS S3, Cloudflare R2, Backblaze
  B2, Wasabi, MinIO…). Back up PostgreSQL, MySQL, MariaDB, MongoDB and
  Redis databases, the volumes of any app, and SimplePanel's own
  configuration, on a schedule or on demand, keeping the last N. Backups
  are plain SQL dumps, `mongodump` archives, Redis snapshots (RDB) or tar
  archives, gzipped and optionally **encrypted with
  [age](https://age-encryption.org)**, so you can also open them without
  SimplePanel (`age -d backup.sql.gz.age | gunzip`).
- **Restore by picking a backup**: into the same database, or into a new
  database next to it so you can compare first. **Find backups in a
  bucket** lists backups made by another server, for moving to a new one.
- **Monitoring**: CPU, memory, disk and network history for the server and
  every service, from 1 hour to 30 days, with no extra containers. Alerts
  when usage stays high, and when a service keeps crashing.
- **Users and roles**: invite people with a link. **Owner** and **Admin**
  manage everything; **Developers** create, configure and deploy in the
  projects you pick; **Viewers** only look. Every change is in the
  activity log.
- **Notifications** to Discord, Slack, Telegram, email or any webhook
  (signed with HMAC-SHA256) for failed deploys and backups, crashing
  services and resource alerts.
- **Automatic subdomains**: set a wildcard domain such as
  `apps.example.com` once (with a `*.apps.example.com` DNS record) and
  every app gets `app-project.apps.example.com` with HTTPS.

Server monitoring reads the host's `/proc`, which the installer mounts
read-only. Re-run the install command to add it to an existing install.

## New in v0.3

- **GitHub and GitLab**: connect once under Settings → Git, then pick
  repositories and branches from a list instead of pasting URLs. For GitHub,
  SimplePanel creates a private GitHub App that belongs to you, for your
  server only (no central app, nothing goes through anyone else's servers).
  GitLab (gitlab.com or your own) connects with an access token.
- **Deploy on push**, with the result shown on the commit in GitHub or
  GitLab, and the commit message in the deployment list.
- **Pull request previews**: each pull request gets its own copy of the app
  at its own automatic subdomain, updated on every push and removed when the
  pull request closes. A comment on the pull request links to it. Previews
  can override variables (for example a staging database), and pull
  requests from forks are skipped so outside code never runs on your server.
- **Deploy keys**: for repositories you don't want to connect, SimplePanel
  can create an SSH deploy key to add to the repository (read-only), and use
  a `git@` URL.
- **Cron jobs**: run a command on a schedule inside a service's running
  container, with presets such as "every hour", a timeout, run history with
  output, "Run now", and a notification when a job fails.
- **API and CLI**: a REST API with Bearer tokens and the `sp` command-line
  tool for scripts and CI (`sp deploy shop/web --wait`). See
  [docs/API.md](docs/API.md).

## Deploy webhooks

Every app has a secret webhook URL (Overview tab, with Advanced view on).

```sh
# Redeploy from CI/CD
curl -X POST https://panel.example.com/api/hooks/deploy/<token>

# Image apps: deploy a specific tag (it is saved as the app's new image)
curl -X POST "https://panel.example.com/api/hooks/deploy/<token>?tag=1.4.2"
```

You can also paste the URL into a GitHub or GitLab repository's webhook
settings (or connect GitHub or GitLab under Settings → Git, which sets this
up for you). Push events to the app's branch (or the repository's default
branch) trigger a deploy, and pull request events start previews when the
app has them on; other branches and events are ignored.

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
| Full      | + delete services and projects, restore backups over current data |

A token belongs to the user who created it and can never do more than that
user's role allows. v0.2 adds tools for metrics (`get_metrics`) and backups
(`list_backups`, `backup_now`, `restore_backup`, `get_backup`). With a
Manage token the AI can only restore into a new database; replacing data
needs a Full token and the service's name as confirmation.

The AI never sees secrets: environment variable values, database passwords,
Git tokens and webhook URLs are left out of every answer, so it asks you
when it needs one. Every change and every database query is listed under
**Connect an AI** in Settings, and admins see everything under
**Users → Activity**.

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

## License

SimplePanel is free software under the [MIT License](LICENSE).
