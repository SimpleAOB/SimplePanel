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

The installer sets up Docker, Docker Swarm, Traefik and SimplePanel. Run it
again at any time to upgrade.

| Variable        | Default                                  | Purpose                                   |
| --------------- | ---------------------------------------- | ----------------------------------------- |
| `SP_IMAGE`      | `ghcr.io/simpleaob/simplepanel:latest`   | SimplePanel image to run                  |
| `TRAEFIK_IMAGE` | `traefik:v3.6`                           | Traefik image                             |
| `ACME_EMAIL`    | _(none)_                                 | Email for Let's Encrypt expiry notices    |
| `SP_PORT`       | `3000`                                   | Port the panel listens on                 |

Example: `curl -fsSL … | sudo ACME_EMAIL=you@example.com sh`

## What you can do in v0.1

- **Deploy from Git**: any public or private (token) HTTPS repository with a
  `Dockerfile`. Pick a branch, a build path and a Dockerfile path in the
  advanced view.
- **Deploy a Docker image** from Docker Hub, GHCR or any registry.
- **Domains with automatic HTTPS** via Let's Encrypt.
- **Databases**: PostgreSQL, MySQL and MariaDB with generated credentials, a
  persistent volume and a copyable connection URL. Databases are only
  reachable from apps in the same project.
- **Zero-downtime deploys**: new containers start before old ones stop.
- **Deploy webhooks** for CI/CD (see below).
- **Logs**: live app logs and build logs side by side.
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
