# Mount Pod crash recovery: recreate, do not resume

Status: accepted

A crashed Mount Pod is not resumed on its old FUSE connection. The node plugin reports the existing binds as dead, emits `MountLost` Events, unmounts `fuse/`, and recreates the Mount Pod with a fresh fd, so new publishes work. Pods that used the old mount must be restarted to remount. The Mount Pod keeps `restartPolicy: Never`.

## Considered: resume the connection

The plugin keeps a dup of the FUSE fd, the Mount Pod restarts in place (`restartPolicy: Always`), receives the dup again, and a fresh rclone serves it with go-fuse `mount2`. Existing binds would heal without restarting workload pods.

## Evidence

Spike in the `mise run test:fuse` container (OrbStack, kernel 7.2.12-orbstack, aarch64), rclone v1.75.2, go-fuse v2.10.1, local remote. The test is not kept. Steps: mount `fuse/`, keep a dup of the fd, hand the fd to `csi-driver-rclone mount`, bind `fuse/` elsewhere, write and list through the bind, hold a dir handle open, `SIGKILL` rclone, then hand the dup to a second `csi-driver-rclone mount`.

- During the outage, `stat` through the bind hangs. The dup keeps the connection alive with no server, so requests queue instead of failing with `ENOTCONN`.
- The second rclone panics right away: `panic: unknown node 2` in `fs.(*rawBridge).inode` (`fs/bridge.go:353`), called from `fuse.(*Server).handleInit` (`fuse/server.go:503`). go-fuse expects `FUSE_INIT` as the first request. The kernel sent `INIT` once, at mount, and the first request the new server reads is a queued `GETATTR` for node id 2, which it has never seen. Unknown node ids are a `log.Panicf`, not an error.
- After that panic, reads and `readdir` through the bind hang again.

So resume needs go-fuse to skip `INIT` on an initialized connection and to answer unknown node ids with `ESTALE` instead of panicking. That would mean a fork of go-fuse and rclone's `mount2` glue. Holding the dup also turns a crash into a hang rather than a clean `ENOTCONN`. Rejected for v1.

## Consequences

- Detection: handoff EOF without a drain result, or the Mount Pod watch seeing it Failed, Succeeded or deleted while the plugin did not stop it.
- The plugin closes its fd copy after `ready`, so a crash gives `ENOTCONN` at once and nothing hangs. No abort through `/sys/fs/fuse/connections` is needed.
- Health: `NodeGetVolumeHealth` reports dead targets `INACCESSIBLE` (reason `MountPodLost`), and kubelet writes that to `pod.status.volumeHealth` when the `CSIVolumeHealth` gate is on. CSI 1.13 replaced `VOLUME_CONDITION` with this RPC. `NodeGetVolumeStats` returns `Unavailable` for dead targets.
- A Mount Pod adopted after a plugin restart has no Mount Spec in memory. If it crashes, it is cleaned up but not recreated until the next publish.
- Opt-in healing (`mountPod.healWorkloads.enabled`, node flag `--heal-workloads`, default off) automates the restart: once the replacement Mount Pod is ready, or recreating gave up or the spec is unknown, the plugin evicts each dead bind's workload pod through the Eviction API, so PodDisruptionBudgets hold and its controller recreates it with a fresh mount. Only controller-owned pods whose UID still matches are evicted; bare pods get a `HealBlocked` Event instead. It needs cluster-wide pods `get` and `pods/eviction` `create`, granted only when enabled ([operations](../operations.md#workload-healing)).
- Revisit if go-fuse gains reconnect support: the handoff protocol could get a `resume` message.
