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
- Cache: hostPath `<cacheRoot>/<volume hash>`, or `cacheVolume`. It is mounted at `/cache`.
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

1. `NodePublishVolume`: resolve the Mount Spec, then ensure the Secret and Pod exist, which is idempotent by name.
2. The node plugin creates `fuse/`, opens `/dev/fuse`, runs mount(2) per ADR 0002 (fstype `fuse.rclone`, `allow_other`, uid/gid = Mount Pod uid/gid), and serves the fd on `handoff.sock`.
3. The Mount Pod (`mount` mode) connects, receives the fd, and runs rclone mount2 in-process on `/dev/fd/N`. Once the VFS is serving, it sends `ready` on the same connection, then keeps the connection open. That open connection is its liveness signal to the plugin.
4. The node plugin waits for `ready` (bounded by the publish timeout), then bind-mounts `fuse/<subpath?>` onto the target path, and remounts read-only if requested.
5. `NodeUnpublishVolume`: unmount the target. If no other targets on this node use `<id>`, start the Linger.
6. Linger expiry: delete the Pod. The Mount Pod receives SIGTERM, drains the upload queue (bounded by `--drain-timeout`, default 5m), and sends `drained` or `drain-timeout` on the connection. The plugin then unmounts `fuse/` lazily, and rclone exits 0 on "unmounted externally". Without an unmount within `--unmount-wait` (default 30s) it exits 0 anyway. The Secret is garbage-collected.

## Restart and failure

- Node plugin restart: running mounts are kept. On start, the plugin lists its Mount Pods on this node and the mounts under `mounts/*/fuse`. Each Mount Pod reconnects to the new plugin's `handoff.sock` and sends `adopt`, which carries no new fd. Users are rebuilt from the mount table: binds of `mounts/<id>/fuse` under the kubelet pods dir.
- Mount Pod death, which shows as connection EOF without `drained`: `fuse/` returns `ENOTCONN`. The plugin reports `VOLUME_CONDITION` abnormal, emits an Event, unmounts `fuse/`, and deletes and recreates the Pod with a new fd. Bind mounts in workload pods stay dead until they are republished: see ticket 06.
- The Mount Pod name stays the same as long as the spec has not changed. A changed spec gets a new `<id>`, and so a new Mount Pod.
