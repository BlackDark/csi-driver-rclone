#!/usr/bin/env bash
# Brings up kind + MinIO + the chart for test/e2e. Idempotent.
set -euo pipefail

CLUSTER="${CLUSTER:-csi-rclone-e2e}"
DRIVER_NS="${DRIVER_NS:-csi-rclone}"
IMAGE="csi-driver-rclone:e2e"
here="$(cd "$(dirname "$0")" && pwd)"
root="$here/../.."
chart="$root/charts/csi-driver-rclone"

if ! kind get clusters | grep -qx "$CLUSTER"; then
  kind create cluster --name "$CLUSTER" --config "$here/kind.yaml" --wait 120s
fi
kubectl config use-context "kind-$CLUSTER" >/dev/null

# MinIO and the chart's sidecar images pull inside kind while the driver builds.
kubectl apply -f "$here/minio.yaml"
pulls=()
for img in $(helm template t "$chart" | sed -n 's/^ *image: *"\{0,1\}\([^"]*\)"\{0,1\}$/\1/p' | sort -u); do
  case "$img" in csi-driver-rclone:* | */csi-driver-rclone:*) continue ;; esac
  docker exec "$CLUSTER-control-plane" crictl pull "$img" >/dev/null &
  pulls+=($!)
done

# Host build reuses the local Go build cache; the image only copies the binary in.
arch="$(docker version -f '{{.Server.Arch}}')"
CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -C "$root" -trimpath \
  -ldflags "-s -w -X main.version=e2e" -o bin/csi-driver-rclone ./cmd/csi-driver-rclone
docker build -t "$IMAGE" --build-arg BIN=local --build-arg VERSION=e2e "$root"
# Second tag for the upgrade e2e (test/e2e/upgrade_test.go): it compares image refs only.
docker tag "$IMAGE" "${IMAGE}-v2"
kind load docker-image --name "$CLUSTER" "$IMAGE" "${IMAGE}-v2"

for p in "${pulls[@]}"; do wait "$p"; done

kubectl create namespace "$DRIVER_NS" --dry-run=client -o yaml | kubectl apply -f -
kubectl label namespace "$DRIVER_NS" pod-security.kubernetes.io/enforce=privileged --overwrite
# Rolls the DaemonSet and controller even when only the locally rebuilt image changed.
helm upgrade --install csi-rclone "$chart" -n "$DRIVER_NS" \
  --set image.repository=csi-driver-rclone --set image.tag=e2e --set image.pullPolicy=Never \
  --set logLevel=debug --set mountPod.linger=20s --set mountPod.healWorkloads.enabled=true --set-string "node.podAnnotations.e2e/build=$(date +%s)" \
  --set-string "controller.podAnnotations.e2e/build=$(date +%s)" \
  --wait --timeout 3m

kubectl -n minio rollout status deploy/minio --timeout 3m
kubectl -n minio wait --for=condition=complete job/make-bucket --timeout 3m
