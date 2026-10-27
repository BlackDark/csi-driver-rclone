# Mount Pod contract

How the node plugin and a Mount Pod work together. Background: [ADR 0001](../adr/0001-shared-mount-pod-per-volume-and-node.md), [ADR 0002](../adr/0002-mount-pod-privilege.md).

## Names and paths

- `<id>`: the first 16 hex characters of the Mount Spec Identity hashed with the node name. It is stable for one (volume, node, spec).
- Mount Pod `rclone-mount-<id>` and Secret `rclone-mount-<id>`, both in the driver namespace. The Secret has an ownerReference to the Pod.
- Labels: `app.kubernetes.io/name=csi-driver-rclone`, `app.kubernetes.io/component=mount`, `rclone.csi.blackdark.github.io/node=<node>`, `rclone.csi.blackdark.github.io/id=<id>`. Annotation `rclone.csi.blackdark.github.io/volume-id=<volume id>`.
- Per-mount directory on the host: `<kubeletDir>/plugins/rclone.csi.blackdark.github.io/mounts/<id>/`
  - `fuse/`: the FUSE mountpoint. Only the node plugin mounts it, and only the node plugin sees it (Bidirectional).
  - `handoff.sock`: a unix socket served by the node plugin.
- The Mount Pod mounts the per-mount directory, without `fuse/`, through hostPath `<...>/mounts/<id>`, using `mountPropagation: None`.
- The node plugin's CSI socket is `<kubeletDir>/plugins/rclone.csi.blackdark.github.io/csi.sock`. The plugin mounts that dir and `<kubeletDir>/pods` at their host paths with Bidirectional propagation. It bind-mounts with mount(2) and umount(2) directly, not mount(8), so the image stays distroless.
- Cache: hostPath `<cacheRoot>/<volume hash>` (first 16 hex of sha256 of the volume ID, type `DirectoryOrCreate`), or `cacheVolume` decoded strictly as a `VolumeSource`. It is mounted at `/cache`. The node plugin creates the hostPath dir and the per-mount directory owned by the Mount Pod uid/gid, mode `0750`, so it needs `cacheRoot` mounted too.
- Pod: `restartPolicy: Never` (a restarted container would need a new fd, so the plugin recreates the pod), `automountServiceAccountToken: false`, `enableServiceLinks: false`, `fsGroup` = Mount Pod gid (Secret files are `0440`), an `emptyDir` at `/tmp` beside the read-only root filesystem, `terminationGracePeriodSeconds` = drain timeout + unmount wait + 10s.
- The Secret is mounted at `/etc/rclone-mount/`. It holds `spec.json` (serialized Mount Spec, without the password) and `config-password` (optional).
- In the Mount Pod the per-mount directory is mounted at `/run/rclone-mount/`, so the socket is `/run/rclone-mount/handoff.sock`. All paths are `mount` mode flags (`--spec`, `--config-password`, `--socket`, `--cache-dir`) with these defaults.

## spec.json

`mountspec.File`, written by `Spec.Serialize` and read by `mountspec.Load`: `{"version":1,"configData":…,"fsPath":…,"options":{…},"identity":…}`. `options` keeps Mount Spec keys (`vfs-cache-mode`); the Mount Pod maps them onto rclone's mount, VFS and global options and rejects unknown ones. rclone config stays in memory, never on disk.

## Handoff protocol

Package `internal/handoff`. One ASCII message per line on the unix socket; the socket is mode `0660`, owned by the Mount Pod uid/gid.

| Client sends | Server mode | Server replies |
|---|---|---|
| `hello` | fresh (just mounted) | `fd` with the FUSE fd as `SCM_RIGHTS`; the server switches to adopt |
| `adopt` | adopt (mount exists) | `ok` |
| `hello` | adopt | `reject mount-exists`, close |
| `adopt` | fresh | `reject no-mount`, close |

After the handshake the client sends `ready`, `drained` or `drain-timeout`; the server sends nothing more. EOF on either side ends the connection. After a lost connection the Mount Pod redials every second, sends `adopt`, and repeats its drain result if it already had one.

## Sequence

1. `NodePublishVolume`: resolve the Mount Spec and take the per-`<id>` lock. A target that already is a healthy mount returns OK.
2. The node plugin creates `fuse/`, opens `/dev/fuse`, runs mount(2) per ADR 0002 (fstype `fuse.rclone`, `allow_other`, uid/gid = Mount Pod uid/gid), and serves the fd on `handoff.sock`. It then creates the Pod and, once the Pod UID is known, the Secret with its ownerReference; both are idempotent by name. The Secret volume is not optional, so kubelet holds the Pod until the Secret exists. A Pod still terminating fails the publish with `Unavailable`; kubelet retries. Any failure up to step 4 deletes Pod and Secret and unmounts `fuse/`.
3. The Mount Pod (`mount` mode) connects, receives the fd, and runs rclone mount2 in-process on `/dev/fd/N`. Once the VFS is serving, it sends `ready` on the same connection, then keeps the connection open. That open connection is its liveness signal to the plugin.
4. The node plugin waits for `ready` (bounded by the publish timeout), then bind-mounts `fuse/<subpath?>` onto the target path, and remounts read-only if requested.
5. `NodeUnpublishVolume`: unmount the target. If no other targets on this node use `<id>`, start the Linger (`--linger`, chart `mountPod.linger`, default 2m; plugin-wide, not part of the Mount Spec Identity) and return. A publish of `<id>` within the Linger cancels it and reuses the Mount Pod, including the read-only remount for that target. A Linger of 0 deletes the Pod within the RPC. Linger timers live in plugin memory; a plugin restart loses them (ticket 06).
6. Linger expiry, under the `<id>` lock and only if `<id>` still has no users: delete the Pod; the rest runs in the background under the lock. The Mount Pod receives SIGTERM, drains the upload queue (bounded by `--drain-timeout`, default 5m), and sends `drained` or `drain-timeout` on the connection. The plugin then unmounts `fuse/` lazily, and rclone exits 0 on "unmounted externally". Without an unmount within `--unmount-wait` (default 30s) it exits 0 anyway. The plugin waits for the drain result or connection EOF, bounded by drain timeout + unmount wait + 10s. The Secret is garbage-collected; the plugin also deletes it as a fallback. The Pod's `terminationGracePeriodSeconds` covers this whole wait, so kubelet never SIGKILLs a draining Mount Pod.
7. Drain timeout (`drain-timeout`, or no drain result within the bound): the plugin logs a warning and emits a Warning Event, reason `DrainTimeout`, on the Mount Pod and on the last workload pod that published `<id>` (from `podInfoOnMount`: `csi.storage.k8s.io/pod.name`/`pod.namespace`). The workload pod may already be gone; the Event is still listed by `kubectl get events --field-selector involvedObject.name=<pod>`. RBAC: a ClusterRole grants `events` create/patch.

## Restart and failure

- Node plugin restart: running mounts are kept. On start, the plugin lists its Mount Pods on this node and the mounts under `mounts/*/fuse`. Each Mount Pod reconnects to the new plugin's `handoff.sock` and sends `adopt`, which carries no new fd. Users are rebuilt from the mount table: binds of `mounts/<id>/fuse` under the kubelet pods dir.
- Until adoption lands, a publish that finds no live mount for `<id>` lazily unmounts any leftover mount on `fuse/` before mounting afresh.
- Mount Pod death, which shows as connection EOF without `drained`: `fuse/` returns `ENOTCONN`. The plugin reports `VOLUME_CONDITION` abnormal, emits an Event, unmounts `fuse/`, and deletes and recreates the Pod with a new fd. Bind mounts in workload pods stay dead until they are republished: see ticket 06.
- The Mount Pod name stays the same as long as the spec has not changed. A changed spec gets a new `<id>`, and so a new Mount Pod.
