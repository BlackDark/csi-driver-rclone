# 04: Tracer: a Static Volume mounted through a Mount Pod

**What to build:** An admin installs the Helm chart on kind, creates a Credential Secret and a Static Volume for MinIO, and one pod reads and writes files through it. The node plugin creates the owned Secret and the Mount Pod in the driver namespace, waits until it is ready and bind-mounts into the target path. Unpublish removes the bind mount and the Mount Pod (no Linger yet).

**Blocked by:** 01, 02, 03

**Status:** ready-for-agent

- [ ] Distroless non-root image with the single binary; node plugin privileged without hostNetwork; controller absent or idle
- [ ] CSIDriver object as specified in the spec (no attacher)
- [x] Mount Pod securityContext follows ADR 0002
- [x] The Mount Pod manifest builder is a pure function with tests: ownerReference, no credentials in args or env literals
- [ ] RBAC: pods and Secrets in the driver namespace only
- [ ] kind + MinIO e2e: write, read, delete the pod, and the Mount Pod and owned Secret are gone

## Progress

Part A (no Kubernetes API, no CSI yet) is done:
- `internal/handoff`: socket protocol, fd passing, adopt after plugin restart ([contract](../design/mount-pod-contract.md#handoff-protocol)).
- `internal/fusemount`: `/dev/fuse` + mount(2) per ADR 0002, Linux only.
- `internal/rclonemount` + `mount` mode: spec.json, in-memory rclone config, mount2 on `/dev/fd/N`, drain on SIGTERM. rclone v1.75.2.
- `mise run test:fuse`: Docker test; uid 1000 serves the mount, root writes through it, the drain reaches the remote, then the external unmount.

Part B (node plugin, unit tests with fakes) is done:
- `internal/mountpod`: pure `Build` (Pod + Secret), `OwnedBy`, names and paths.
- `internal/node`: Node service; publish ensures FUSE mount, handoff, Pod, owned Secret, waits for `ready`, binds (read-only per publish); unpublish of the last user deletes the Pod, then unmounts `fuse/` after the drain. Linger and restart adoption are seams only.
- `internal/identity`, `internal/csiserver` (unix socket, protosanitizer logging), `node` mode flags.

Left (part C): image, chart (CSIDriver, DaemonSet, RBAC), kind + MinIO e2e.
