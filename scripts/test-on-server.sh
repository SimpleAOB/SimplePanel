#!/bin/sh
# Tests SimplePanel's memory protection on a real, disposable server.
#
# It installs SimplePanel from a branch (building the image on the server),
# then runs a container that tries to use far more memory than the server
# has, and checks that the server, SSH, Docker, Swarm and the panel keep
# answering while the kernel stops only that container.
#
# DO NOT run this on a server you care about: it fills its memory on
# purpose. Run as root:
#
#   curl -fsSL https://raw.githubusercontent.com/SimpleAOB/SimplePanel/<branch>/scripts/test-on-server.sh | BRANCH=<branch> sh
#
# Paste the report between the ==== lines back to whoever asked for it.

set -u
BRANCH="${BRANCH:-main}"
DIR=/root/sp-test
HOG_SECONDS="${HOG_SECONDS:-90}"

[ "$(id -u)" -eq 0 ] || { echo "run as root" >&2; exit 1; }
mkdir -p "$DIR"
LOG="$DIR/run.log"
: >"$LOG"
mb() { case "$1" in '' | infinity | max | *[!0-9]*) echo "${1:-unset}" ;; *) echo "$(($1 / 1048576)) MB" ;; esac; }
say() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
results=""
check() { # check NAME OK(0/1) DETAIL
	if [ "$2" = 0 ]; then st=PASS; else st=FAIL; fi
	results="$results
$st  $1${3:+ ($3)}"
}

say "Preparing (git, swap for the build)"
command -v git >/dev/null 2>&1 || { apt-get update -qq >>"$LOG" 2>&1 && apt-get install -y -qq git >>"$LOG" 2>&1; }
if [ -z "$(swapon --noheadings 2>/dev/null)" ] && [ ! -e /swapfile ]; then
	fallocate -l 2G /swapfile && chmod 600 /swapfile && mkswap /swapfile >>"$LOG" && swapon /swapfile &&
		echo '/swapfile none swap sw 0 0' >>/etc/fstab
fi
if ! command -v docker >/dev/null 2>&1; then
	say "Installing Docker"
	curl -fsSL https://get.docker.com | sh >>"$LOG" 2>&1
fi

say "Fetching branch $BRANCH"
rm -rf "$DIR/src"
git clone -q --depth 1 -b "$BRANCH" https://github.com/SimpleAOB/SimplePanel "$DIR/src" >>"$LOG" 2>&1 || { echo "clone failed; see $LOG" >&2; exit 1; }
commit=$(git -C "$DIR/src" rev-parse --short HEAD)

say "Building the SimplePanel image (several minutes on a small server)"
start=$(date +%s)
if docker build -q -t simplepanel:test --build-arg VERSION="test-$commit" "$DIR/src" >>"$LOG" 2>&1; then
	check "image builds on this server" 0 "$(($(date +%s) - start))s"
else
	check "image builds on this server" 1 "see $LOG"
	printf '\n==== SimplePanel server test (%s) ====%s\n==== end ====\n' "$commit" "$results"
	exit 1
fi

say "Installing SimplePanel"
if SP_IMAGE=simplepanel:test sh "$DIR/src/install.sh" >"$DIR/install.log" 2>&1; then
	check "installer finishes" 0
else
	check "installer finishes" 1 "$(tail -n 3 "$DIR/install.log" | tr '\n' ' ')"
fi

# ---- What the installer set up -------------------------------------------------
mem_total=$(awk '/^MemTotal:/ {print int($2/1024)}' /proc/meminfo)
grep -q '"cgroup-parent": *"simplepanel.slice"' /etc/docker/daemon.json 2>/dev/null
check "daemon.json puts containers in simplepanel.slice" $?
cap=$(systemctl show simplepanel.slice -p MemoryMax --value 2>/dev/null)
swapcap=$(systemctl show simplepanel.slice -p MemorySwapMax --value 2>/dev/null)
[ -n "$cap" ] && [ "$cap" != infinity ]
check "slice has a memory cap" $? "MemoryMax=$(mb "$cap") of ${mem_total} MB, MemorySwapMax=$(mb "$swapcap")"
pid=$(docker inspect -f '{{.State.Pid}}' "$(docker ps -q -f label=com.docker.swarm.service.name=simplepanel | head -n 1)" 2>/dev/null)
grep -q simplepanel.slice "/proc/${pid:-0}/cgroup" 2>/dev/null
check "panel container runs inside the slice" $? "$(cat "/proc/${pid:-0}/cgroup" 2>/dev/null | head -n 1)"
capenv=$(docker service inspect simplepanel --format '{{range .Spec.TaskTemplate.ContainerSpec.Env}}{{println .}}{{end}}' 2>/dev/null | grep SP_MEMORY_CAP_MB)
[ -n "$capenv" ] && [ "$capenv" != SP_MEMORY_CAP_MB=0 ]
check "panel knows the cap" $? "$capenv"
swapon --noheadings | grep -q .
check "swap is on" $? "$(free -m | awk '/^Swap:/ {print $2 " MB"}')"
[ "$(sysctl -n vm.swappiness)" = 10 ]
check "kernel tuning applied" $? "swappiness=$(sysctl -n vm.swappiness), min_free_kbytes=$(sysctl -n vm.min_free_kbytes)"
curl -fsS -m 5 http://127.0.0.1:3000/api/health >/dev/null 2>&1
check "panel answers before the test" $?

# ---- Fill memory ----------------------------------------------------------------
say "Filling memory for ${HOG_SECONDS}s with a container that asks for 3x the server's RAM"
docker pull -q alpine:3 >>"$LOG" 2>&1
since=$(date '+%Y-%m-%d %H:%M:%S')
docker rm -f sp-hog >/dev/null 2>&1
docker run -d --name sp-hog alpine:3 sh -c \
	"apk add -q stress-ng >/dev/null && stress-ng --vm 2 --vm-bytes $((mem_total * 3 / 2))M --vm-keep --timeout ${HOG_SECONDS}s" >>"$LOG" 2>&1
worst_swarm=0 worst_panel=0 worst_ssh=0 fail_swarm=0 fail_panel=0 fail_ssh=0 min_avail=$mem_total n=0
end=$(($(date +%s) + HOG_SECONDS + 15))
while [ "$(date +%s)" -lt "$end" ]; do
	n=$((n + 1))
	t0=$(date +%s%N)
	timeout 20 docker service ls >/dev/null 2>&1 || fail_swarm=$((fail_swarm + 1))
	t1=$(date +%s%N)
	timeout 20 curl -fsS -m 20 http://127.0.0.1:3000/api/health >/dev/null 2>&1 || fail_panel=$((fail_panel + 1))
	t2=$(date +%s%N)
	timeout 20 bash -c 'exec 3<>/dev/tcp/127.0.0.1/22; head -c 4 <&3' 2>/dev/null | grep -q SSH || fail_ssh=$((fail_ssh + 1))
	t3=$(date +%s%N)
	ms() { echo $((($2 - $1) / 1000000)); }
	a=$(ms "$t0" "$t1") b=$(ms "$t1" "$t2") c=$(ms "$t2" "$t3")
	[ "$a" -gt "$worst_swarm" ] && worst_swarm=$a
	[ "$b" -gt "$worst_panel" ] && worst_panel=$b
	[ "$c" -gt "$worst_ssh" ] && worst_ssh=$c
	avail=$(awk '/^MemAvailable:/ {print int($2/1024)}' /proc/meminfo)
	[ "$avail" -lt "$min_avail" ] && min_avail=$avail
	sleep 3
done
docker rm -f sp-hog >/dev/null 2>&1

[ "$fail_swarm" = 0 ] && [ "$worst_swarm" -lt 10000 ]
check "Swarm kept answering" $? "slowest ${worst_swarm} ms, ${fail_swarm}/${n} failed"
[ "$fail_panel" = 0 ] && [ "$worst_panel" -lt 10000 ]
check "panel kept answering" $? "slowest ${worst_panel} ms, ${fail_panel}/${n} failed"
[ "$fail_ssh" = 0 ] && [ "$worst_ssh" -lt 10000 ]
check "SSH kept answering" $? "slowest ${worst_ssh} ms, ${fail_ssh}/${n} failed"
oom=$(journalctl -k --since "$since" --no-pager 2>/dev/null | grep -i "killed process" || true)
victims=$(printf '%s\n' "$oom" | sed -n 's/.*Killed process [0-9]* (\([^)]*\)).*/\1/p' | sort | uniq -c | tr '\n' ' ')
printf '%s\n' "$oom" | grep -q stress-ng
check "the kernel stopped the memory hog" $? "killed: ${victims:-nothing}"
others=$(printf '%s\n' "$oom" | grep -v stress-ng | grep -c . || true)
[ "$others" = 0 ]
check "nothing else was killed" $?
journalctl -k --since "$since" --no-pager 2>/dev/null | grep -i "oom" | grep -q "simplepanel.slice"
check "out-of-memory stayed inside the slice" $?
sleep 5
curl -fsS -m 5 http://127.0.0.1:3000/api/health >/dev/null 2>&1
check "panel answers after the test" $?
timeout 20 docker service ls >/dev/null 2>&1
check "Swarm answers after the test" $?

printf '\n==== SimplePanel server test (branch %s, %s) ====\n' "$BRANCH" "$commit"
printf 'Server: %s MB RAM, %s, kernel %s, %s\n' "$mem_total" "$(. /etc/os-release; echo "$PRETTY_NAME")" "$(uname -r)" "$(docker version -f 'Docker {{.Server.Version}}' 2>/dev/null)"
printf 'Lowest MemAvailable during the test: %s MB\n' "$min_avail"
printf '%s\n' "$results" | sed '1d'
printf '==== end ====\n'
printf '\nLogs: %s, %s/install.log\n' "$LOG" "$DIR"
grep -i "setup code" -A1 "$DIR/install.log" 2>/dev/null | head -n 2
