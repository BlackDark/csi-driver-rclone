# Scope

What the driver does, what it does not do, and why. Terms are defined in [GLOSSARY.md](../GLOSSARY.md).

## Possible

- Static, Dynamic and Inline Volumes. Dynamic Volumes get one templated subpath per PVC (`${pvc.namespace}`, `${pvc.name}`, `${pv.name}`).
- Several containers and pods on one node share one Mount Pod and one Cache per volume (for example Sonarr, Radarr, Plex and Jellyfin on `media`). Each pod can use its own `subPath`.
- The same volume can be used on several nodes (ReadWriteMany). Each node gets its own Mount Pod and Cache.
- Read-only per pod. The bind mount is read-only even when the Mount Pod is read-write.
- Any rclone backend, including nested remotes (crypt over OneDrive, union) through `configData`. Encrypted configs work through `configPassword`.
- Mounts survive node plugin restarts and driver upgrades: deleting the plugin pod, a DaemonSet rollout or `helm upgrade`. IO in workload pods carries on. After the restart, the plugin adopts the running Mount Pods and rebuilds their users from the mount table.
- A crashed or deleted Mount Pod does not survive for the pods using it. Their mount returns `ENOTCONN` until the pod restarts (ADR 0003). The driver detects the crash, reports the volume `Inaccessible` in `pod.status.volumeHealth` (`CSIVolumeHealth` feature gate), emits `MountLost` Events, and recreates the Mount Pod, so restarted and new pods mount again. This also holds after a plugin restart: the adopted Mount Pod's spec and workload pods are read back from its Secret and annotation.
- Node reboot: mounts are gone with the node. kubelet republishes, and new Mount Pods start.
- The Cache survives Mount Pod replacement. It lives on hostPath by default, one dir per volume, or on any volume source set in `cacheVolume`. A changed Mount Spec waits until the Mount Pod of the old spec is released, so two rclone processes never share a Cache.
- Inline Volumes are per pod: each gets its own Mount Pod and Cache, never shared, and the Cache is removed with the Mount Pod.
- The upload queue drains before a Mount Pod stops, bounded by a timeout. A timed-out drain emits a `DrainTimeout` Event on the Mount Pod and the last workload pod.
- Prometheus metrics: CSI operations, per-volume VFS and transfer stats, and mount health (also reported through CSI `NodeGetVolumeHealth`, the successor of `VOLUME_CONDITION`). ServiceMonitor, PodMonitor and Grafana dashboard are optional.

## Security posture

- The node plugin is the only privileged component, because it needs Bidirectional propagation for bind mounts. It does not use hostNetwork.
- The controller runs unprivileged and non-root, with a read-only root filesystem, no hostPath and no Secret access. The Mount Pod is unprivileged where possible: it depends on the fd-passing spike. Images are distroless and non-root.
- RBAC: pods and Secrets only in the driver namespace (pods also `patch`, for the users annotation). No cluster-wide Secret access. Events cluster-wide (create, patch), so drain timeouts show on the workload pod. The controller has only what external-provisioner needs: PVs, PVCs, StorageClasses, Events, and leases in the driver namespace.
- Metrics endpoints (node plugin, controller, Mount Pods) are unauthenticated plain HTTP on the pod network, with no rc server and no rc credentials. Labels carry volume IDs, Mount Pod ids and images, never credentials, remote paths or Mount Options ([metrics](reference/metrics.md)).
- Mount Pods run at `system-node-critical` priority, so the apps using them are evicted first. They tolerate what the node plugin tolerates. Resources are set per StorageClass or PV over chart defaults: a 64Mi request and no memory limit ([operations](operations.md)).

## Not possible, by design

| Not supported | Reason | Alternative |
|---|---|---|
| Credentials in PV `volumeAttributes` or StorageClass parameters | Anyone who can read PVs could read them. | Credential Secret through `nodePublishSecretRef` |
| Arbitrary rclone flags by default | A PV author could inject options. | Allowlist. Admins can opt into Unrestricted Options. Never for Inline Volumes, which accept a smaller subset and no `cacheVolume` or Mount Pod resources. Keys: [volume parameters](reference/volume-parameters.md). |
| Backend flags (`--onedrive-*`, `--s3-*`) as Mount Options | There are hundreds of them, and they belong to the Remote. | Put them in `configData` (`chunk_size = 5M`, `delta = true`). |
| Per-pod uid, gid or `fsGroup` on a shared mount | One FUSE mount has one ownership. | Set `uid`, `gid` and `umask` per volume. See [Ownership](#ownership). |
| Consistency across nodes | Each node has its own Cache. Writes held by `vfs-write-back` are invisible to other nodes until uploaded and polled. | Write from one node, or use a short `vfs-write-back` and `poll-interval`. |
| Deleting remote data on PVC delete | It is risky and rarely wanted. | Planned for later. The design keeps it possible. |
| rc API, `vfs/refresh` trigger | No credentialed control endpoint. | Rely on `poll-interval`. A driver-level refresh trigger may come later. |
| Capacity, quotas, snapshots, cloning, expansion | Remotes have no block semantics. | None. |
| mergerfs or composing with other drivers | Out of scope. | rclone `union` remote. Composition will be designed in `csi-driver-mergerfs`. |
| Windows nodes, Kubernetes < 1.29 | Not targeted. | None. |
| Restarting a live Mount Pod on driver upgrade | It would break running pods. A FUSE connection cannot be handed to a new rclone (ADR 0003). | It upgrades on node reboot, drain, or reuse after the Linger expires. |

## Ownership

Kubernetes has these ways to control file ownership on a shared FUSE mount:

| Option | Status | Notes |
|---|---|---|
| Fixed `uid`, `gid` and `umask` per volume | Chosen | This is how rclone mounts are commonly run. Pods set a matching `runAsUser` or `supplementalGroups`. |
| `fsGroup` through CSIDriver `fsGroupPolicy: File` | Rejected | Kubelet would chown recursively across the remote. That is slow, and API-costly on cloud storage. |
| `VOLUME_MOUNT_GROUP` (the driver receives the pod's `fsGroup`) | Rejected for shared mounts | The first pod would set the group for every later pod. It only makes sense with one mount per pod. |
| Idmapped bind mounts per pod (`mount_setattr`, Linux 5.12+) | Possible later | Gives per-pod uid and gid mapping over one FUSE mount. It needs FUSE idmap support (Linux 6.12+) and is not yet tested with rclone. |
| `fsGroupPolicy: None` | Chosen | Kubelet never touches ownership. |
