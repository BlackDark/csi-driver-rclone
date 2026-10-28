<div align="center">

# csi-driver-rclone

**Kubernetes CSI driver for rclone: mount S3, OneDrive, WebDAV, SFTP and any other rclone remote as a volume.**

[![CI](https://github.com/BlackDark/csi-driver-rclone/actions/workflows/ci.yml/badge.svg)](https://github.com/BlackDark/csi-driver-rclone/actions/workflows/ci.yml)

</div>

Status: pre-alpha. Linux nodes, Kubernetes ≥1.34 (the bundled external-provisioner v6 needs the `storage/v1` VolumeAttributesClass API).

## Why

It replaces a host systemd `rclone mount` plus a hostPath PV with a volume Kubernetes manages.

- One Mount Pod per volume and node. Every pod on that node using the volume shares one rclone process and one Cache, each with its own `subPath` and read-only flag ([ADR 0001](docs/adr/0001-shared-mount-pod-per-volume-and-node.md)).
- Mounts survive node plugin restarts and `helm upgrade`; workload IO carries on. A live Mount Pod keeps its image; new Mount Pods use the new one ([operations](docs/operations.md#upgrades)).
- Credentials come only from a Credential Secret. Mount Options are allowlisted. Mount Pods run unprivileged and non-root, and only the node plugin is privileged ([ADR 0002](docs/adr/0002-mount-pod-privilege.md)). There is no rc server.

## What works, what does not

Full list with reasons: [docs/scope.md](docs/scope.md).

Works:
- Static, Dynamic (one templated subpath per PVC) and Inline Volumes.
- Any rclone backend, nested remotes (crypt over OneDrive, union) and encrypted configs (`configData` + `configPassword`).
- ReadWriteMany across nodes, with one Mount Pod and Cache per node.
- Cache on hostPath (default, survives Mount Pod replacement) or any volume source (`cacheVolume`).
- OAuth backends with single-use or rotating refresh tokens (Box, OneDrive): refreshed tokens are written back to a driver-owned Token Secret and one refresher at a time holds a Lease, so the next Mount Pod and other volumes on the same token keep working ([ADR 0004](docs/adr/0004-oauth-token-write-back.md)).
- Upload drain on stop, bounded by a timeout. A crashed Mount Pod is recreated, and the pods that used it get a `MountLost` Event and must be restarted ([ADR 0003](docs/adr/0003-mount-pod-crash-recovery.md)), or are evicted for their controller to recreate with `mountPod.healWorkloads.enabled` ([healing](docs/operations.md#workload-healing)).
- Prometheus metrics for every component, with optional ServiceMonitor, PodMonitor and Grafana dashboard ([metrics](docs/reference/metrics.md)).

Does not work, by design:
- Credentials in PV or StorageClass attributes.
- Backend flags (`--s3-*`, `--onedrive-*`) as Mount Options: put them in `configData`.
- Per-pod uid, gid or `fsGroup` on a shared mount: set `uid`, `gid`, `umask` per volume.
- rc API and `vfs/refresh`: use `poll-interval`.
- Consistency across nodes, capacity, quotas, snapshots, expansion, deleting remote data on PVC delete.
- mergerfs: use an rclone `union` remote.
- Windows nodes, Kubernetes < 1.34.

## Install

The node plugin is privileged, so its namespace needs the `privileged` Pod Security level. Mount Pods run in the same namespace. Label the namespace before installing; pods created before the label are rejected.

```bash
kubectl create namespace csi-rclone
kubectl label namespace csi-rclone pod-security.kubernetes.io/enforce=privileged
helm install csi-rclone oci://ghcr.io/blackdark/csi-driver-rclone/charts/csi-driver-rclone --version <version> -n csi-rclone
```

The image defaults to `ghcr.io/blackdark/csi-driver-rclone:<appVersion>`. Chart values: [values.yaml](charts/csi-driver-rclone/values.yaml). Verify signatures with:

```bash
cosign verify ghcr.io/blackdark/csi-driver-rclone:<version> \
  --certificate-identity-regexp '^https://github.com/BlackDark/csi-driver-rclone/' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

From source: `docker build -t csi-driver-rclone:dev .`, load the image into the cluster, and install `charts/csi-driver-rclone` with `--set image.repository=csi-driver-rclone --set image.tag=dev`.

## Quickstart

Credentials go in a Credential Secret; everything else in `volumeAttributes` or StorageClass parameters ([keys](docs/reference/volume-parameters.md)).

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
```

### Static Volume

```yaml
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

### Dynamic Volume

Each PVC gets its own subpath. `${pvc.namespace}`, `${pvc.name}` and `${pv.name}` are filled in. Deleting the PVC removes only the PV; the data stays. The Credential Secret must not set `remotePath`. The chart can render the StorageClass (`storageClass.enabled`).

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

### Inline Volume

The Credential Secret lives in the pod's namespace. Each pod gets its own Mount Pod. Only a subset of Mount Options is accepted, and `allowAllOptions`, `cacheVolume` and Mount Pod resources are rejected ([keys](docs/reference/volume-parameters.md#inline-volumes)).

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

Moving from a host systemd `rclone mount`: [migration guide](docs/guides/migrate-from-systemd.md).

## Operate

- [Operations](docs/operations.md): Mount Pod memory sizing, priority, OAuth tokens, upgrades.
- [Metrics](docs/reference/metrics.md): series, ports, monitors, dashboard.
- [Docs map](docs/README.md).

## Limitations

- A crashed or OOM-killed Mount Pod ends the mount for the pods using it; they must be restarted, or opt into workload healing. There is no default memory limit for that reason.
- Writes held by `vfs-write-back` are only on the node's Cache until uploaded; other nodes see them after upload and `poll-interval`.
- One FUSE mount has one ownership: all pods on a volume see the same `uid`, `gid` and `umask`.
- A changed Mount Spec takes effect once the old Mount Pod on that node is released.
- The metrics endpoints are unauthenticated plain HTTP inside the cluster.
- OAuth tokens refreshed by rclone go to a Token Secret in the driver namespace, not back into your Credential Secret; re-authorize by changing the token in the Credential Secret ([operations](docs/operations.md#oauth-tokens-and-git-rollouts)). Token Secrets are not garbage collected.

## Development

Requires [mise](https://mise.jdx.dev) and Docker.

```bash
git clone https://github.com/BlackDark/csi-driver-rclone && cd csi-driver-rclone
mise install
mise run check      # format, vet, lint, unit tests, csi-sanity, helm lint
mise run test:fuse  # FUSE handoff in a Linux container (needs /dev/fuse)
mise run test:fuse:host  # same on a Linux host with passwordless sudo (CI)
mise run e2e        # kind + MinIO end to end; cluster csi-rclone-e2e
mise run e2e:down
```

CI runs e2e only weekly on `main`, and on manual runs: Actions → CI → Run workflow, pick the branch (a PR branch works; the result shows on the PR). Every other job runs on each PR and push.

Release: merge the release PR. On every push to `main`, release-please (`.github/workflows/release-please.yml`) keeps a release PR open that bumps the version from Conventional Commits (pre-1.0: `feat` → minor, `fix` → patch, `!` → minor), updates `CHANGELOG.md` and sets `version` and `appVersion` in `charts/csi-driver-rclone/Chart.yaml`. Merging it tags `vX.Y.Z`, creates the GitHub release with the changelog, and calls `release.yml`: it fails unless the tag commit is on `main` with a CI run there that passed `check` (`hack/release/check-main-ci.sh`, waits while that run is in progress) and the tag equals the chart version (`hack/release/check-version.sh`), runs the full CI including e2e, then publishes image tags `X.Y.Z`, `X.Y`, `latest` and the chart. Never push release tags by hand. A manual run of `release.yml` on a branch publishes image tag `edge` only.

One-time setup: PRs opened with `GITHUB_TOKEN` trigger no workflows, so the release PR gets no `check`. Store a fine-grained PAT (this repo; Contents and Pull requests read/write) or a GitHub App token as repository secret `RELEASE_PLEASE_TOKEN`. Without it, push an empty commit to the release PR branch or close and reopen the PR to run CI.

## License

Apache License 2.0, see [LICENSE](LICENSE).
