#!/usr/bin/env bash
# Tear down the local stack. Idempotent: safe to run if nothing exists.
set -euo pipefail

CLUSTER="${KIND_CLUSTER:-kubedag}"
NAMESPACE="${NAMESPACE:-kubedag}"

if [ -d .dev ]; then
  log "stopping app processes"
  for f in .dev/*.pid; do
    [ -e "$f" ] || continue
    pid=$(cat "$f")
    if kill -0 "$pid" 2>/dev/null; then
      kill "$pid" 2>/dev/null || true
      echo "  stopped $(basename "$f" .pid) ($pid)"
    fi
    rm -f "$f"
  done
fi

pkill -f "kubectl port-forward -n $NAMESPACE" 2>/dev/null || true

if kind get clusters 2>/dev/null | grep -qx "$CLUSTER"; then
  log "deleting kind cluster '$CLUSTER'"
  kind delete cluster --name "$CLUSTER"
else
  log "kind cluster '$CLUSTER' not present, nothing to do"
fi

log "done"