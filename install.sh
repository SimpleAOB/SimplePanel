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
#   SP_NO_SWAP=1    don't add a swap file when the server has none
#   SP_NO_TUNING=1  don't change kernel memory settings (sysctl)
#   SP_PROTECT_HOST=1  set up the server-wide memory cap on a server where
#                   Docker is already running containers (this restarts
#                   Docker once). New servers get it automatically.
#   SP_PROTECT_HOST=0  never set up the memory cap

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

timeout 60 docker info >/dev/null 2>&1 || die "Docker is installed but not running or not responding (try: systemctl restart docker)"

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
# Swarm normally looks the tag up in the registry again when it creates the
# service; resolve_image=--no-resolve-image makes it use the local copy.
pull_error=
resolve_image=
pull() {
	if out=$(docker pull --quiet "$1" 2>&1); then
		return 0
	fi
	pull_error=$out
	if docker image inspect "$1" >/dev/null 2>&1; then
		warn "could not pull $1; using the copy already on this server"
		resolve_image=--no-resolve-image
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

sp_resolve_image=$resolve_image
resolve_image=
info "Pulling $TRAEFIK_IMAGE"
pull "$TRAEFIK_IMAGE" || explain_pull_error "$TRAEFIK_IMAGE"
traefik_resolve_image=$resolve_image

# ---- Memory safety -------------------------------------------------------------
# A server that runs out of memory doesn't just kill one app: the kernel
# starts evicting everything, Docker and Swarm stop answering and even SSH
# hangs. Three things keep that from happening:
#   1. a swap file, so a short spike slows things down instead of failing;
#   2. kernel settings that keep some memory free for the system;
#   3. a cap on what all containers together may use (simplepanel.slice),
#      leaving memory for the kernel, SSH and Docker itself. If containers
#      hit it, the kernel stops the biggest one instead of the server
#      locking up. The panel sizes each service's own limit within it.

mem_total_mb=$(awk '/^MemTotal:/ {print int($2 / 1024)}' /proc/meminfo)
# Must match deploy.AutoReserveMB: 15% of RAM, at least 384 MB, at most 2 GB.
reserve_mb=$((mem_total_mb * 15 / 100))
[ "$reserve_mb" -lt 384 ] && reserve_mb=384
[ "$reserve_mb" -gt 2048 ] && reserve_mb=2048
cap_mb=$((mem_total_mb - reserve_mb))
[ "$cap_mb" -lt 256 ] && cap_mb=256
swap_cap_mb=$((cap_mb / 2))

if [ "${SP_NO_SWAP:-}" != 1 ] && [ -z "$(swapon --noheadings 2>/dev/null)" ] && [ ! -e /swapfile ]; then
	free_mb=$(df -Pm / | awk 'NR == 2 {print $4}')
	if [ "${free_mb:-0}" -gt 8192 ]; then
		info "Adding a 2 GB swap file (/swapfile)"
		if { fallocate -l 2G /swapfile 2>/dev/null || dd if=/dev/zero of=/swapfile bs=1M count=2048 status=none; } &&
			chmod 600 /swapfile && mkswap /swapfile >/dev/null && swapon /swapfile; then
			grep -q '^/swapfile ' /etc/fstab || echo '/swapfile none swap sw 0 0' >>/etc/fstab
		else
			warn "could not add a swap file; continuing without one"
			swapoff /swapfile 2>/dev/null || true
			rm -f /swapfile
		fi
	else
		warn "less than 8 GB of disk free; not adding a swap file"
	fi
fi

if [ "${SP_NO_TUNING:-}" != 1 ] && [ -d /etc/sysctl.d ]; then
	min_free=65536
	[ "$mem_total_mb" -lt 2048 ] && min_free=32768
	cur_free=$(cat /proc/sys/vm/min_free_kbytes 2>/dev/null || echo 0)
	[ "$cur_free" -gt "$min_free" ] && min_free=$cur_free
	cat >/etc/sysctl.d/90-simplepanel.conf <<SYSCTL
# Written by the SimplePanel installer (re-run it to update; delete to undo).
# Prefer dropping cache over swapping apps out, but use swap before the
# kernel runs out of memory.
vm.swappiness = 10
# Keep some memory free for the kernel itself, so the server stays
# reachable under memory pressure.
vm.min_free_kbytes = $min_free
SYSCTL
	sysctl -q -p /etc/sysctl.d/90-simplepanel.conf >/dev/null 2>&1 || warn "could not apply kernel memory settings"
fi

# The memory cap needs systemd, cgroup v2 and Docker's systemd cgroup
# driver (the default on current Ubuntu and Debian).
protect=${SP_PROTECT_HOST:-auto}
host_cap=
if [ "$protect" != 0 ] && [ -d /run/systemd/system ] &&
	[ "$(stat -fc %T /sys/fs/cgroup 2>/dev/null)" = cgroup2fs ] &&
	[ "$(docker info --format '{{.CgroupDriver}}' 2>/dev/null)" = systemd ]; then
	cat >/etc/systemd/system/simplepanel.slice <<SLICE
# Written by the SimplePanel installer. Every container (apps, databases,
# SimplePanel and Traefik) runs in this slice, which may use at most
# ${cap_mb} MB, leaving ${reserve_mb} MB for the system, SSH and Docker.
[Unit]
Description=SimplePanel containers
Before=slices.target

[Slice]
MemoryAccounting=yes
MemoryMax=${cap_mb}M
# Some swap helps through short peaks; unlimited swap lets busy apps drag
# the whole server into swapping.
MemorySwapMax=${swap_cap_mb}M
# Under CPU contention, the system (SSH, Docker) wins over containers.
CPUWeight=50
SLICE
	systemctl daemon-reload
	# CgroupParent isn't in every Docker version's info; read the config.
	current_parent=$(sed -n 's/.*"cgroup-parent"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' /etc/docker/daemon.json 2>/dev/null | head -n 1)
	if [ "$current_parent" = simplepanel.slice ]; then
		# Already set up: the new limits apply straight away.
		systemctl set-property --runtime simplepanel.slice "MemoryMax=${cap_mb}M" "MemorySwapMax=${swap_cap_mb}M" >/dev/null 2>&1 || true
		host_cap=$cap_mb
	elif [ -n "$current_parent" ]; then
		warn "/etc/docker/daemon.json already puts containers in \"$current_parent\"; leaving it alone, so containers are not capped server-wide"
	else
		running=$(docker ps -q 2>/dev/null | wc -l)
		if [ "$protect" = 1 ] || { [ "$protect" = auto ] && [ "$running" -eq 0 ]; }; then
			if [ ! -s /etc/docker/daemon.json ]; then
				mkdir -p /etc/docker
				cat >/etc/docker/daemon.json <<'JSON'
{
  "cgroup-parent": "simplepanel.slice",
  "log-driver": "json-file",
  "log-opts": { "max-size": "10m", "max-file": "3" }
}
JSON
			elif need python3; then
				cp /etc/docker/daemon.json /etc/docker/daemon.json.simplepanel-backup
				python3 - <<'PY'
import json
p = "/etc/docker/daemon.json"
with open(p) as f:
    cfg = json.load(f)
cfg["cgroup-parent"] = "simplepanel.slice"
with open(p, "w") as f:
    json.dump(cfg, f, indent=2)
    f.write("\n")
PY
			else
				warn "/etc/docker/daemon.json exists and python3 is missing; add \"cgroup-parent\": \"simplepanel.slice\" to it yourself"
			fi
			if grep -q '"cgroup-parent"[[:space:]]*:[[:space:]]*"simplepanel.slice"' /etc/docker/daemon.json 2>/dev/null; then
				info "Capping containers at ${cap_mb} MB (keeping ${reserve_mb} MB for the system); restarting Docker"
				systemctl restart docker
				i=0
				until timeout 30 docker info >/dev/null 2>&1; do
					i=$((i + 1))
					[ "$i" -gt 30 ] && die "Docker did not come back after restarting; check: journalctl -u docker"
					sleep 2
				done
				host_cap=$cap_mb
			fi
		else
			warn "containers are not capped server-wide yet. To protect this server from running out of memory, re-run with SP_PROTECT_HOST=1 (restarts Docker and its containers once)."
		fi
	fi
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
	# shellcheck disable=SC2086
	docker service update --quiet --detach $traefik_resolve_image --image "$TRAEFIK_IMAGE" --args "$*" \
		--log-driver json-file --log-opt max-size=10m --log-opt max-file=3 simplepanel-traefik >/dev/null
else
	info "Starting Traefik"
	# shellcheck disable=SC2086
	docker service create --quiet --detach $traefik_resolve_image \
		--name simplepanel-traefik \
		--network "$NETWORK" \
		--constraint node.role==manager \
		--publish mode=host,target=80,published=80,protocol=tcp \
		--publish mode=host,target=443,published=443,protocol=tcp \
		--publish mode=host,target=443,published=443,protocol=udp \
		--mount type=volume,source=simplepanel-traefik-dynamic,target=/etc/traefik/dynamic,readonly \
		--mount type=volume,source=simplepanel-traefik-certs,target=/certs \
		--update-order stop-first \
		--log-driver json-file --log-opt max-size=10m --log-opt max-file=3 \
		--label simplepanel.managed=true \
		"$TRAEFIK_IMAGE" "$@" >/dev/null
fi

# ---- SimplePanel -------------------------------------------------------------

# The setup code: until the admin account exists, only someone with this
# code (printed below) can create it, so nobody else can claim the server.
new_setup_code() {
	od -An -N8 -tx1 /dev/urandom | tr -d ' \n' | tr 'a-f' 'A-F' | cut -c1-12
}
current_setup_code() {
	docker service inspect simplepanel --format '{{range .Spec.TaskTemplate.ContainerSpec.Env}}{{println .}}{{end}}' 2>/dev/null |
		sed -n 's/^SP_SETUP_CODE=//p'
}


if [ "$existing_install" = true ]; then
	info "Upgrading SimplePanel"
	# Not detached: wait until the new task has replaced the old one, so the
	# health check below talks to the upgraded panel.
	# Installs from before v0.2 lack the read-only /proc mount used for
	# server monitoring.
	proc_mount=""
	if ! docker service inspect simplepanel --format '{{range .Spec.TaskTemplate.ContainerSpec.Mounts}}{{.Target}} {{end}}' | grep -q '/host/proc'; then
		proc_mount="--mount-add type=bind,source=/proc,target=/host/proc,readonly"
	fi
	# shellcheck disable=SC2086
	# A panel that was never set up gets a setup code too.
	setup_env=""
	if curl -fsS --max-time 5 "http://127.0.0.1:$SP_PORT/api/setup" 2>/dev/null | grep -q '"needsSetup":true' &&
		[ -z "$(current_setup_code)" ]; then
		setup_env="--env-add SP_SETUP_CODE=$(new_setup_code)"
	fi
	# shellcheck disable=SC2086
	docker service update --quiet $sp_resolve_image --image "$SP_IMAGE" $proc_mount $setup_env \
		--env-add "SP_ENDPOINT_MODE=$endpoint_mode" --env-add "SP_MEMORY_CAP_MB=${host_cap:-0}" \
		--env-add "SP_PUBLIC_PORT=$SP_PORT" \
		--log-driver json-file --log-opt max-size=10m --log-opt max-file=3 \
		--force simplepanel >/dev/null
else
	info "Starting SimplePanel"
	# SimplePanel stops before its replacement starts: SQLite has one writer.
	# shellcheck disable=SC2086
	docker service create --quiet --detach $sp_resolve_image \
		--name simplepanel \
		--network "$NETWORK" \
		--constraint node.role==manager \
		--publish "mode=host,target=3000,published=$SP_PORT,protocol=tcp" \
		--mount type=bind,source=/var/run/docker.sock,target=/var/run/docker.sock \
		--mount type=volume,source=simplepanel-data,target=/data \
		--mount type=bind,source=/proc,target=/host/proc,readonly \
		--mount type=volume,source=simplepanel-traefik-dynamic,target=/etc/traefik/dynamic \
		--env "SP_ENDPOINT_MODE=$endpoint_mode" \
		--env "SP_MEMORY_CAP_MB=${host_cap:-0}" \
		--env "SP_PUBLIC_PORT=$SP_PORT" \
		--env "SP_SETUP_CODE=$(new_setup_code)" \
		--update-order stop-first \
		--log-driver json-file --log-opt max-size=10m --log-opt max-file=3 \
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
# Ask the panel rather than trusting existing_install: a re-run after an
# install that never finished still has no admin account.
setup_state=$(curl -fsS "http://127.0.0.1:$SP_PORT/api/setup" 2>/dev/null || true)
if printf '%s' "$setup_state" | grep -q '"needsSetup":true'; then
	code=$(current_setup_code)
	# Only promise a code the panel actually checks: images from before
	# setup codes (v1.0.0 and older) ignore SP_SETUP_CODE and don't report
	# needsCode, so the page would not ask for it.
	if [ -n "$code" ] && printf '%s' "$setup_state" | grep -q '"needsCode":true'; then
		printf '  Setup code:  \033[1m%s\033[0m  (the page asks for it)\n' "$code"
	else
		printf '  \033[1;33mDo this now:\033[0m the first person to open the page claims the server.\n'
		if [ -n "$code" ]; then
			printf '  %s is older than setup codes, so the page will not ask for one.\n' "$SP_IMAGE"
		fi
	fi
fi
if [ -n "$host_cap" ]; then
	printf '\n  Memory: containers may use %s MB; %s MB is kept for the system.\n' "$host_cap" "$reserve_mb"
fi
printf '\n  Ports 80 and 443 must be reachable for domains and HTTPS.\n'
printf '  Tip: set a panel domain in Settings, then firewall port %s.\n\n' "$SP_PORT"
