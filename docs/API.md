# SimplePanel REST API

The web UI and the `sp` CLI both use this API, so anything you can click you
can also script. All paths start with `/api` and all bodies are JSON.

## Authentication

Create a token under **Settings → API & CLI** and send it as a Bearer token:

```sh
curl -H "Authorization: Bearer sp_..." https://panel.example.com/api/projects
```

- A token has an access level: **Read-only**, **Manage** or **Full**. It can
  never do more than the role of the user who made it (Viewer, Developer,
  Admin, Owner), and Developers only reach their own projects.
- Tokens made under **Connect an AI** work only on the MCP endpoint, not on
  this API, so an AI assistant can't use the REST API to read secrets.
- Tokens can't manage users, tokens or Git connections, change your
  password, or reveal a backup encryption key. Sign in to the panel for that.
- Changes made with a token are in the activity log with source `api`.
- Requests signed in with the browser cookie must also send
  `X-SimplePanel: 1` on anything but GET (CSRF protection). Bearer requests
  don't need it.

Errors come back as `{"error": "message"}` with a 4xx or 5xx status.

## Common calls

| What | Call |
| --- | --- |
| Who am I | `GET /api/me` |
| Projects | `GET /api/projects`, `POST /api/projects {"name"}` |
| Services in a project | `GET /api/projects/{id}/apps` |
| Find a service by name | `GET /api/services/{project}/{name}` |
| Service details | `GET /api/apps/{id}` |
| Change a service | `PUT /api/apps/{id}` (send the full settings, as `GET` returns them) |
| Set or remove variables | `PATCH /api/apps/{id}/env {"set": {"KEY": "value"}, "unset": ["OLD"]}` |
| Deploy | `POST /api/apps/{id}/deploy` (returns the deployment) |
| Deployment status and build log | `GET /api/deployments/{id}` |
| Deployment history | `GET /api/apps/{id}/deployments` |
| Logs | `GET /api/apps/{id}/logs?tail=300` |
| Restart, stop, start | `POST /api/apps/{id}/restart` (`/stop`, `/start`) |
| Pull request previews | `GET /api/apps/{id}/previews` |
| Cron jobs | `GET /api/apps/{id}/cron`, `POST /api/apps/{id}/cron {"name","schedule","command","timeout"}` |
| Change or remove a cron job | `PUT /api/cron/{id}`, `DELETE /api/cron/{id}` |
| Run a cron job now | `POST /api/cron/{id}/run` (returns the run) |
| Cron history and output | `GET /api/cron/{id}/runs`, `GET /api/cron-runs/{id}` |
| Backups | `GET /api/apps/{id}/backups`, `POST /api/apps/{id}/backups` |
| Metrics | `GET /api/apps/{id}/metrics`, `GET /api/metrics/server` |
| Activity log | `GET /api/audit?limit=100&source=api` |

A deploy returns at once, before the build finishes. Poll
`GET /api/deployments/{id}` until its status is `succeeded` or `failed`, or
use `sp deploy --wait`, which does that for you.

## Webhooks (no token)

| Path | Used by |
| --- | --- |
| `POST /api/hooks/deploy/{token}` | the app's secret deploy URL; `?tag=` for image apps |
| `POST /api/hooks/github` | GitHub Apps created by SimplePanel (HMAC-signed) |
| `POST /api/hooks/gitlab/{id}` | GitLab connections (`X-Gitlab-Token` secret) |

## The `sp` CLI

Download `sp` for your platform from the
[latest release](https://github.com/SimpleAOB/SimplePanel/releases/latest),
then:

```sh
sp login https://panel.example.com      # asks for a token
sp services                              # every service and its status
sp deploy shop/web --wait                # exits non-zero if the deploy fails
sp deploy shop/api --tag 1.4.2 --wait    # image apps: deploy another tag
sp logs shop/web -f
sp env set shop/web LOG_LEVEL=debug --deploy
sp cron run shop/worker "Nightly report" --wait
sp previews shop/web
sp api GET /projects                     # call any endpoint
```

Add `--json` for machine-readable output. In CI, set `SP_URL` and `SP_TOKEN`
instead of running `sp login`:

```yaml
- name: Deploy
  env:
    SP_URL: https://panel.example.com
    SP_TOKEN: ${{ secrets.SP_TOKEN }}
  run: |
    curl -fsSL -o sp https://github.com/SimpleAOB/SimplePanel/releases/latest/download/sp-linux-amd64
    chmod +x sp
    ./sp deploy shop/web --wait
```
