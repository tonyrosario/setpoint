#!/usr/bin/env bash
#
# setpoint demo — the full lifecycle against real Docker:
#   apply → Ready → drift repair → update-by-recreate → backoff → delete (M0)
#   → emergent ordering: out-of-order apply, dependency-kill self-repair (M1).
#
# Safe to re-run: it force-removes any leftover owned containers and networks
# on entry and exit, and stops the daemon it started. Requires a running
# Docker daemon and Go (via mise). Nothing here needs root.
#
# Usage: scripts/demo.sh
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

BIN="$REPO_ROOT/bin"
SERVER="http://127.0.0.1:8080"
export SETPOINT_SERVER="$SERVER"
OWNER_LABEL="setpoint.io/owner=setpoint"
CONTAINER="setpoint-web"
LOG="$(mktemp -t setpointd.XXXXXX.log)"
DAEMON_PID=""
TMPDIR_DEMO="$(mktemp -d -t setpoint-demo.XXXXXX)"

# --- pretty output ---------------------------------------------------------
bold() { printf '\n\033[1m== %s ==\033[0m\n' "$*"; }
info() { printf '   %s\n' "$*"; }

cleanup() {
  [[ -n "$DAEMON_PID" ]] && kill "$DAEMON_PID" 2>/dev/null || true
  docker ps -aq --filter "label=$OWNER_LABEL" | xargs -r docker rm -f >/dev/null 2>&1 || true
  docker network ls -q --filter "label=$OWNER_LABEL" | xargs -r docker network rm >/dev/null 2>&1 || true
  rm -rf "$TMPDIR_DEMO"
}
trap cleanup EXIT

# Poll `cpctl get` until the resource reports Ready, or fail after a timeout.
wait_ready() {
  local kind="$1" name="$2" timeout="${3:-30}" i
  for ((i = 0; i < timeout * 2; i++)); do
    if "$BIN/cpctl" get "$kind" "$name" 2>/dev/null | awk 'NR>1 {print $3}' | grep -qx true; then
      return 0
    fi
    sleep 0.5
  done
  echo "timed out waiting for $kind/$name to become Ready" >&2
  "$BIN/cpctl" get "$kind" "$name" >&2 || true
  return 1
}

# Poll until the resource's status message contains the given substring.
wait_message() {
  local kind="$1" name="$2" want="$3" timeout="${4:-30}" i
  for ((i = 0; i < timeout * 2; i++)); do
    if "$BIN/cpctl" get "$kind" "$name" 2>/dev/null | grep -q "$want"; then
      return 0
    fi
    sleep 0.5
  done
  echo "timed out waiting for $kind/$name message to contain: $want" >&2
  "$BIN/cpctl" get "$kind" "$name" >&2 || true
  return 1
}

# The current owned network's full Docker ID (empty if none).
owned_network_id() {
  docker network ls -q --no-trunc --filter "label=$OWNER_LABEL" \
    --filter "label=setpoint.io/resource-name=backend"
}

# The network ID the owned web container is attached to (empty if absent).
attached_network_id() {
  local cid
  cid="$(docker ps -q --filter "label=$OWNER_LABEL" --filter "label=setpoint.io/resource-name=web")"
  [[ -n "$cid" ]] || return 0
  docker inspect "$cid" --format '{{range .NetworkSettings.Networks}}{{.NetworkID}}{{end}}'
}

# Poll until exactly the given image is the running owned container.
wait_image() {
  local want="$1" timeout="${2:-30}" i
  for ((i = 0; i < timeout * 2; i++)); do
    if docker ps --filter "label=$OWNER_LABEL" --format '{{.Image}}' | grep -qx "$want"; then
      return 0
    fi
    sleep 0.5
  done
  echo "timed out waiting for image $want" >&2
  return 1
}

# Poll until the resource no longer appears in `cpctl get`.
wait_gone() {
  local name="$1" timeout="${2:-30}" i
  for ((i = 0; i < timeout * 2; i++)); do
    if ! "$BIN/cpctl" get containers 2>/dev/null | awk 'NR>1 {print $2}' | grep -qx "$name"; then
      return 0
    fi
    sleep 0.5
  done
  return 1
}

# --- preflight -------------------------------------------------------------
bold "Preflight"
command -v go >/dev/null || { echo "go not found (install via mise: mise use -g go@1.26)"; exit 1; }
docker info >/dev/null 2>&1 || { echo "Docker daemon not reachable — start Docker and retry"; exit 1; }
info "go $(go version | awk '{print $3}'), docker $(docker info --format '{{.ServerVersion}}')"

bold "Build setpointd + cpctl"
( cd cmd && go build -o "$BIN/setpointd" ./setpointd )
( cd cli && go build -o "$BIN/cpctl" ./cpctl )
info "built into ./bin"

# Clear any leftovers from a previous run before we start.
docker ps -aq --filter "label=$OWNER_LABEL" | xargs -r docker rm -f >/dev/null 2>&1 || true
docker network ls -q --filter "label=$OWNER_LABEL" | xargs -r docker network rm >/dev/null 2>&1 || true

bold "Start the control plane"
# Short sweep interval so out-of-band drift is caught quickly in the demo.
"$BIN/setpointd" --sweep-interval 3s >"$LOG" 2>&1 &
DAEMON_PID=$!
sleep 1
info "setpointd running (pid $DAEMON_PID), logs: $LOG"

# --- 1. apply --------------------------------------------------------------
bold "1. Apply a Container (declarative, async)"
info "\$ cpctl apply -f examples/container.yaml"
"$BIN/cpctl" apply -f examples/container.yaml
info "The API returned 202 immediately; convergence happens in the background."
wait_ready container web
"$BIN/cpctl" get containers
info "Real container running:"
docker ps --filter "label=$OWNER_LABEL" --format '   {{.Names}}  {{.Image}}  {{.Status}}'

# --- 2. drift repair -------------------------------------------------------
bold "2. Drift repair — kill the container out-of-band, watch it heal"
info "\$ docker kill $CONTAINER   (simulating something killing it behind our back)"
docker kill "$CONTAINER" >/dev/null
info "The reconciler observes the container is gone and recreates it..."
wait_ready container web
docker ps --filter "label=$OWNER_LABEL" --format '   {{.Names}}  {{.Image}}  {{.Status}}'
info "Self-healed — desired state restored without us touching the API."

# --- 3. update-by-recreate -------------------------------------------------
bold "3. Update — change the image, watch it converge"
before="$(docker ps -q --filter "label=$OWNER_LABEL")"
info "\$ cpctl apply -f examples/container-updated.yaml   (nginx:alpine -> httpd:alpine)"
"$BIN/cpctl" apply -f examples/container-updated.yaml
wait_image httpd:alpine   # wait for the recreate to actually land
wait_ready container web
after="$(docker ps -q --filter "label=$OWNER_LABEL")"
docker ps --filter "label=$OWNER_LABEL" --format '   {{.Names}}  {{.Image}}  {{.Status}}'
if [[ "$before" != "$after" ]]; then
  info "Container recreated (new id) — the spec-hash changed, so it was replaced."
else
  info "Same container id (unexpected for an image change)."
fi

# --- 4. backoff ------------------------------------------------------------
bold "4. Backoff — a bad image fails and retries with growing delay"
cat >"$TMPDIR_DEMO/bad.yaml" <<'YAML'
kind: container
name: web
spec:
  image: nginx:this-tag-does-not-exist-000
YAML
info "\$ cpctl apply -f <bad image>"
"$BIN/cpctl" apply -f "$TMPDIR_DEMO/bad.yaml"
sleep 5
info "Status is Error, and the reconciler is backing off (not spinning):"
"$BIN/cpctl" get containers
attempts="$(grep -cE 'msg=(creating|updating)' "$LOG" 2>/dev/null || echo '?')"
info "reconcile attempts logged so far: $attempts (exponential backoff, not thousands)"
info "\$ cpctl apply -f examples/container.yaml   (fix it — converges promptly)"
"$BIN/cpctl" apply -f examples/container.yaml
wait_ready container web
info "Recovered — fixing the spec resets the backoff and it converges."

# --- 5. delete -------------------------------------------------------------
bold "5. Delete — declarative teardown"
info "\$ cpctl delete container web"
"$BIN/cpctl" delete container web
wait_gone web   # reconciler removes the container, then the resource itself
info "Container removed from Docker:"
docker ps -a --filter "label=$OWNER_LABEL" --format '   {{.Names}}' | grep . || info "   (none — clean)"
info "Resource removed from the control plane:"
"$BIN/cpctl" get containers

# --- 6. emergent ordering: out-of-order apply -------------------------------
bold "6. Emergent ordering — apply the dependent BEFORE its dependency"
info "examples/container-networked.yaml wires a network through a Reference:"
info "   references: { net: { kind: network, name: backend, field: networkId } }"
info "   spec:       { image: nginx:alpine, network: \$(ref:net) }"
info ""
info "\$ cpctl apply -f examples/container-networked.yaml   (network/backend does NOT exist yet)"
"$BIN/cpctl" apply -f examples/container-networked.yaml
wait_message container web "waiting for network/backend"
"$BIN/cpctl" get containers
info "Not an error — the container is simply not Ready, and says why. A DAG"
info "executor (CloudFormation) would have rejected this order up front;"
info "setpoint just retries until the Reference resolves (ADR-0005)."
info ""
info "\$ cpctl apply -f examples/network.yaml   (now supply the dependency)"
"$BIN/cpctl" apply -f examples/network.yaml
wait_ready network backend
wait_ready container web
"$BIN/cpctl" get networks
"$BIN/cpctl" get containers
net_id="$(owned_network_id)"
if [[ "$(attached_network_id)" == "$net_id" ]]; then
  info "docker inspect confirms: the container is attached to the referenced"
  info "network — ordering emerged from readiness, no manual retry."
else
  info "WARNING: container is not attached to the expected network."
fi

# --- 7. emergent ordering: dependency-kill self-repair ----------------------
bold "7. Kill the dependency — watch the dependent heal onto its replacement"
old_net="$(owned_network_id)"
info "\$ docker network disconnect + rm   (destroying the network behind our back)"
docker network disconnect --force "$old_net" "$CONTAINER" >/dev/null 2>&1 || true
docker network rm "$old_net" >/dev/null
info "The reconciler recreates the network (a NEW Docker ID). The container's"
info "spec-hash covers the resolved reference value, so it no longer matches —"
info "the same drift machinery recreates the container onto the new network..."
for ((i = 0; i < 60; i++)); do
  new_net="$(owned_network_id)"
  [[ -n "$new_net" && "$new_net" != "$old_net" && "$(attached_network_id)" == "$new_net" ]] && break
  sleep 0.5
done
wait_ready container web
"$BIN/cpctl" get containers
if [[ "$(attached_network_id)" == "$(owned_network_id)" && -n "$(owned_network_id)" ]]; then
  info "Healed: replacement network $(docker network ls --filter "label=$OWNER_LABEL" --format '{{.ID}}'), container reattached."
else
  info "WARNING: dependent did not reattach to the replacement network."
fi

# --- 8. ordered teardown ----------------------------------------------------
bold "8. Teardown — deletion respects the dependency too"
info "\$ cpctl delete network backend   (while the container is still attached)"
"$BIN/cpctl" delete network backend
wait_message network backend "containers attached"
"$BIN/cpctl" get networks
info "Docker refuses to remove a network with attached containers; setpoint"
info "surfaces that as a pending deletion and keeps retrying — not a failure."
info ""
info "\$ cpctl delete container web   (remove the dependent; the network follows)"
"$BIN/cpctl" delete container web
wait_gone web
for ((i = 0; i < 60; i++)); do
  [[ -z "$(owned_network_id)" ]] && break
  sleep 0.5
done
info "Everything owned is gone from Docker and from the control plane:"
"$BIN/cpctl" get containers
"$BIN/cpctl" get networks

bold "Done"
info "M0: create, self-heal, update, back off, delete — level-triggered through"
info "one reconciler. M1: cross-resource References resolve through Status, so"
info "ordering emerges from readiness instead of a DAG — out-of-order apply"
info "converges, and killing a dependency cascades a repair. See README.md."
