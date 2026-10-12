#!/usr/bin/env bash
# Rebuild the runner image and reload it into kind.
#
# A plain `docker tag` is not enough: kind's containerd caches by digest, so the
# StatefulSet keeps running the old image until its pods are recreated. Hence
# the delete at the end.
set -euo pipefail
CLUSTER="${KIND_CLUSTER:-kubedag}"
NAMESPACE="${KUBE_NAMESPACE:-kubedag-runner}"
IMAGE="${RUNNER_IMAGE:-kubedag-runner:v0}"

docker build -q -t "$IMAGE" -f k8s/runner/Dockerfile k8s/runner >/dev/null
kind load docker-image "$IMAGE" --name "$CLUSTER" >/dev/null
kubectl -n "$NAMESPACE" delete pod -l app=runner --ignore-not-found >/dev/null
echo "rebuilt $IMAGE"
