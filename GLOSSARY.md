# Glossary

**Remote**: An rclone storage location, such as an S3 bucket or an SFTP path, defined by a backend type and its options.

**Credential Secret**: The Kubernetes Secret that holds a Remote's sensitive options. It is the only accepted source of credentials.

**Static Volume**: A PV an admin writes by hand that points at a Remote and path.

**Dynamic Volume**: A PV created from a StorageClass. Each PVC gets its own templated subpath under the StorageClass Remote. Deleting the PVC leaves the remote data untouched.

**Inline Volume**: A volume declared directly in a pod spec, with no PV or PVC. Its lifetime is the pod's, and it has its own Mount Pod and Cache, never shared with other pods.

**Mount Pod**: The pod that runs one rclone mount for one volume on one node. Every workload pod on that node using the volume shares it.

**Cache**: The rclone VFS cache that belongs to one Mount Pod. It lives on the node and survives Mount Pod replacement.

**Linger**: How long an unused Mount Pod stays up after the last workload pod on its node stops using it.

**Drain**: A stopping Mount Pod waiting for its upload queue to empty, bounded by the drain timeout. A timeout leaves pending writes in the Cache and emits a `DrainTimeout` Event.

**Mount Option**: An rclone setting, such as VFS cache mode or dir-cache-time, applied to a mount. Only allowlisted options are accepted.

**Unrestricted Options**: An admin-only opt-in that accepts any rclone option, not only allowlisted ones.

**Handoff**: The node plugin passing the FUSE fd it mounted to a Mount Pod over a unix socket. The socket connection stays open afterwards as the Mount Pod's liveness signal.

**Adoption**: A restarted node plugin taking over the running Mount Pods on its node. It rebuilds their users from the mount table, and each Mount Pod reconnects with `adopt`.

**Mount Lost**: A Mount Pod that stopped while the node plugin did not stop it. Its binds return `ENOTCONN` until their pods restart. The plugin emits a `MountLost` Event and recreates the Mount Pod.

**Token Write-back**: Keeping an OAuth token that rclone refreshed inside a Mount Pod, so the next Mount Pod and every other process on the same token use the newest one. One refresher at a time holds a refresh Lease. On by default; `tokenWriteBack: "false"` opts a volume out.

**Token Secret**: The driver-owned Secret in the driver namespace that holds the current token of one OAuth grant, keyed by the hash of the token it was seeded from in the Credential Secret. The Credential Secret is never written.

**Heal**: Opt-in eviction of a controller-owned workload pod after its Mount Lost, so its controller recreates it and it remounts. Bare pods are not healed.
