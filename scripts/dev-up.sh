#!/usr/bin/env bash
#
# One-command local stack: kind cluster, Redis, Postgres, runner pod pool,
# orchestrator and pod manager running on the host.
#
# Design decision: infra (Redis, Postgres, kind) runs in-cluster or in
# containers, but the two Go binaries run on the HOST rather than as pods.
# Rationale: you can attach a debugger, `kill -9` a process to test crash
# recovery without rebuilding an image, and `go run` gives you instant reload.
# The moment we hide the binaries in a pod, the crash-injection tests for
# N1/Phase 7 become slow and painful. This is a deliberate developer
# experience trade-off, not an oversight.
set -euo pipefail

CLUSTER="${KIND_CLUSTER:-kubedag}"
NAMESPACE="${NAMESPACE:-kubedag}"
RUNNER_IMAGE="${RUNNER_IMAGE:-kubedag-runner:v0}"
REDIS_IMAGE="${REDIS_IMAGE:-redis:7-alpine}"
POSTGRES_IMAGE="${POSTGRES_IMAGE:-postgres:16-alpine}"
INFRA_ONLY=0

for arg in "$@"; do
  case "$arg" in
    --infra-only) INFRA_ONLY=1 ;;
    -h|--help) sed -n '2,20p' "$0"; exit 0 ;;
    *) echo "unknown flag: $arg" >&2; exit 2 ;;
  esac
done

log()  { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m warn\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31merror\033[0m %s\n' "$*" >&2; exit 1; }

need() { command -v "$1" >/dev/null 2>&1 || die "$1 not found in PATH"; }

# --- preflight ---------------------------------------------------------------
need go
need docker
need kind
need kubectl

log "go        $(go version | awk '{print $3}')"
log "docker    $(docker --version | awk '{print $3}' | tr -d ,)"
log "kind      $(kind version | awk '{print $2}')"
log "kubectl   $(kubectl version --client -o json 2>/dev/null | grep -o '"gitVersion": *"[^"]*"' | cut -d'"' -f4 || echo unknown)"

# --- cluster -----------------------------------------------------------------
if kind get clusters 2>/dev/null | grep -qx "$CLUSTER"; then
  log "kind cluster '$CLUSTER' already exists, reusing"
else
  log "creating kind cluster '$CLUSTER'"
  kind create cluster --name "$CLUSTER" --config deploy/kind/kind-config.yaml --wait 120s
fi

kubectl config use-context "kind-${CLUSTER}" >/dev/null

if ! kubectl get namespace "$NAMESPACE" >/dev/null 2>&1; then
  log "creating namespace '$NAMESPACE'"
  kubectl create namespace "$NAMESPACE"
fi

# Labelling the namespace lets NetworkPolicies and the PSP-style hardening in
# Phase 9 target this stack without hardcoding names.
kubectl label namespace "$NAMESPACE" kubedag.dev/stack=local --overwrite >/dev/null

# --- redis -------------------------------------------------------------------
# Deployed as a single-node StatefulSet rather than a plain Deployment so that
# PVC-backed persistence is possible later without changing the manifest shape.
log "deploying redis"
kubectl -n "$NAMESPACE" apply -f deploy/infra/redis.yaml

# --- postgres ----------------------------------------------------------------
log "deploying postgres"
kubectl -n "$NAMESPACE" apply -f deploy/infra/postgres.yaml

# --- runner image ------------------------------------------------------------
# Loading into kind's containerd means pods start without a registry push. The
# tradeoff: it only works on the local kind node, and the image must be rebuilt
# (not just re-tagged) for changes to appear.
log "building and loading runner image"
docker build -f deploy/runner/Dockerfile -t "$RUNNER_IMAGE" deploy/runner >/dev/null
kind load docker-image "$RUNNER_IMAGE" --name "$CLUSTER" >/dev/null

# --- runner pool -------------------------------------------------------------
# A StatefulSet gives pods stable names (runner-0, runner-1, ...), which is what
# makes lease-by-pod-name viable across orchestrator restarts. A Deployment
# would give us random suffixes and break lease identity.
log "deploying runner pool"
kubectl -n "$NAMESPACE" delete statefulset "${RUNNER_IMAGE%%:*}-pool" --ignore-not-found >/dev/null 2>&1 || true
kubectl -n "$NAMESPACE" apply -f deploy/runner/runner-pool.yaml

# --- wait for readiness ------------------------------------------------------
log "waiting for redis and postgres"
kubectl -n "$NAMESPACE" rollout status statefulset/redis --timeout=180s
kubectl -n "$NAMESPACE" rollout status statefulset/postgres --timeout=180s

log "waiting for runner pods"
kubectl -n "$NAMESPACE" rollout status statefulset/runner-pool --timeout=300s

# --- port-forwards -----------------------------------------------------------
# Exposing infra via port-forward keeps the manifests free of NodePort/LoadBalancer,
# which kind cannot support anyway. Downside: forwards die with the script, so
# long-running sessions need `make dev-up` re-run or the logs below.
log "establishing port-forwards (127.0.0.1 -> cluster)"
pkill -f "kubectl port-forward -n $NAMESPACE" 2>/dev/null || true

kubectl -n "$NAMESPACE" port-forward svc/redis    6379:6379  >/tmp/kubedag-redis-pf.log    2>&1 &
kubectl -n "$NAMESPACE" port-forward svc/postgres 5432:5432  >/tmp/kubedag-postgres-pf.log 2>&1 &
kubectl -n "$NAMESPACE" port-forward svc/orchestrator 8080:8080 >/tmp/kubedag-api-pf.log     2>&1 &

sleep 3

if [ "$INFRA_ONLY" = "1" ]; then
  log "infra-only mode: skipping app processes"
else
  log "starting orchestrator and podmanager (PIDs written to .dev/)"
  mkdir -p .dev
  KUBEDAG_API_ADDR=":8080" KUBEDAG_NAMESPACE="$NAMESPACE" \
    go run ./cmd/orchestrator >.dev/orchestrator.log 2>&1 &
  echo $! > .dev/orchestrator.pid

  KUBEDAG_NAMESPACE="$NAMESPACE" \
    go run ./cmd/podmanager >.dev/podmanager.log 2>&1 &
  echo $! > .dev/podmanager.pid
fi

# --- summary -----------------------------------------------------------------
cat <<EOF

$(printf '\033[1;32mstack is up\033[0m')

  cluster     $CLUSTER
  namespace   $NAMESPACE
  redis       127.0.0.1:6379
  postgres    127.0.0.1:5432 (kubedag/kubedag)
  api         http://127.0.0.1:8080

  check       curl -s localhost:8080/healthz
              curl -s localhost:8080/readyz
  pods        kubectl -n $NAMESPACE get pods
  logs        tail -f .dev/orchestrator.log

  teardown    make dev-down

EOF