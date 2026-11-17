#!/bin/sh
# SimplePanel installer.
#
#   curl -fsSL https://raw.githubusercontent.com/SimpleAOB/SimplePanel/main/install.sh | sudo sh
#
# Installs Docker (if missing), enables Docker Swarm, and starts SimplePanel
# and Traefik as Swarm services. Re-running the script upgrades SimplePanel.
#
# Optional environment variables:
#   SP_IMAGE        SimplePanel image   (default: the latest release,
#                   ghcr.io/simpleaob/simplepanel:latest, or :edge, built
#                   from main, until the first release is published)
#   TRAEFIK_IMAGE   Traefik image       (default traefik:v3.6)
#   ACME_EMAIL      email for Let's Encrypt expiry notices (optional)
#   SP_PORT         port for the panel  (default 3000)

set -eu

SP_REPO=ghcr.io/simpleaob/simplepanel
SP_IMAGE="${SP_IMAGE:-}"
TRAEFIK_IMAGE="${TRAEFIK_IMAGE:-traefik:v3.6}"
ACME_EMAIL="${ACME_EMAIL:-}"
SP_PORT="${SP_PORT:-3000}"
NETWORK=simplepanel

info() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33mwarning:\033[0m %s\n' "$*" >&2; }
die() {
	printf '\033[1;31merror:\033[0m %s\n' "$*" >&2
	exit 1
}

# ---- Preflight ---------------------------------------------------------------

[ "$(id -u)" -eq 0 ] || die "please run as root, e.g. curl -fsSL <url> | sudo sh"
[ "$(uname -s)" = "Linux" ] || die "SimplePanel needs Linux"
[ -f /.dockerenv ] && die "run this on the server itself, not inside a container"

case "$SP_PORT" in
'' | *[!0-9]*) die "SP_PORT must be a number" ;;
esac

if [ -r /etc/os-release ]; then
	# shellcheck disable=SC1091
	. /etc/os-release
	case "${ID:-}" in
	ubuntu | debian) ;;
	*) warn "SimplePanel is tested on Ubuntu and Debian; continuing on ${PRETTY_NAME:-unknown}" ;;
	esac
fi

need() { command -v "$1" >/dev/null 2>&1; }

if ! need curl; then
	info "Installing curl"
	if need apt-get; then
		apt-get update -qq && apt-get install -y -qq curl ca-certificates >/dev/null
	else
		die "curl is required"
	fi
fi

existing_install=false

# ---- Docker ------------------------------------------------------------------

if ! need docker; then
	info "Installing Docker"
	curl -fsSL https://get.docker.com | sh
	systemctl enable --now docker >/dev/null 2>&1 || true
fi

docker info >/dev/null 2>&1 || die "Docker is installed but not running (try: systemctl start docker)"

has_service() { docker service inspect "$1" >/dev/null 2>&1; }

if has_service simplepanel; then
	existing_install=true
fi

# Ports must be free, except those held by SimplePanel's own services: an
# upgrade, or a re-run after an install that stopped half way, already
# owns them.
check_port() {
	if need ss && ss -ltnH "sport = :$1" 2>/dev/null | grep -q .; then
		die "port $1 is already in use; stop the service using it (for example nginx or apache) and re-run"
	fi
}
if ! has_service simplepanel-traefik; then
	check_port 80
	check_port 443
fi
if ! has_service simplepanel; then
	check_port "$SP_PORT"
fi

# ---- Images ------------------------------------------------------------------
# Pull everything before changing anything, so a failed download leaves the
# server as it was.

# pull IMAGE: pull, or fall back to a local copy. Sets pull_error on failure.
pull_error=
pull() {
	if out=$(docker pull --quiet "$1" 2>&1); then
		return 0
	fi
	pull_error=$out
	if docker image inspect "$1" >/dev/null 2>&1; then
		warn "could not pull $1; using the copy already on this server"
		return 0
	fi
	return 1
}

explain_pull_error() {
	printf '%s\n' "$pull_error" | tail -n 3 >&2
	case "$pull_error" in
	*unauthorized* | *denied* | *"authentication required"*)
		die "the registry refused to send $1.
  The image is private, or the tag doesn't exist yet. If you published it
  yourself, make the package public on GitHub (Packages > simplepanel >
  Package settings > Change visibility), or run 'docker login ghcr.io' first." ;;
	*"not found"* | *"manifest unknown"*)
		die "$1 doesn't exist. Check SP_IMAGE." ;;
	*)
		die "could not pull $1. Check the server's internet connection and re-run." ;;
	esac
}

if [ -n "$SP_IMAGE" ]; then
	info "Pulling $SP_IMAGE"
	pull "$SP_IMAGE" || explain_pull_error "$SP_IMAGE"
else
	# Prefer the latest release; until the first one exists, use the build of
	# main (:edge). An install that already runs :edge stays on it.
	current=$(docker service inspect --format '{{.Spec.TaskTemplate.ContainerSpec.Image}}' simplepanel 2>/dev/null || true)
	case "${current%%@*}" in
	*:edge) candidates="$SP_REPO:edge" ;;
	*) candidates="$SP_REPO:latest $SP_REPO:edge" ;;
	esac
	for c in $candidates; do
		info "Pulling $c"
		if pull "$c"; then
			SP_IMAGE=$c
			break
		fi
	done
	[ -n "$SP_IMAGE" ] || explain_pull_error "$SP_REPO"
fi

info "Pulling $TRAEFIK_IMAGE"
pull "$TRAEFIK_IMAGE" || explain_pull_error "$TRAEFIK_IMAGE"

# ---- Swarm -------------------------------------------------------------------

private_ip() {
	ip -4 route get 1.1.1.1 2>/dev/null | awk '{for (i = 1; i <= NF; i++) if ($i == "src") { print $(i + 1); exit }}'
}

swarm_state=$(docker info --format '{{.Swarm.LocalNodeState}}')
if [ "$swarm_state" != "active" ]; then
	info "Initialising Docker Swarm"
	addr=$(private_ip)
	if [ -n "$addr" ]; then
		docker swarm init --advertise-addr "$addr" >/dev/null
	else
		docker swarm init >/dev/null
	fi
fi
[ "$(docker info --format '{{.Swarm.ControlAvailable}}')" = "true" ] || die "this node is not a Swarm manager"

# Swarm's virtual IPs need the ip_vs kernel module. Some VPS kernels (LXC,
# OpenVZ) lack it; fall back to DNS round-robin there.
endpoint_mode=vip
if ! { [ -d /proc/net/ip_vs ] || modprobe ip_vs >/dev/null 2>&1 || grep -q '^ip_vs ' /proc/modules 2>/dev/null; }; then
	warn "the ip_vs kernel module is unavailable; apps will use DNS round-robin load balancing"
	endpoint_mode=dnsrr
fi

# ---- Network and volumes -----------------------------------------------------

if ! docker network inspect "$NETWORK" >/dev/null 2>&1; then
	info "Creating overlay network '$NETWORK'"
	docker network create --driver overlay --label simplepanel.managed=true "$NETWORK" >/dev/null
fi

for v in simplepanel-data simplepanel-traefik-dynamic simplepanel-traefik-certs; do
	docker volume inspect "$v" >/dev/null 2>&1 || docker volume create --label simplepanel.managed=true "$v" >/dev/null
done

# ---- Traefik -----------------------------------------------------------------

set -- \
	--entrypoints.web.address=:80 \
	--entrypoints.websecure.address=:443 \
	--entrypoints.websecure.http3=true \
	--providers.file.directory=/etc/traefik/dynamic \
	--providers.file.watch=true \
	--certificatesresolvers.letsencrypt.acme.httpchallenge=true \
	--certificatesresolvers.letsencrypt.acme.httpchallenge.entrypoint=web \
	--certificatesresolvers.letsencrypt.acme.storage=/certs/acme.json \
	--ping=true \
	--log.level=INFO \
	--global.checknewversion=false \
	--global.sendanonymoususage=false
if [ -n "$ACME_EMAIL" ]; then
	set -- "$@" "--certificatesresolvers.letsencrypt.acme.email=$ACME_EMAIL"
fi

if docker service inspect simplepanel-traefik >/dev/null 2>&1; then
	info "Updating Traefik"
	docker service update --quiet --detach --image "$TRAEFIK_IMAGE" --args "$*" simplepanel-traefik >/dev/null
else
	info "Starting Traefik"
	docker service create --quiet --detach \
		--name simplepanel-traefik \
		--network "$NETWORK" \
		--constraint node.role==manager \
		--publish mode=host,target=80,published=80,protocol=tcp \
		--publish mode=host,target=443,published=443,protocol=tcp \
		--publish mode=host,target=443,published=443,protocol=udp \
		--mount type=volume,source=simplepanel-traefik-dynamic,target=/etc/traefik/dynamic,readonly \
		--mount type=volume,source=simplepanel-traefik-certs,target=/certs \
		--update-order stop-first \
		--label simplepanel.managed=true \
		"$TRAEFIK_IMAGE" "$@" >/dev/null
fi

# ---- SimplePanel -------------------------------------------------------------

if [ "$existing_install" = true ]; then
	info "Upgrading SimplePanel"
	# Not detached: wait until the new task has replaced the old one, so the
	# health check below talks to the upgraded panel.
	docker service update --quiet --image "$SP_IMAGE" \
		--env-add "SP_ENDPOINT_MODE=$endpoint_mode" --force simplepanel >/dev/null
else
	info "Starting SimplePanel"
	# SimplePanel stops before its replacement starts: SQLite has one writer.
	docker service create --quiet --detach \
		--name simplepanel \
		--network "$NETWORK" \
		--constraint node.role==manager \
		--publish "mode=host,target=3000,published=$SP_PORT,protocol=tcp" \
		--mount type=bind,source=/var/run/docker.sock,target=/var/run/docker.sock \
		--mount type=volume,source=simplepanel-data,target=/data \
		--mount type=volume,source=simplepanel-traefik-dynamic,target=/etc/traefik/dynamic \
		--env "SP_ENDPOINT_MODE=$endpoint_mode" \
		--update-order stop-first \
		--label simplepanel.managed=true \
		"$SP_IMAGE" >/dev/null
fi

# ---- Done --------------------------------------------------------------------

info "Waiting for SimplePanel to start"
i=0
until curl -fsS "http://127.0.0.1:$SP_PORT/api/health" >/dev/null 2>&1; do
	i=$((i + 1))
	[ "$i" -gt 60 ] && die "SimplePanel did not start; check: docker service logs simplepanel"
	sleep 2
done

public_ip=$(curl -4fsS --max-time 5 https://api.ipify.org 2>/dev/null || true)
[ -n "$public_ip" ] || public_ip=$(private_ip)

printf '\n\033[1;32mSimplePanel is running.\033[0m\n\n'
printf '  Open  http://%s:%s  to create your admin account.\n' "${public_ip:-<server-ip>}" "$SP_PORT"
if [ "$existing_install" = false ]; then
	printf '  Do this now: the first person to open the page claims the server.\n'
fi
printf '\n  Ports 80 and 443 must be reachable for domains and HTTPS.\n'
printf '  Tip: set a panel domain in Settings, then firewall port %s.\n\n' "$SP_PORT"
