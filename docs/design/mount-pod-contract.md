# Mount Pod contract

How the node plugin and a Mount Pod work together. Background: [ADR 0001](../adr/0001-shared-mount-pod-per-volume-and-node.md), [ADR 0002](../adr/0002-mount-pod-privilege.md).

## Names and paths

- `<id>`: the first 16 hex characters of the Mount Spec Identity hashed with the node name. It is stable for one (volume, node, spec).
- Mount Pod `rclone-mount-<id>` and Secret `rclone-mount-<id>`, both in the driver namespace. The Secret has an ownerReference to the Pod.
- Labels: `app.kubernetes.io/name=csi-driver-rclone`, `app.kubernetes.io/component=mount`, `rclone.csi.blackdark.github.io/node=<node>`, `rclone.csi.blackdark.github.io/id=<id>`. `rclone.csi.blackdark.github.io/volume=<volume hash>` (see Cache) on the Pod and Secret, so the plugin finds every Mount Pod of a volume on its node. Annotations `rclone.csi.blackdark.github.io/volume-id=<volume id>` and, on the Pod, `rclone.csi.blackdark.github.io/users`: JSON `{"<target path>":{"ns":…,"name":…,"uid":…}}` of the workload pods using it (from `podInfoOnMount`), rewritten on every publish and unpublish that changes it. RBAC: `pods` `patch` in the driver namespace.
- Per-mount directory on the host: `<kubeletDir>/plugins/rclone.csi.blackdark.github.io/mounts/<id>/`
  - `fuse/`: the FUSE mountpoint. Only the node plugin mounts it, and only the node plugin sees it (Bidirectional).
  - `handoff.sock`: a unix socket served by the node plugin.
- The Mount Pod mounts the per-mount directory, without `fuse/`, through hostPath `<...>/mounts/<id>`, using `mountPropagation: None`.
- The node plugin's CSI socket is `<kubeletDir>/plugins/rclone.csi.blackdark.github.io/csi.sock`. The plugin mounts that dir and `<kubeletDir>/pods` at their host paths with Bidirectional propagation. It bind-mounts with mount(2) and umount(2) directly, not mount(8), so the image stays distroless.
- Cache: hostPath `<cacheRoot>/<volume hash>` (first 16 hex of sha256 of the volume ID, type `DirectoryOrCreate`), or `cacheVolume` decoded strictly as a `VolumeSource`. It is mounted at `/cache`. The hostPath dir is keyed by volume ID only, not by `<id>`, so it outlives a spec change; it is node-local, so in effect per (volume, node). Released Inline Volumes delete their hostPath dir (their volume ID is per pod), unless the drain timed out. The node plugin creates the hostPath dir and the per-mount directory owned by the Mount Pod uid/gid, mode `0750`, so it needs `cacheRoot` mounted too.
- Pod: `restartPolicy: Never` (a restarted container would need a new fd, so the plugin recreates the pod), `automountServiceAccountToken: false`, `enableServiceLinks: false`, `fsGroup` = Mount Pod gid (Secret files are `0440`), an `emptyDir` at `/tmp` beside the read-only root filesystem, env `HOME=/tmp` and `XDG_CACHE_HOME=/cache` (rclone derives backend defaults such as the `cache` backend's db path from them at start, before `--cache-dir` applies), `terminationGracePeriodSeconds` = drain timeout + unmount wait + 10s.
- The Secret is mounted at `/etc/rclone-mount/`. It holds only `spec.json` (serialized Mount Spec). Its `configData` is plaintext: the node plugin decrypts an encrypted config during Mount Spec resolution, so the Mount Pod needs no password. A `config-password` key left by earlier builds is ignored.
- In the Mount Pod the per-mount directory is mounted at `/run/rclone-mount/`, so the socket is `/run/rclone-mount/handoff.sock`. All paths are `mount` mode flags (`--spec`, `--socket`, `--cache-dir`) with these defaults.
- The Mount Pod serves `/metrics` and `/healthz` on `--metrics-addr` (default `:9810`, container port `metrics`); `/healthz` backs its readiness probe and answers 200 only while the mount is served. Its `VOLUME_ID` env comes from the `volume-id` annotation through the downward API and labels the series ([metrics](../reference/metrics.md)). There is no liveness probe: with RestartPolicy Never a dead rclone ends the pod and the plugin replaces it.

## spec.json

`mountspec.File`, written by `Spec.Serialize` and read by `mountspec.Load`: `{"version":2,"configData":…,"fsPath":…,"options":{…},"identity":…,"unrestricted":…,"resources":{…},"cacheVolume":…,"ephemeral":…,"tokens":[{"section":…,"isolated":…,"seedHash":…}]}`. It holds every Mount Spec field except the config password (not needed after decryption) and the per-publish read-only flag; the last five are omitted when empty. `tokens` lists the Token Write-back sections (ADR 0004); `configData` there already carries the token from the Token Secret. Version 1 files (without them) still load. The Mount Pod uses the remote and options; the node plugin reads the whole file back when it adopts a mount. `options` keeps Mount Spec keys (`vfs-cache-mode`); the Mount Pod maps them onto rclone's mount, VFS and global options and rejects unknown ones. rclone config stays in memory, never on disk.

## Handoff protocol

Package `internal/handoff`. One ASCII message per line on the unix socket; the socket is mode `0660`, owned by the Mount Pod uid/gid.

| Client sends | Server mode | Server replies |
|---|---|---|
| `hello` | fresh (just mounted) | `fd` with the FUSE fd as `SCM_RIGHTS`; the server switches to adopt |
| `adopt` | adopt (mount exists) | `ok` |
| `hello` | adopt | `reject mount-exists`, close |
| `adopt` | fresh | `reject no-mount`, close |

After the handshake the client sends `ready`, `drained` or `drain-timeout`. For Token Write-back (ADR 0004) it also sends `token-subscribe`, `token-lock <s>`, `token-unlock <s>` and `token-save <s> <t>`; the server answers `token-grant <s> <t>` or `token-deny <s>`, answers every `token-save` with `token-saved <s> <sha256 of t>` once the Token Secret holds it (or `token-save-failed <s>`; the Mount Pod keeps the token and resends), and pushes `token <s> <t>` after a subscribe. `<s>` and `<t>` are base64 of the section name in the user's `configData` and of the token JSON; lines after the handshake may be up to 64KiB. Lock waits run beside status messages, never blocking them. A closed connection releases its locks. EOF on either side ends the connection. After a lost connection the Mount Pod redials every second, sends `adopt`, and repeats its drain result if it already had one.

## Sequence

1. `NodePublishVolume`: resolve the Mount Spec (mode Ephemeral when the volume context has `csi.storage.k8s.io/ephemeral=true`, else Persistent) and take the per-`<id>` lock. A target that already is a healthy mount returns OK. An invalid spec fails with `InvalidArgument`, naming the key; kubelet shows that message in its `FailedMount` Event on the workload pod, so the driver adds no Event of its own.
   - Cache exclusivity: before starting a new `<id>`, under a per-volume lock, the plugin refuses with `Unavailable` when another `<id>` of the same volume is known in memory or has a Mount Pod (any phase, label `…/volume`) on this node. Two rclone processes on one Cache dir would corrupt it. A changed spec therefore takes effect once the old `<id>` is released: its pods are gone and its Linger expired. kubelet retries the publish meanwhile.
2. The node plugin creates `fuse/`, opens `/dev/fuse`, runs mount(2) per ADR 0002 (fstype `fuse.rclone`, `allow_other`, uid/gid = Mount Pod uid/gid), and serves the fd on `handoff.sock`. It then creates the Pod and, once the Pod UID is known, the Secret with its ownerReference; both are idempotent by name. The Secret volume is not optional, so kubelet holds the Pod until the Secret exists. A Pod still terminating fails the publish with `Unavailable`; kubelet retries. Any failure up to step 4 deletes Pod and Secret and unmounts `fuse/`.
3. The Mount Pod (`mount` mode) connects, receives the fd, and runs rclone mount2 in-process on `/dev/fd/N`. Once the VFS is serving, it sends `ready` on the same connection, then keeps the connection open. That open connection is its liveness signal to the plugin.
4. The node plugin waits for `ready` (bounded by the publish timeout), then bind-mounts `fuse/<subpath?>` onto the target path, and remounts read-only if requested. A spec with Unrestricted Options emits a Warning Event, reason `UnrestrictedOptions`, on the workload pod for each new target.
5. `NodeUnpublishVolume`: unmount the target. If no other targets on this node use `<id>`, start the Linger (`--linger`, chart `mountPod.linger`, default 2m; plugin-wide, not part of the Mount Spec Identity) and return. A publish of `<id>` within the Linger cancels it and reuses the Mount Pod, including the read-only remount for that target. A Linger of 0 deletes the Pod within the RPC. Linger timers live in plugin memory; after a restart, adoption starts a fresh Linger for every adopted mount without users.
6. Linger expiry, under the `<id>` lock and only if `<id>` still has no users: delete the Pod; the rest runs in the background under the lock. The Mount Pod receives SIGTERM, drains the upload queue (bounded by `--drain-timeout`, default 5m), and sends `drained` or `drain-timeout` on the connection. The plugin then unmounts `fuse/` lazily, and rclone exits 0 on "unmounted externally". Without an unmount within `--unmount-wait` (default 30s) it exits 0 anyway. The plugin waits for the drain result or connection EOF, bounded by drain timeout + unmount wait + 10s. The Secret is garbage-collected; the plugin also deletes it as a fallback. The Pod's `terminationGracePeriodSeconds` covers this whole wait, so kubelet never SIGKILLs a draining Mount Pod.
7. Drain timeout (`drain-timeout`, or no drain result within the bound): the plugin logs a warning and emits a Warning Event, reason `DrainTimeout`, on the Mount Pod and on the last workload pod that published `<id>` (from `podInfoOnMount`: `csi.storage.k8s.io/pod.name`/`pod.namespace`). The workload pod may already be gone; the Event is still listed by `kubectl get events --field-selector involvedObject.name=<pod>`. RBAC: a ClusterRole grants `events` create/patch.

## Restart and failure

- Node plugin restart (pod deletion, DaemonSet rollout, `helm upgrade`): running mounts are kept; their state comes from the cluster and the node. Before serving CSI calls, the plugin lists its Mount Pods on this node (label `rclone.csi.blackdark.github.io/node`) and reads `/proc/self/mountinfo`. A mount on `mounts/<id>/fuse` belongs to `<id>`. Its users are the CSI targets `<kubelet>/pods/<uid>/volumes/kubernetes.io~csi/<name>/mount` with the same `major:minor`: a bind shares the device of the fuse superblock. kubelet's own `volume-subpaths` binds share it too but are not users; kubelet removes them with the pod.
  - Adoption never compares the Mount Pod image with `--mount-image`, nor its resources with `--mount-resources`: both are node-wide, outside the Mount Spec Identity. An adopted Mount Pod keeps its image until it is released; the next Mount Pod for `<id>` gets the new one ([upgrades](../operations.md#upgrades)).
  - Adopted: `fuse/` is mounted and answers `statfs`, and the Mount Pod is alive (not terminating, not Failed or Succeeded). The plugin serves `handoff.sock` in adopt mode, and the Mount Pod reconnects and sends `adopt`. Users are rebuilt from the binds, their workload pod refs from the `users` annotation, and the Mount Spec from `spec.json` in the owned Secret. So a later crash is recreated at once and reported to the workload pods. A mount without users starts a fresh Linger. A missing or unreadable Secret leaves the spec unknown; then a crash is recreated by the next publish.
  - Cleaned up: anything else, such as a dead `fuse/`, a Pod without a mount, or a terminating or failed Pod. The plugin lazily unmounts `fuse/`, deletes the Pod and Secret, and removes the dirs. The binds of a dead mount are reported Inaccessible until kubelet unpublishes them, and their workload pods (from the `users` annotation, when the Pod still exists) get a `MountLost` Event. With `--heal-workloads` they are evicted too; the spec is unknown here, so their replacement's publish recreates the Mount Pod.
- A publish that finds no live mount for `<id>` lazily unmounts any leftover mount on `fuse/` (one whose Mount Pod is gone) before mounting afresh.
- Mount Pod lost: the handoff connection closes without `drained`, or the Pod watch sees the Mount Pod Failed, Succeeded or deleted while the plugin did not stop it. The plugin:
  1. marks every target of `<id>` dead,
  2. emits a Warning Event, reason `MountLost`, on the Mount Pod and on each target's workload pod,
  3. deletes the Pod, waits for its drain result or EOF, unmounts `fuse/`, and deletes the Secret,
  4. if targets remain, waits until the Pod name is free and recreates the Mount Pod with a fresh fd, so new publishes work.

  5. with `--heal-workloads`, evicts each target's workload pod ([workload healing](#workload-healing)), after step 4 or once it gave up.

  The old binds point at the dead connection and return `ENOTCONN` until their pod restarts. A FUSE connection cannot be resumed by a new rclone (ADR 0003).
- Health: `NodeGetVolumeHealth` reports a dead target, or one whose `statfs` returns `ENOTCONN`, as `INACCESSIBLE` with reason `MountPodLost`. Kubelet writes that to `pod.status.volumeHealth` (feature gate `CSIVolumeHealth`). `NodeGetVolumeStats` returns statfs usage, or `Unavailable` for `ENOTCONN`. CSI 1.13 replaced `VOLUME_CONDITION` with `GET_VOLUME_HEALTH`.
- Publish is idempotent: a target that already is a healthy mount of a live `<id>` returns OK. A target that is a dead mount is unmounted and bound again. A Mount Pod that is not `ready` within `--publish-timeout` fails the publish with `DeadlineExceeded`, and the Pod, Secret, `fuse/` and handoff server are removed.
- The Mount Pod name stays the same as long as the spec has not changed. A changed spec gets a new `<id>`, and so a new Mount Pod, but only after the old `<id>` is released (Cache exclusivity, step 1).
- Inline Volumes: kubelet generates the volume ID per pod and volume (`csi-<hash>`), so every pod gets its own `<id>`, Mount Pod and Cache; Inline Volumes are never shared across pods.

## Workload healing

Opt-in with `--heal-workloads` (chart `mountPod.healWorkloads.enabled`). For each workload pod of a lost mount, in the background and outside the `<id>` lock:

1. get the pod; skip it when gone, terminating, or its UID differs from the one recorded at publish (already replaced),
2. no controller ownerReference (bare pod): Warning `HealBlocked`, not evicted,
3. else create a `policy/v1` Eviction with a UID precondition. A PodDisruptionBudget refusal (429) is retried with backoff from 1s, doubling to 30s, for up to 5 minutes, then Warning `HealBlocked`. NotFound or Conflict means already replaced. Success: Normal `WorkloadHealed`.

Each outcome counts in `csi_rclone_workload_heals_total{result=evicted|skipped|blocked}`.
