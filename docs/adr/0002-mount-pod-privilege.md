# Unprivileged Mount Pod through FUSE fd passing

Status: accepted

Mount Pods run unprivileged. The node plugin, which is already privileged, opens `/dev/fuse`, mounts it at the Mount Pod's node-local path, and passes the fd over a unix socket (`SCM_RIGHTS`). The Mount Pod runs rclone `mount2` (go-fuse) on `/dev/fd/N`.

## Evidence

Spike (ticket 02) on `oci_test_cluster`, arm64, kernel `7.0.0-1013-oracle`, k3s v1.37.1, containerd 2.3.4, rclone v1.75.2 (`rclone/rclone:1.75.2`), go-fuse v2.11.0, local-dir remote.

- A privileged pod mounted onto a Bidirectional hostPath and sent the fd. An unprivileged pod (uid 1000, all caps zero, no `/dev/fuse`) served it with `rclone mount2 /data /dev/fd/3`.
- A third pod (uid 2000, drop ALL, HostToContainer) read and wrote through the mount, and the write reached the remote.
- The privileged side may close its copy of the fd, or even exit, after sending. The kernel connection lives as long as rclone holds the fd.

## Required settings

- Mount Pod: `privileged: false`, `allowPrivilegeEscalation: false`, `capabilities.drop: [ALL]`, `runAsNonRoot: true`, any non-root uid/gid, `seccompProfile: RuntimeDefault`. It needs no `/dev/fuse` and no propagation on its own mounts. It only needs the socket, through a hostPath dir under the plugin's dir.
- Node plugin: privileged (already the case), with Bidirectional propagation on the dir that holds the Mount Pod mount paths.
- mount(2): `source=<device name>`, `fstype=fuse.rclone`, flags `MS_NOSUID|MS_NODEV` (plus `MS_RDONLY` for read-only volumes), data `fd=N,rootmode=40000,user_id=<mount pod uid>,group_id=<mount pod gid>,allow_other`, plus `default_permissions` if rclone's `--default-permissions` is set. rclone's own mount options are then ignored for the kernel side, so the plugin derives them from the Mount Spec. `allow_other` is required, otherwise only `user_id` can access the mount and workload pods get `EACCES`. The `fuse.rclone` type keeps rclone's mount readiness check and `mount` listings consistent.
- rclone flags: `--allow-non-empty`, because otherwise rclone tries to list `/dev/fd/N` as a directory. Do not pass `--daemon`.

## Implementation: mount2 only

go-fuse treats a `/dev/fd/N` mountpoint as an already open fd (`parseFuseFd` in `fuse/server.go`). bazil (`rclone mount`) has no such path and always calls `fusermount3`. In the spike it failed with `fusermount3: fstat of comm fd 3 failed` and `failed to mount FUSE fs`.

## Caveats

- Between mount(2) and rclone taking over the fd, any access to the path blocks. In the spike, a consumer `cat` hung until the handoff. Do not bind-mount into workload pods before the Mount Pod is ready.
- rclone cannot unmount a `/dev/fd/N` mount. go-fuse returns `Cannot unmount magic mountpoint`. The node plugin calls `umount` on the real path, and on SIGTERM the Mount Pod drains, then signals the plugin. When the plugin unmounts externally, rclone logs `Unmounted externally` and exits 0.
- If the Mount Pod dies, the mount stays in the table and every access returns `ENOTCONN` ("Transport endpoint is not connected"), in workload pods too. Recovery: the plugin unmounts the path (plain `umount` worked, use lazy as a fallback), then mounts again with a new fd for the recreated Mount Pod. Existing workload bind mounts still point at the dead connection and have to be republished, so detection must stay in place (spec story 19).
- The fd handoff needs a rendezvous, a socket per Mount Pod, that the plugin serves while the pod starts. A plugin restart during the handoff means that mount has to be retried.
