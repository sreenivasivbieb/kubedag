#!/usr/bin/env bash
# Rebuild the runner image and load it into kind. Needed whenever
# deploy/runner changes; a plain `docker tag` is not enough because kind
# caches by digest.
set -euo pipefail
CLUSTER="${KIND_CLUSTER:-kubedag}"
IMAGE="${RUNNER_IMAGE:-kubedag-runner:v0}"

docker build -f deploy/runner/Dockerfile -t "$IMAGE" deploy/runner
kind load docker-image "$IMAGE" --name "$CLUSTER"

# Remove pods so the new image is actually pulled; imagePullPolicy is IfNotPresent.
kubectl -n "${NAMESPACE:-kubedag}" delete pod -l app.kubernetes.io/name=runner --ignore-not-found
echo "rebuilt $IMAGE"
