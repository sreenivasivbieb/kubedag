#!/usr/bin/env bash
# Tear down the local stack. Idempotent.
set -euo pipefail

CLUSTER="${KIND_CLUSTER:-kubedag}"

if [ -f .dev/orchestrator.pid ]; then
  pid=$(cat .dev/orchestrator.pid)
  if kill -0 "$pid" 2>/dev/null; then kill "$pid" 2>/dev/null || true; fi
  rm -f .dev/orchestrator.pid
fi

if docker ps -a --format '{{.Names}}' | grep -qx "kubedag-redis"; then
  docker rm -f kubedag-redis >/dev/null
fi

if kind get clusters 2>/dev/null | grep -qx "$CLUSTER"; then
  kind delete cluster --name "$CLUSTER" >/dev/null
fi

echo "torn down"
