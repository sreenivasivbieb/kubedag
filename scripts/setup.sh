#!/usr/bin/env bash
#
# One-command local stack: kind cluster, Redis, runner pod pool, and the
# orchestrator running on the host.
#
# Design decision worth defending: the orchestrator runs on the HOST, not as a
# pod. The Phase 7 crash-recovery tests `kill -9` the orchestrator mid-run. If
# it lived in a pod, every one of those tests would need an image rebuild, and
# `go`/`bun` reload would be gone. The moment you hide the binary in a
# container, failure injection becomes slow enough that you stop doing it.
set -euo pipefail

CLUSTER="${KIND_CLUSTER:-kubedag}"
NAMESPACE="${KUBE_NAMESPACE:-kubedag-runner}"
RUNNER_IMAGE="${RUNNER_IMAGE:-kubedag-runner:v0}"
RUNNER_REPLICAS="${RUNNER_REPLICAS:-7}"

INFRA_ONLY=0
for arg in "$@"; do
  case "$arg" in
    --infra-only) INFRA_ONLY=1 ;;
    -h|--help) sed -n '2,16p' "$0"; exit 0 ;;
    *) echo "unknown flag: $arg" >&2; exit 2 ;;
  esac
done

log()  { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
die()  { printf '\033[1;31merror\033[0m %s\n' "$*" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || die "$1 not found in PATH"; }

need docker
need kind
need kubectl
need bun

log "docker    $(docker --version | awk '{print $3}' | tr -d ,)"
log "kind      $(kind version | awk '{print $2}')"
log "bun       $(bun --version)"

# --- cluster -----------------------------------------------------------------
if kind get clusters 2>/dev/null | grep -qx "$CLUSTER"; then
  log "kind cluster '$CLUSTER' exists, reusing"
else
  log "creating kind cluster '$CLUSTER'"
  kind create cluster --name "$CLUSTER" --wait 120s
fi
kubectl config use-context "kind-${CLUSTER}" >/dev/null

# --- runner image ------------------------------------------------------------
# Loading into kind's containerd avoids a registry push. The cost: the image
# must be REBUILT (not re-tagged) for changes to land, because containerd
# caches by digest.
log "building runner image $RUNNER_IMAGE"
docker build -q -t "$RUNNER_IMAGE" -f k8s/runner/Dockerfile k8s/runner >/dev/null
kind load docker-image "$RUNNER_IMAGE" --name "$CLUSTER" >/dev/null

# --- manifests ---------------------------------------------------------------
log "applying namespace and rbac"
kubectl apply -f k8s/namespace.yaml >/dev/null

log "deploying runner pool ($RUNNER_REPLICAS replicas)"
kubectl apply -f k8s/runner-pod.yaml >/dev/null
kubectl -n "$NAMESPACE" scale statefulset/runner --replicas="$RUNNER_REPLICAS" >/dev/null

log "waiting for runner pods"
kubectl -n "$NAMESPACE" rollout status statefulset/runner --timeout=300s

# --- redis -------------------------------------------------------------------
# Redis runs as a plain container rather than an in-cluster pod: it holds no
# state worth protecting locally, and a host port keeps the app runnable on the
# host without a port-forward that dies with this script.
if docker ps -a --format '{{.Names}}' | grep -qx "kubedag-redis"; then
  log "starting existing redis container"
  docker start kubedag-redis >/dev/null
else
  log "creating redis container"
  docker run -d --name kubedag-redis -p 6379:6379 redis:7-alpine >/dev/null
fi

# Readiness: the app connects on boot and ioredis retries, but a clear error
# here beats a confusing connection stack trace later.
for i in $(seq 1 30); do
  if docker exec kubedag-redis redis-cli ping 2>/dev/null | grep -q PONG; then
    log "redis ready"
    break
  fi
  [ "$i" = "30" ] && die "redis did not become ready in 30s"
  sleep 1
done

if [ "$INFRA_ONLY" = "1" ]; then
  log "infra-only: skipping the orchestrator"
else
  log "starting orchestrator (logs: .dev/orchestrator.log)"
  mkdir -p .dev
  KUBE_NAMESPACE="$NAMESPACE" bun --watch src/index.ts >.dev/orchestrator.log 2>&1 &
  echo $! > .dev/orchestrator.pid
  sleep 2
fi

READY=$(kubectl -n "$NAMESPACE" get pods -l app=runner --no-headers 2>/dev/null | grep -c Running || true)

cat <<EOF

$(printf '\033[1;32mstack is up\033[0m')

  cluster    $CLUSTER
  namespace  $NAMESPACE
  runners    $READY running
  redis      127.0.0.1:6379
  api        http://127.0.0.1:3000

  check      curl -s localhost:3000/healthz
             curl -s localhost:3000/pods
  pods       kubectl -n $NAMESPACE get pods
  logs       tail -f .dev/orchestrator.log

  submit     curl -X POST localhost:3000/workflow -H 'Content-Type: application/json' \\
               -d '{"workflowId":"wf-1","steps":[{"id":"A","command":"echo hello"}]}'

  teardown   ./scripts/teardown.sh

EOF