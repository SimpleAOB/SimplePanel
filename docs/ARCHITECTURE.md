# SimplePanel architecture (v0.3)

SimplePanel is a free, self-hosted server control panel: point it at a fresh VPS,
run one command, then deploy apps from a Git repository or a Docker image with
a domain and automatic HTTPS.

This document explains the moving parts and why each was picked. The guiding
rules are **secure by default**, **low overhead**, and **simple by default, with
an advanced view for everything else**.

## Big picture

```
                 Internet
                    │ 80 / 443
          ┌─────────▼──────────┐
          │  Traefik v3        │  routes by Host(), gets Let's Encrypt certs
          │  (file provider)   │  NO docker socket mounted
          └─────────┬──────────┘
                    │  overlay network "simplepanel"
     ┌──────────────┼────────────────────────┐
     │              │                        │
┌────▼─────┐  ┌─────▼─────┐            ┌─────▼─────┐
│SimplePanel│  │ app: blog │   ...      │ app: api  │   Docker Swarm services
│ (Go + UI) │  └───────────┘            └───────────┘
└────┬──────┘
     │ docker.sock (manager node only)
┌────▼──────────────┐
│ Docker Engine     │  Swarm mode, single node in v0.1
└───────────────────┘
```

Everything, SimplePanel included, runs as a Docker Swarm service.

### Networks

- `simplepanel`: shared overlay network for Traefik, the panel and every
  regular app, so Traefik can reach apps by service name.
- `sp_<project>`: one private overlay network per project. Every app and
  database in the project joins it, with its short name as an alias
  (`db`, `api`). **Databases join only this network**, so they are not
  reachable from other projects or from the proxy.

## Components

### 1. SimplePanel server: Go, single static binary

- **Why Go:** one small static binary with no runtime to patch, a strong
  standard library for HTTP and crypto, memory safety, and first-class
  Docker ecosystem. The web UI is embedded with `go:embed`, so the whole panel
  is one file in one container image.
- **Few dependencies on purpose.** We talk to the Docker Engine API directly
  over the unix socket with a small typed client (`internal/docker`) instead of
  pulling in the full Docker SDK and its transitive tree. Third-party Go
  modules are limited to:
  - `modernc.org/sqlite` (pure-Go SQLite, no cgo) for state
  - `golang.org/x/crypto` for bcrypt
  - `gopkg.in/yaml.v3` for writing Traefik config
- **State:** one SQLite file at `/data/simplepanel.db` (Docker volume
  `simplepanel-data`). It stores users, sessions, projects, apps, domains and
  deployment history. Docker is the source of truth for *what is running*;
  SQLite is the source of truth for *what should be running*.

### 2. Runtime: Docker Swarm

- Each app becomes one Swarm **service** named `<project>_<app>`.
- Updates use `update_config.order = start-first` with automatic rollback on
  failure. The new task starts before the old one stops, and Swarm's internal
  VIP load-balances between them, giving zero-downtime deploys.
- Databases use `stop-first` instead: two database processes must never
  share a data directory, so a database restarts briefly on redeploy.
- Swarm VIPs need the `ip_vs` kernel module. On VPS kernels without it (some
  LXC/OpenVZ hosts) the installer sets `SP_ENDPOINT_MODE=dnsrr`, and apps use
  DNS round-robin instead.
- v0.1 targets a single node. Images built from Git live in the local image
  store. Multi-node support (a built-in registry) is planned for later.

### 3. Reverse proxy: Traefik v3

- **Why Traefik:** native Let's Encrypt (HTTP-01), hot-reloading dynamic
  config, low memory use, and it is the most widely known proxy among
  self-hosters, so its YAML is familiar.
- **Security choice:** Traefik does *not* get the Docker socket. SimplePanel
  writes Traefik's dynamic configuration as YAML files to a shared volume
  (`/etc/traefik/dynamic`), and Traefik's file provider watches that folder.
  If Traefik (the internet-facing process) were ever compromised, it would
  not get root on the host via the socket.
- Traefik's ports are published in host mode so apps see real client IPs.
- Users who know Traefik can drop their own `*.yml` files into the dynamic
  folder; SimplePanel only manages files prefixed `sp-`.
- HTTP on port 80 redirects to HTTPS except for ACME challenges. Certificates
  are stored in the `simplepanel-traefik-certs` volume.

### 4. Web UI: Svelte 5 + Vite

- **Why Svelte:** a compiled framework with a very small runtime, so the
  embedded bundle stays small and fast on cheap VPSes.
- **Simple by default:** the app form shows only what you need (source,
  domain, port, env vars). An **Advanced view** toggle (remembered per browser)
  reveals replicas, CPU/memory limits, volumes, command override, Git branch,
  build path and Dockerfile path, and similar options.
- **Layered layout:** a narrow left rail of projects, then a sidebar listing
  the selected project's apps, then the main content area, with an optional
  right panel for details and logs.
- **Theming:** every colour, radius, font and spacing value is a CSS variable
  in `web/src/theme.css`. Components never hard-code visual values, so the
  project's visual style can be applied by editing that one file.

## Deploy flows

### Docker image
1. User enters an image (`nginx:1.27`, `ghcr.io/org/app:tag`).
2. SimplePanel pulls the image, then creates or updates the Swarm service.
3. Traefik config is rewritten for the app's domains.

### Git repository
1. User enters a repo URL (and optionally branch, build context and
   Dockerfile path in the advanced view). Private repos use an access token,
   stored server-side and never sent back to the browser.
2. SimplePanel runs `git clone --depth 1` into a temp dir, tars the build
   context, and calls Docker's `/build` API. The build log streams into the
   deployment record and is visible in the UI.
3. The image is tagged `simplepanel/<project>_<app>:<deployment-id>` and the
   service is updated as above.
4. Each app gets a secret **deploy webhook URL** so a push to GitHub/GitLab can
   trigger a redeploy.

Build type is **Automatic** (default), **Dockerfile** or **Static site**.
Automatic uses the Dockerfile when the repo has one and a static site build
otherwise.

**Static sites**: SimplePanel generates a two-stage Dockerfile
(`internal/deploy/static.go`). The first stage copies the repo into a
builder image (`node:22-alpine` by default, configurable, e.g. a Hugo
image), installs Node dependencies when there is a `package.json`
(npm/pnpm/yarn by lockfile) and runs the optional build command. The second
stage is `nginx:stable-alpine` serving the output directory on port 80 with
no directory listings, dotfiles blocked (except `.well-known`), asset cache
headers, gzip and an optional single-page-app fallback to `index.html`.

Base images are refreshed on every build for security updates. If the
registry refuses (for example a Docker Hub rate limit), the build is retried
with the images already on the server.

Automatic builds for server apps without a Dockerfile (Nixpacks or
buildpacks) are planned.

### Databases (PostgreSQL, MySQL, MariaDB, MongoDB, Redis)
1. User picks an engine and a name. SimplePanel generates a user, a random
   password (and a root password for MySQL/MariaDB) as environment variables
   and adds a named volume for the data directory.
   - MongoDB's user is the administrator the official image creates
     (`MONGO_INITDB_ROOT_*`, authenticated against `admin`), so the URL ends
     in `?authSource=admin`.
   - Redis has no user and its image reads no password variable, so the
     database gets a default command that starts
     `redis-server --appendonly yes --requirepass "$REDIS_PASSWORD"`. The
     command is visible (and editable) in Advanced view; the password
     itself only lives in the environment.
   - AI read-only access is only offered for the SQL engines.
2. The database is deployed like an image app (official images pinned to a
   major version, e.g. `postgres:17-alpine`), attached only to the project
   network.
3. The UI shows host, port, user, password and a connection URL to copy into
   an app's environment, e.g. `postgres://shop:…@shop_db:5432/shop`.

Advanced view can publish a database port to the internet; the UI warns that
this needs a firewall rule.

### Deploy webhooks
Each app has a secret webhook URL (`/api/hooks/deploy/<192-bit token>`),
which can be regenerated from the UI.
- A plain `POST` redeploys, which suits any CI/CD system.
- `?tag=1.4.2` on an image app switches the image tag, saves it and deploys.
- GitHub (`X-GitHub-Event`) and GitLab (`X-Gitlab-Event`) deliveries are
  recognised: only pushes to the app's branch (or the repository's default
  branch) deploy; other events are acknowledged and ignored.
- Webhook calls are rate-limited per IP.

### Export and import
`GET /api/projects/{id}/export` and `GET /api/apps/{id}/export` return a
versioned JSON document (`"simplepanel": 1`). `POST /api/import` recreates
a project, or a service inside an existing project.
- **No secrets out:** Git tokens, webhook tokens and database credentials
  are never exported. App env vars are included only with `?env=1`.
- **No secrets in:** imported database credential variables are dropped and
  regenerated; services from private repos must get a token again.
- **Strict validation:** unknown fields, trailing data and unsupported
  versions are rejected, every service goes through the same validation as
  the UI, and the whole document is validated before anything is created,
  so a bad import changes nothing. Domains already in use are skipped with a
  warning.

### MCP server (AI assistants)
`/mcp` speaks the MCP Streamable HTTP transport: one JSON-RPC message per
POST, answered with plain JSON (no SSE stream, no session state).
- **Auth:** `Authorization: Bearer sp_<64 hex>` API tokens, stored as
  SHA-256 hashes and shown once. Scopes are `read`, `manage` and `full`;
  `tools/list` only returns the tools the token's scope allows. Failed
  token attempts are rate-limited per IP, and requests with a foreign
  `Origin` are refused (DNS rebinding).
- **Same rules as the UI:** tools call the API handlers in-process, so
  validation and side effects are identical. Arguments are strictly
  decoded (unknown fields are errors) and services can be named
  `project/name`.
- **No secrets, at any scope:** env values, DB passwords, Git tokens and
  webhook URLs are never returned. Tool descriptions tell the AI to ask the
  user instead.
- **Audit log:** every changing call and every DB query is stored in
  `audit_log` with the token name and redacted arguments (env values and
  tokens replaced), and shown in Settings.
- **Read-only database access (opt-in per database):** SimplePanel creates
  `sp_ai_readonly` inside the database. PostgreSQL (15+) gets `CONNECT`,
  `USAGE` and `SELECT` on non-system schemas plus default privileges for
  tables the app user creates later; MySQL/MariaDB get `SELECT, SHOW VIEW`
  on the app's database. Queries run through the database's own client via
  `docker exec` in its container (no drivers in the panel, and the panel
  doesn't need to join the project network), in a read-only session with a
  10 s statement timeout. Plain queries are wrapped as
  `SELECT * FROM (…) LIMIT 501` and output is capped at 500 rows and 2 MB.
  The read-only user's password stays in SQLite and is never shown.

## v0.2 additions

### Users, roles and access

Users have a role: `owner` (the first account), `admin`, `developer` or
`viewer`. Admins see every project. Developers and viewers have
`all_projects` and/or per-project overrides in `project_access`. Each
request works out an access level for a project: View (1), Deploy (2:
create, configure, deploy, back up, restore into a new database) or Manage
(3: delete, restore over current data). The level is the lowest of the
user's role and, for API tokens, the token's scope (read = View,
manage = Deploy, full = Manage). Projects a user can't see answer 404.
Server settings, users, backup destinations and notification channels are
admin-only. Invite and password-reset links are one-time tokens valid for
seven days (`/#/invite/<token>`); only a SHA-256 of the token is stored.
Every change from the UI and from MCP tokens goes to `audit_log` with the
user.

### Backups

`internal/s3` is a small S3 client (SigV4, path-style or virtual-host
addressing, multipart uploads above 64 MB) with no SDK dependency.
`internal/backup` runs jobs:

- **Databases** are dumped with the engine's own tool inside the database
  container (`docker exec`): `pg_dump --clean --if-exists --no-owner`, or
  `mysqldump`/`mariadb-dump --single-transaction`. Restores pipe the dump
  back in the same way.
- **MongoDB** is dumped with `mongodump --archive` (all databases).
  `mongorestore --archive --drop` replaces the collections in the backup and
  skips `admin`, `config` and `local`, so a restore never changes users.
- **Redis** is saved with `redis-cli --rdb` (a point-in-time snapshot).
  Restoring stops the database, replaces its data volume with the snapshot
  (as `dump.rdb` and as the base of a fresh append-only file, since Redis
  ignores `dump.rdb` when AOF is on) and starts it again.
- **App volumes** are archived by a short-lived helper container
  (`alpine`, no network) that mounts the volumes read-only. Restoring
  scales the app to 0, replaces the volume contents and scales it back.
- **SimplePanel's config** is a `VACUUM INTO` snapshot of its SQLite file.

The stream is gzipped, optionally encrypted with age (scrypt passphrase per
destination) and uploaded as
`<folder>/<project>/<service>/YYYYMMDD-HHMMSS-<kind>.<ext>.gz[.age]`, where
`<ext>` is `sql`, `archive` (MongoDB), `rdb` (Redis), `tar` or `db`.
Schedules are five-field cron expressions checked every 30 seconds;
scheduled backups beyond the job's “keep” count are deleted. At most two
backups run at once, one per service.

### Monitoring

`internal/monitor` samples every 30 seconds: server CPU, memory and network
from `/proc` (the host's, mounted at `/host/proc`), disk from `statfs` on
the data folder, and per-service figures from Docker's one-shot container
stats, summed over a service's replicas. Samples go to the `metrics` table:
raw for 24 hours, then 5-minute averages for the retention period (14 days
by default). Alerts fire when a level stays above its threshold for N
minutes, and once more when it recovers. A service whose tasks keep failing
sends a “keeps crashing” event.

### Notifications

`internal/notify` sends events to Discord, Slack, Telegram, email (SMTP
with STARTTLS or TLS) and generic webhooks (JSON, signed with
`X-SimplePanel-Signature: sha256=<HMAC>` when a secret is set). Channels
pick their events and, optionally, projects. Secret settings are never sent
back to the browser. Each delivery is logged.

### Automatic subdomains

With a wildcard domain set, the Traefik file config gets one extra HTTPS
router per app for `<app>-<project>.<wildcard>`, pointing at the app's HTTP
port (its own setting, else its first domain's port, else 80). Apps can
turn it off.

## v0.3 additions

### Git providers (GitHub and GitLab)

Connections live in the `git_providers` table. An app links to one with
`git_provider_id` and `git_repo` (`owner/name`); its clone URL is still
stored, so unlinking keeps it deployable.

- **GitHub** uses the [app manifest flow](https://docs.github.com/apps/sharing-github-apps/registering-a-github-app-from-a-manifest):
  the browser form-POSTs a manifest to github.com, the owner approves it, and
  GitHub redirects back to `/api/git/github/callback` with a one-time code
  that SimplePanel converts into the app's ID, private key, webhook secret and
  client secret. Each install therefore owns its own GitHub App; there is no
  central app or relay. The callback is authenticated by a one-time `state`
  (10 minutes), because the `SameSite=Strict` session cookie isn't sent on a
  cross-site redirect. API calls use an RS256 app JWT exchanged for
  installation tokens, cached until shortly before they expire. Clones use
  an installation token scoped to the repository.
- **GitLab** (gitlab.com or self-hosted) uses an access token with `api`
  scope. Linking an app adds a project webhook pointing at
  `/api/hooks/gitlab/{id}` with a random `X-Gitlab-Token`.
- **Webhooks.** `/api/hooks/github` finds the connection by checking the
  `X-Hub-Signature-256` HMAC against each GitHub connection's secret.
  Events are parsed into one shape (push, pull request opened, updated,
  closed) and matched against linked apps by repository and branch.
- **Commit status and comments.** Deploys report pending, success or
  failure on the commit (context `SimplePanel: project/app`). Previews also
  upsert one comment on the pull request with the preview URL.
- **Deploy keys.** Unlinked apps can get an Ed25519 SSH key pair; the public
  half goes into the repository as a read-only deploy key and the app uses a
  `git@` URL. Clones run `ssh` with `StrictHostKeyChecking=accept-new` and a
  per-app known_hosts file, seeded with the published host keys of
  github.com, gitlab.com and bitbucket.org; other hosts are pinned on first
  use.

### Pull request previews

A preview is an ordinary app row with `parent_app_id` and `pr_number`, named
`<app>-pr-<n>`. On each pull request event the parent's settings are copied,
`preview_env` is applied on top of its variables, and the PR's head branch is
deployed with 1 replica, no published ports and its own volumes, reached on
its automatic subdomain (so previews need a wildcard domain). Closing the
pull request removes the service and its volumes. Limits and safety: at most
10 previews per app, pull requests from forks are skipped (their code is
never cloned), and previews never get backups, cron jobs or a deploy webhook
of their own.

### Cron jobs

`internal/cron` checks every few seconds for due jobs (standard 5-field
cron, times in the server's time zone) and runs each one with `docker exec`
in a running task container of the service, wrapped in `timeout` when the
image has it. Output keeps the last 64 KB, at most 4 jobs run at once, a job
still running when it comes due again is recorded as skipped, and the last
50 runs per job are kept. A failed run sends the `cron.failed`
notification.

### Public API and CLI

The REST API that the UI uses also accepts `Authorization: Bearer` tokens.
Tokens carry an `api_access` flag: only tokens created under API & CLI have
it, so tokens made for AI assistants stay MCP-only and can't use the REST
API to read secrets. A token's level is the lower of its scope and its
user's role. Requests that could grant more access (users, tokens, Git
connections, backup keys) are session-only. Token requests are logged in the
activity log with source `api`. Bearer requests skip the CSRF header check,
since browsers never attach them automatically.

`cmd/sp` is a small stdlib-only CLI built for Linux, macOS and Windows on
every release. It stores the URL and token in the user's config directory
(mode 0600); `SP_URL` and `SP_TOKEN` override it for CI. See
[API.md](API.md).

## Database Query tab

A built-in, read-only query tool on every database (no phpMyAdmin or
similar to deploy). Code: `internal/deploy/dbquery.go`, `mongoparse.go`,
`internal/api/query.go`, `web/src/components/QueryView.svelte`.

- **The database enforces read-only.** The first time someone uses the tab,
  SimplePanel creates a dedicated user `sp_query_ro` inside that database
  (password kept on the server, never shown): SELECT/SHOW VIEW only on
  PostgreSQL, MySQL and MariaDB (plus read-only sessions and a statement
  timeout per query), the `readAnyDatabase` role on MongoDB (no access to
  users or password hashes), and an ACL user with `+@read -@dangerous` on
  Redis. If the user goes missing (Redis restarts, a restore) the panel
  recreates it and retries once.
- **No exposed ports, no drivers.** Queries run through the database's own
  client inside its container (`psql`, `mysql`/`mariadb`, `mongosh`,
  `redis-cli`), like AI read-only access.
- **MongoDB queries are parsed, not evaluated.** `db.orders.find({...}).sort(...)`
  is parsed by the panel into collection, method and Extended JSON
  arguments and handed to a fixed mongosh script as data, so no JavaScript
  the user types ever runs (mongosh could otherwise read files in the
  container). Only find, findOne, aggregate (without `$out`/`$merge`),
  countDocuments, estimatedDocumentCount and distinct are accepted.
- **Who:** owners and admins always; developers and viewers need the
  "Query databases" permission (`users.db_query`, off by default) and
  access to the project.
- **Limits:** 30 s and 1,000 rows by default, up to 120 s and 10,000 rows
  (Advanced view). Every query and table browse is in the activity log with
  its text. History is per user (last 100); saved queries are shared per
  database.

## Security model

- **First-run setup:** the first visitor creates the admin account. Until that
  happens, the install script prints the panel URL so the owner can claim it
  immediately. Setup is locked once an admin exists.
- **Passwords:** bcrypt (cost 12), minimum length 10.
- **Sessions:** 256-bit random tokens, stored server-side as SHA-256 hashes;
  cookie is `HttpOnly`, `SameSite=Strict`, and `Secure` when served over HTTPS.
- **CSRF:** `SameSite=Strict` cookie plus a required `X-SimplePanel` header on
  every state-changing API request (browsers cannot add custom headers
  cross-site without a CORS preflight, which we never allow).
- **Login throttling:** per-IP rate limit on login attempts.
- **Secrets:** Git tokens are never returned by the API and are masked in
  build logs. They reach `git` through `GIT_CONFIG_*` environment variables
  scoped to the repository host, never the command line or the cloned
  repo's config. Clones only allow the `https` and `ssh` protocols; SSH
  deploy keys are never returned by the API. Secrets are stored
  unencrypted in SQLite on a root-only volume; anyone with root on the host
  already controls Docker, so encryption at rest is deferred.
- **Input validation:** project/app names are restricted to
  `[a-z0-9-]`, domains are validated, image references are validated, and
  nothing user-supplied is passed through a shell (`git` is executed with an
  argument list, never `sh -c`).
- **Security headers:** strict CSP, `X-Frame-Options: DENY`,
  `Referrer-Policy: same-origin`, `X-Content-Type-Options: nosniff`.
- The panel listens on port 3000 for first setup. Once the owner sets a panel
  domain, it is reachable over HTTPS through Traefik and port 3000 can be
  closed with the firewall.

## Install

```
curl -fsSL https://raw.githubusercontent.com/SimpleAOB/SimplePanel/main/install.sh | sudo sh
```

The script:
1. Checks it is running as root on a supported Linux (Ubuntu/Debian first).
2. Installs Docker from Docker's official script if missing.
3. Initialises Swarm (`docker swarm init`) if needed.
4. Creates the `simplepanel` overlay network and volumes.
5. Starts the `simplepanel` and `simplepanel-traefik` services.
6. Prints the panel URL.

Re-running the script upgrades SimplePanel in place.

## Repository layout

```
cmd/simplepanel/      main entrypoint
cmd/sp/               the sp command-line client
internal/api/         HTTP handlers, auth, middleware
internal/cron/        cron job scheduler
internal/gitprovider/ GitHub App and GitLab API clients, webhook parsing
internal/docker/      minimal Docker Engine API client
internal/deploy/      git clone + build + service create/update
internal/proxy/       Traefik dynamic config writer
internal/store/       SQLite persistence and migrations
internal/web/         embedded UI assets
web/                  Svelte UI source (theme in web/src/theme.css)
install.sh            one-line installer
Dockerfile            builds the SimplePanel image
```

## Out of scope for now (planned)

- Webhook signature verification (HMAC) in addition to the secret URL
- OAuth for the MCP endpoint (tokens are pasted into the AI app for now)
- App templates
- Bitbucket and Gitea connections (they work today through webhook URLs and
  deploy keys)
- Builds without a Dockerfile (Nixpacks)
- Multi-node clusters with a built-in registry
- Two-factor sign-in
