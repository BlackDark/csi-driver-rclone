<div align="center">

# csi-driver-rclone

**Kubernetes CSI driver for rclone: mount S3, WebDAV, SFTP and other rclone remotes as volumes.**

[![CI](https://github.com/BlackDark/csi-driver-rclone/actions/workflows/ci.yml/badge.svg)](https://github.com/BlackDark/csi-driver-rclone/actions/workflows/ci.yml)

</div>

## Status

Pre-alpha: Static Volumes work, with pods on one node sharing one Mount Pod that lingers `mountPod.linger` (default 2m) after the last pod leaves and drains pending uploads on stop. Mounts survive node plugin restarts and `helm upgrade`. A crashed Mount Pod is recreated, and the pods that used it get a `MountLost` Event and must be restarted. Dynamic Volumes work: a StorageClass gives each PVC its own templated subpath, and deleting the PVC keeps the data. Inline Volumes work, with a Credential Secret in the pod's namespace and a restricted option subset. The hostPath Cache survives Mount Pod replacement; `cacheVolume` puts it on any volume source. Every component serves Prometheus metrics, with an optional ServiceMonitor, PodMonitor and Grafana dashboard ([series](docs/reference/metrics.md)). No image or chart is published yet, so build both from source. What v1 covers: [docs/scope.md](docs/scope.md).

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

A Dynamic Volume per PVC from a StorageClass. The controller fills in `${pvc.namespace}`, `${pvc.name}` and `${pv.name}`. Deleting a PVC removes only the PV; the data stays on the remote. The Credential Secret must not set `remotePath`. The chart can render this StorageClass (`storageClass.enabled`).

```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata: {name: rclone-s3}
provisioner: rclone.csi.blackdark.github.io
reclaimPolicy: Delete
parameters:
  remote: s3
  remotePath: my-bucket/${pvc.namespace}/${pvc.name}
  vfs-cache-mode: writes
  csi.storage.k8s.io/node-publish-secret-name: s3-creds
  csi.storage.k8s.io/node-publish-secret-namespace: ${pvc.namespace}
---
apiVersion: v1
kind: PersistentVolumeClaim
metadata: {name: data, namespace: default}
spec:
  accessModes: [ReadWriteMany]
  storageClassName: rclone-s3
  resources: {requests: {storage: 1Gi}}
```

An Inline Volume in a pod spec, with the Credential Secret in the pod's namespace. Only a subset of Mount Options is accepted, and no `allowAllOptions`, `cacheVolume` or Mount Pod resources ([keys](docs/reference/volume-parameters.md#inline-volumes)). Each pod gets its own Mount Pod.

```yaml
apiVersion: v1
kind: Pod
metadata: {name: app, namespace: default}
spec:
  containers:
    - name: app
      image: busybox
      command: [sleep, infinity]
      volumeMounts: [{name: data, mountPath: /data}]
  volumes:
    - name: data
      csi:
        driver: rclone.csi.blackdark.github.io
        volumeAttributes: {remote: s3, remotePath: my-bucket, vfs-cache-mode: writes}
        nodePublishSecretRef: {name: s3-creds}
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
