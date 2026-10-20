#!/usr/bin/env bash
# Runs the Home Assistant add-on the way the Supervisor does and fails if it
# doesn't start. v0.1.9 and v0.1.10 shipped an add-on that never started:
# the Supervisor mounts the add-on's config folder owned by root (0755), and
# the image ran watchglass as uid 1000, so its first write failed. The plain
# image's smoke test can't see that, because it runs with --user set to the
# runner's own uid.
#
# Usage: addon-supervisor-check.sh ADDON_DIR BASE_IMAGE
#
# BASE_IMAGE is the plain image built earlier in the same run. It is tagged
# as the published image at version $ADDON_CHECK_TAG, so the add-on is built
# (or, for an add-on with an image: key, run) on top of this run's image and
# never on one pulled from the registry. On exit it removes the containers,
# the folders and the image tags it created.
#
# Optional environment:
#   ADDON_CHECK_NAME  name of the add-on image and prefix of the containers
#                     (default watchglass-addon-ci)
#   ADDON_CHECK_PORT  host port, published on 127.0.0.1 only (default 18082)
#   ADDON_CHECK_TAG   version the add-on is built for (default ci)
set -euo pipefail

if [ $# -ne 2 ]; then
	echo "usage: $0 ADDON_DIR BASE_IMAGE" >&2
	exit 2
fi
addon_dir=$1
base=$2
name=${ADDON_CHECK_NAME:-watchglass-addon-ci}
port=${ADDON_CHECK_PORT:-18082}
tag=${ADDON_CHECK_TAG:-ci}
wait_secs=30

fail() {
	echo "::error::$*"
	exit 1
}

# The runner's user can use docker but needs sudo for root-owned folders
# (and to look inside them, whatever mode a regression leaves them with).
as_root() {
	if [ "$(id -u)" = 0 ]; then "$@"; else sudo "$@"; fi
}

containers=()
tags=()
work=""
# Shows a container's log (folded) and removes it.
remove() {
	echo "::group::log of $1"
	docker logs "$1" 2>&1 || true
	echo "::endgroup::"
	docker rm -f "$1" >/dev/null 2>&1 || true
}
cleanup() {
	local c
	for c in "${containers[@]}"; do
		if docker container inspect "$c" >/dev/null 2>&1; then
			remove "$c"
		fi
	done
	if [ -n "$work" ] && [ -d "$work" ]; then
		as_root rm -rf -- "$work"
	fi
	# Only the tags this run added; an image that had the tag before keeps
	# it (pointing at BASE_IMAGE now).
	for c in "${tags[@]}"; do
		docker image rm "$c" >/dev/null 2>&1 || true
	done
}
# Tags an image and remembers the tag for cleanup if it is new.
add_tag() {
	if ! docker image inspect "$2" >/dev/null 2>&1; then
		tags+=("$2")
	fi
	docker tag "$1" "$2"
}
trap cleanup EXIT

config="$addon_dir/config.yaml"
[ -f "$config" ] || fail "$config not found"

# The image the Supervisor would run. With an image: key it pulls
# <image>:<version> and runs it as it is; without one it builds the
# Dockerfile in the add-on folder, passing BUILD_VERSION.
image_key=$(sed -n 's/^image:[[:space:]]*["'\'']\{0,1\}\([^"'\''[:space:]]*\).*/\1/p' "$config")
if [ -n "$image_key" ]; then
	echo "config.yaml has image: $image_key, so the Supervisor would run that image unchanged"
	add_tag "$base" "$image_key:$tag"
	addon_image="$image_key:$tag"
else
	[ -f "$addon_dir/Dockerfile" ] || fail "$addon_dir has neither an image: key nor a Dockerfile"
	from=$(sed -n 's/^FROM[[:space:]]\{1,\}\([^:[:space:]]*\):\${BUILD_VERSION}.*/\1/p' "$addon_dir/Dockerfile")
	[ -n "$from" ] || fail "$addon_dir/Dockerfile no longer starts FROM <image>:\${BUILD_VERSION}; update this check to build it on this run's image"
	add_tag "$base" "$from:$tag"
	echo "building $addon_dir on $from:$tag (this run's $base)"
	if ! docker image inspect "$name:$tag" >/dev/null 2>&1; then
		tags+=("$name:$tag")
	fi
	docker build --pull=false --build-arg BUILD_VERSION="$tag" -t "$name:$tag" "$addon_dir" 2>&1
	addon_image="$name:$tag"
fi

# Built on this run's image, not on one from the registry: the add-on
# image's layers must start with every layer of BASE_IMAGE.
base_layers=$(docker image inspect -f '{{range .RootFS.Layers}}{{.}} {{end}}' "$base")
addon_layers=$(docker image inspect -f '{{range .RootFS.Layers}}{{.}} {{end}}' "$addon_image")
case "$addon_layers" in
"$base_layers"*) echo "$addon_image is built on $base ($(docker image inspect -f '{{.Id}}' "$base"))" ;;
*) fail "the add-on image $addon_image is not built on this run's $base; it came from somewhere else" ;;
esac

# The add-on's environment: from config.yaml, as the Supervisor sets it.
# Only a block map (one KEY: value per line) is read; anything else would
# quietly run the add-on without its environment.
if grep -Eq '^environment:[[:space:]]*[^[:space:]#]' "$config"; then
	fail "$config writes environment: on one line; this check reads it only as a block map (one KEY: value per line below it), so write it that way"
fi
env_args=()
while IFS= read -r kv; do
	[ -n "$kv" ] && env_args+=(-e "$kv")
done < <(awk '
	/^environment:/ { on = 1; next }
	on && /^[^[:space:]#]/ { on = 0 }
	on && /^[[:space:]]+[A-Za-z_][A-Za-z0-9_]*:/ {
		line = $0
		sub(/^[[:space:]]+/, "", line)
		sub(/[[:space:]]+#.*$/, "", line)
		k = line; sub(/:.*/, "", k)
		v = line; sub(/^[^:]*:[[:space:]]*/, "", v)
		gsub(/^["\047]|["\047]$/, "", v)
		print k "=" v
	}' "$config")
# The Supervisor starts add-ons with Docker's init unless config.yaml says
# init: false.
init_args=(--init)
if grep -Eq '^init:[[:space:]]*false' "$config"; then
	init_args=()
fi
echo "environment: ${env_args[*]:-(none)}; init: ${init_args[*]:-no}"

# Ownership the files must end up with: the image's watchglass user.
wg_uid=1000
wg_gid=$(docker run --rm --entrypoint id "$addon_image" -g watchglass)

work=$(mktemp -d)
echo "work folder: $work"

# A config folder as the Supervisor creates it: owned by root, mode 0755.
supervisor_folder() {
	as_root install -d -o 0 -g 0 -m 0755 "$1"
}

# Remembered before docker run: a container that is created but can't
# start (the port is taken, say) still has to be removed.
start() {
	containers+=("$1")
	docker run -d --name "$1" "${init_args[@]}" "${env_args[@]}" \
		-p "127.0.0.1:$port:8080" -v "$2:/config" "$addon_image" >/dev/null ||
		fail "could not start the add-on container $1 (docker's error is above)"
}

# Wait for HTTP 200 on /. Ingress mode serves direct requests too. Each
# request has its own time limit, so an add-on that takes the connection
# and never answers fails here too instead of hanging the job.
wait_http() {
	local c=$1 what=$2 code="" running deadline=$((SECONDS + wait_secs))
	while [ "$SECONDS" -lt "$deadline" ]; do
		code=$(curl -s --max-time 2 -o /dev/null -w '%{http_code}' "http://127.0.0.1:$port/" || true)
		[ "$code" = 200 ] && return 0
		running=$(docker inspect -f '{{.State.Running}}' "$c")
		[ "$running" = true ] || break
		sleep 1
	done
	local state last
	state=$(docker inspect -f '{{if .State.Running}}still running{{else}}exited with code {{.State.ExitCode}}{{end}}' "$c")
	last=$(docker logs "$c" 2>&1 | tail -n 1 | tr -d '\r')
	fail "$what: the add-on did not answer HTTP 200 on / within ${wait_secs}s (last status ${code:-none}); the container $state; its log ends: ${last:-(empty)}"
}

owner() {
	as_root stat -c '%u:%g' "$1"
}

check_owner() {
	local f=$1 what=$2 got
	as_root test -f "$f" || fail "$what: config.yaml was not created in the mounted /config"
	got=$(owner "$f")
	[ "$got" = "$wg_uid:$wg_gid" ] || fail "$what: config.yaml is owned by $got, want $wg_uid:$wg_gid (the watchglass user)"
}

# The watchglass process inside the container: uid 1000 and not root, no
# supplementary group but its own (root's group 0 would reach root-owned
# files), no new privileges, no effective capabilities.
check_process() {
	local c=$1 pids pid status uids gids groups g nnp capeff
	pids=$(docker exec "$c" sh -c 'for p in /proc/[0-9]*; do [ "$(cat "$p/comm" 2>/dev/null)" = watchglass ] && echo "${p#/proc/}"; done; true')
	[ -n "$pids" ] || fail "no watchglass process found in $c"
	[ "$(echo "$pids" | wc -l)" = 1 ] || fail "more than one watchglass process in $c: $pids"
	pid=$pids
	status=$(docker exec "$c" cat "/proc/$pid/status")
	uids=$(echo "$status" | awk '/^Uid:/ { print $2, $3, $4, $5 }')
	gids=$(echo "$status" | awk '/^Gid:/ { print $2, $3, $4, $5 }')
	groups=$(echo "$status" | awk '/^Groups:/ { $1 = ""; sub(/^ +/, ""); print }')
	nnp=$(echo "$status" | awk '/^NoNewPrivs:/ { print $2 }')
	capeff=$(echo "$status" | awk '/^CapEff:/ { print $2 }')
	echo "watchglass is pid $pid in $c: Uid $uids, Gid $gids, Groups ${groups:-(none)}, NoNewPrivs $nnp, CapEff $capeff"
	[ "$uids" = "$wg_uid $wg_uid $wg_uid $wg_uid" ] || fail "watchglass runs with uids $uids (real, effective, saved, fs), want $wg_uid for all"
	[ "$gids" = "$wg_gid $wg_gid $wg_gid $wg_gid" ] || fail "watchglass runs with gids $gids (real, effective, saved, fs), want $wg_gid for all"
	for g in $groups; do
		[ "$g" = "$wg_gid" ] || fail "watchglass keeps supplementary groups $groups, want none but $wg_gid"
	done
	[ "$nnp" = 1 ] || fail "watchglass runs with NoNewPrivs ${nnp:-unset}, want 1"
	[ "$capeff" = 0000000000000000 ] || fail "watchglass runs with effective capabilities $capeff, want none"
}

# 1. First start in an empty folder the Supervisor made.
c1="$name-first"
dir1="$work/first"
supervisor_folder "$dir1"
start "$c1" "$dir1"
wait_http "$c1" "first start in a root-owned 0755 /config"
echo "first start answers 200"
check_owner "$dir1/config.yaml" "first start"
echo "first start created config.yaml owned by $wg_uid:$wg_gid"
check_process "$c1"

# 2. A restart keeps config.yaml as it is.
sum=$(as_root sha256sum "$dir1/config.yaml" | cut -d ' ' -f 1)
docker restart "$c1" >/dev/null
wait_http "$c1" "after docker restart"
as_root test -f "$dir1/config.yaml" || fail "after docker restart: config.yaml is gone"
sum2=$(as_root sha256sum "$dir1/config.yaml" | cut -d ' ' -f 1)
[ "$sum2" = "$sum" ] || fail "after docker restart: config.yaml changed (sha256 $sum before, $sum2 after)"
check_owner "$dir1/config.yaml" "after docker restart"
echo "after docker restart: answers 200, config.yaml unchanged"
remove "$c1"

# 3. A config.yaml root wrote before start, as the File editor add-on
# would, is handed back to the watchglass user as it is (not replaced by a
# new one, which would lose the user's edits).
c2="$name-rootcfg"
dir2="$work/rootcfg"
supervisor_folder "$dir2"
{
	echo "# saved by root, as the File editor add-on does"
	as_root cat "$dir1/config.yaml"
} | as_root tee "$dir2/config.yaml" >/dev/null
as_root chmod 0644 "$dir2/config.yaml"
echo "config.yaml before start: $(owner "$dir2/config.yaml")"
sum=$(as_root sha256sum "$dir2/config.yaml" | cut -d ' ' -f 1)
start "$c2" "$dir2"
wait_http "$c2" "with a config.yaml root wrote"
check_owner "$dir2/config.yaml" "with a config.yaml root wrote"
sum2=$(as_root sha256sum "$dir2/config.yaml" | cut -d ' ' -f 1)
[ "$sum2" = "$sum" ] || fail "with a config.yaml root wrote: its content changed on start (sha256 $sum before, $sum2 after); the user's edits would be lost"
echo "a config.yaml root wrote is now owned by $wg_uid:$wg_gid, unchanged, and the add-on answers 200"
check_process "$c2"

echo "add-on check passed in ${SECONDS}s"
