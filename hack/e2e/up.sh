#!/usr/bin/env bash
# Brings up kind + MinIO + the chart for test/e2e. Idempotent.
set -euo pipefail

CLUSTER="${CLUSTER:-csi-rclone-e2e}"
DRIVER_NS="${DRIVER_NS:-csi-rclone}"
IMAGE="csi-driver-rclone:e2e"
here="$(cd "$(dirname "$0")" && pwd)"
root="$here/../.."

if ! kind get clusters | grep -qx "$CLUSTER"; then
  kind create cluster --name "$CLUSTER" --config "$here/kind.yaml" --wait 120s
fi
kubectl config use-context "kind-$CLUSTER" >/dev/null

docker build -t "$IMAGE" --build-arg VERSION=e2e "$root"
# Second tag for the upgrade e2e (test/e2e/upgrade_test.go): another version, same code.
docker build -t "${IMAGE}-v2" --build-arg VERSION=e2e-v2 "$root"
kind load docker-image --name "$CLUSTER" "$IMAGE" "${IMAGE}-v2"

kubectl apply -f "$here/minio.yaml"

kubectl create namespace "$DRIVER_NS" --dry-run=client -o yaml | kubectl apply -f -
kubectl label namespace "$DRIVER_NS" pod-security.kubernetes.io/enforce=privileged --overwrite
# Rolls the DaemonSet and controller even when only the locally rebuilt image changed.
helm upgrade --install csi-rclone "$root/charts/csi-driver-rclone" -n "$DRIVER_NS" \
  --set image.repository=csi-driver-rclone --set image.tag=e2e --set image.pullPolicy=Never \
  --set logLevel=debug --set mountPod.linger=20s --set-string "node.podAnnotations.e2e/build=$(date +%s)" \
  --set-string "controller.podAnnotations.e2e/build=$(date +%s)" \
  --wait --timeout 3m

kubectl -n minio rollout status deploy/minio --timeout 3m
kubectl -n minio wait --for=condition=complete job/make-bucket --timeout 3m
