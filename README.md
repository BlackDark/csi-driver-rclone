<div align="center">

# csi-driver-rclone

**Kubernetes CSI driver for rclone: mount S3, WebDAV, SFTP and other rclone remotes as volumes.**

[![CI](https://github.com/BlackDark/csi-driver-rclone/actions/workflows/ci.yml/badge.svg)](https://github.com/BlackDark/csi-driver-rclone/actions/workflows/ci.yml)

</div>

## Status

Pre-alpha: Static Volumes work, with pods on one node sharing one Mount Pod that lingers `mountPod.linger` (default 2m) after the last pod leaves and drains pending uploads on stop. Mounts survive node plugin restarts and `helm upgrade`. A crashed Mount Pod is recreated, and the pods that used it get a `MountLost` Event and must be restarted. Dynamic and inline volumes do not yet. No image or chart is published yet, so build both from source. What v1 covers: [docs/scope.md](docs/scope.md).

## Quickstart

The node plugin is privileged, so its namespace needs the `privileged` Pod Security level. Mount Pods run in the same namespace.

```bash
docker build -t csi-driver-rclone:dev .   # then push it, or kind load it
kubectl create namespace csi-rclone
kubectl label namespace csi-rclone pod-security.kubernetes.io/enforce=privileged
helm install csi-rclone charts/csi-driver-rclone -n csi-rclone \
  --set image.repository=csi-driver-rclone --set image.tag=dev
```

A Static Volume: credentials go in a Credential Secret, everything else in `volumeAttributes` ([keys](docs/reference/volume-parameters.md)).

```yaml
apiVersion: v1
kind: Secret
metadata: {name: s3-creds, namespace: default}
stringData:
  configData: |
    [s3]
    type = s3
    provider = Minio
    access_key_id = ACCESS_KEY
    secret_access_key = SECRET_KEY
    endpoint = http://minio.example:9000
---
apiVersion: v1
kind: PersistentVolume
metadata: {name: bucket}
spec:
  capacity: {storage: 1Gi}
  accessModes: [ReadWriteMany]
  storageClassName: ""
  claimRef: {namespace: default, name: bucket}
  csi:
    driver: rclone.csi.blackdark.github.io
    volumeHandle: bucket
    volumeAttributes:
      remote: s3
      remotePath: my-bucket
      vfs-cache-mode: writes
    nodePublishSecretRef: {name: s3-creds, namespace: default}
---
apiVersion: v1
kind: PersistentVolumeClaim
metadata: {name: bucket, namespace: default}
spec:
  accessModes: [ReadWriteMany]
  storageClassName: ""
  volumeName: bucket
  resources: {requests: {storage: 1Gi}}
```

## Development

Requires [mise](https://mise.jdx.dev) and Docker.

```bash
git clone https://github.com/BlackDark/csi-driver-rclone && cd csi-driver-rclone
mise install
mise run check      # format, vet, lint, unit tests, helm lint
mise run e2e        # kind + MinIO end to end; cluster csi-rclone-e2e
mise run e2e:down
```
