# Glossary

**Remote**: An rclone storage location, such as an S3 bucket or an SFTP path, defined by a backend type and its options.

**Credential Secret**: The Kubernetes Secret that holds a Remote's sensitive options. It is the only accepted source of credentials.

**Static Volume**: A PV an admin writes by hand that points at a Remote and path.

**Dynamic Volume**: A PV created from a StorageClass. Each PVC gets its own templated subpath under the StorageClass Remote. Deleting the PVC leaves the remote data untouched.

**Inline Volume**: A volume declared directly in a pod spec, with no PV or PVC. Its lifetime is the pod's.

**Mount Pod**: The pod that runs one rclone mount for one volume on one node. Every workload pod on that node using the volume shares it.

**Cache**: The rclone VFS cache that belongs to one Mount Pod. It lives on the node and survives Mount Pod replacement.

**Linger**: How long an unused Mount Pod stays up after the last workload pod on its node stops using it.

**Mount Option**: An rclone setting, such as VFS cache mode or dir-cache-time, applied to a mount. Only allowlisted options are accepted.

**Unrestricted Options**: An admin-only opt-in that accepts any rclone option, not only allowlisted ones.
