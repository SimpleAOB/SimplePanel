#!/bin/sh
# SimplePanel installer.
#
#   curl -fsSL https://raw.githubusercontent.com/SimpleAOB/SimplePanel/main/install.sh | sudo sh
#
# Installs Docker (if missing), enables Docker Swarm, and starts SimplePanel
# and Traefik as Swarm services. Re-running the script upgrades SimplePanel.
#
# Optional environment variables:
#   SP_IMAGE        SimplePanel image   (default ghcr.io/simpleaob/simplepanel:latest)
#   TRAEFIK_IMAGE   Traefik image       (default traefik:v3.6)
#   ACME_EMAIL      email for Let's Encrypt expiry notices (optional)
#   SP_PORT         port for the panel  (default 3000)

set -eu

SP_IMAGE="${SP_IMAGE:-ghcr.io/simpleaob/simplepanel:latest}"
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

if docker service inspect simplepanel >/dev/null 2>&1; then
	existing_install=true
fi

# Ports must be free on a fresh install (an upgrade already owns them).
if [ "$existing_install" = false ] && need ss; then
	for p in 80 443 "$SP_PORT"; do
		if ss -ltnH "sport = :$p" 2>/dev/null | grep -q .; then
			die "port $p is already in use; stop the service using it (for example nginx or apache) and re-run"
		fi
	done
fi

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

info "Pulling $SP_IMAGE"
if ! docker pull --quiet "$SP_IMAGE" >/dev/null; then
	docker image inspect "$SP_IMAGE" >/dev/null 2>&1 || die "could not pull $SP_IMAGE"
	warn "pull failed; using the local copy of $SP_IMAGE"
fi

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
