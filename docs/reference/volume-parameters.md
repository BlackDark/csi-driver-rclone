# Volume parameters

Keys read by Mount Spec resolution (`internal/mountspec`). Terms: [GLOSSARY.md](../../GLOSSARY.md).

## Credential Secret

| Key | Meaning |
|---|---|
| `configData` | rclone INI config; may define several remotes (crypt over OneDrive, union). |
| `configPassword` | Password for an encrypted `configData`. |
| `remoteType` | Shorthand instead of `configData`: backend type, e.g. `s3`. Every other key in the secret becomes a backend option of one generated remote (`access_key_id`, `secret_access_key`, `provider`, ...; dashes become underscores). |
| `remotePath` | Path inside the remote. Optional; set it here or in the volume context, not both. |

`configData` and `remoteType` are mutually exclusive. With `configData`, the secret holds no other keys besides `configPassword` and `remotePath`.

## Volume context (PV `volumeAttributes`, StorageClass parameters, inline `volumeAttributes`)

| Key | Meaning | Inline Volumes |
|---|---|---|
| `remote` | Remote name in `configData` to mount. Optional when it defines one remote. Names the shorthand remote (default `remote`). | yes |
| `remotePath` | Path inside the remote, default root. | yes |
| `allowAllOptions` | `true` enables Unrestricted Options. | rejected |
| `cacheVolume` | Cache volume source as a JSON object, e.g. `{"emptyDir":{}}`. Default: hostPath per volume ID. | rejected |
| `mountPod.cpuRequest`, `mountPod.cpuLimit`, `mountPod.memoryRequest`, `mountPod.memoryLimit` | Mount Pod resources as Kubernetes quantities. `memoryRequest` defaults to `64Mi`. | rejected |
| any other key | A Mount Option: rclone long flag name without dashes, e.g. `vfs-cache-mode`. `vfs_cache_mode` and `--vfs-cache-mode` are accepted. | subset |

Keys containing `/` (Kubernetes-injected, `csi.storage.k8s.io/*`) are ignored.

## Dynamic Volumes (StorageClass parameters)

StorageClass parameters become the volume context of each provisioned PV. The controller validates them with the rules below (no secret needed) and rejects a bad StorageClass at provisioning time.

- `remotePath` is required and templated: `${pvc.namespace}`, `${pvc.name}`, `${pv.name}`. It must contain `${pvc.name}` or `${pv.name}`, so every PVC gets its own path. Other variables are rejected.
- The resolved path must be relative, with no empty, `.` or `..` segments.
- The PV volume context holds the parameters with `remotePath` resolved and without `csi.storage.k8s.io/*` keys. It records the Remote reference and path for a later delete policy.
- Set the Credential Secret with `csi.storage.k8s.io/node-publish-secret-name` and `-namespace` (Kubernetes resolves `${pvc.namespace}` there). The secret must not set `remotePath`, because the volume context already does.
- `DeleteVolume` never touches remote data; `reclaimPolicy: Delete` removes only the PV object. The volume ID is the PV name; the requested capacity is recorded, not enforced.

## Rules

- Credentials are rejected in the volume context: `configData`, `configPassword`, `remoteType`, and any key containing `password`, `pass`, `secret`, `token`, `key` or `credential`.
- Mount Options must be on the allowlist ([spec](../spec/0001-csi-driver-rclone-v1.md#mount-spec-central-pure-module)) unless `allowAllOptions` is `true`.
- Inline Volume subset: `read-only`, `vfs-cache-mode`, `vfs-cache-max-size`, `vfs-cache-max-age`, `vfs-read-ahead`, `vfs-read-chunk-size*`, `buffer-size`, `dir-cache-time`, `poll-interval`, `attr-timeout`, `uid`, `gid`, `umask`, `dir-perms`, `file-perms`, `log-level`, `no-modtime`, `no-checksum`. Rate limits, transfers, retries, `user-agent`, write-back and cache space settings stay with admins.
- Always rejected, even with `allowAllOptions`: `log-file`, `rc*`, `daemon*`, `cache-dir`, `config`.
- `allow-other` is always forced to `true`.
- `vfs-cache-mode` must be `off`, `minimal`, `writes` or `full`. Other values are passed to rclone unchecked and fail at mount time.
- Remote names are suffixed per volume ID (`media` becomes `media_<hash>`), including references such as `remote = onedrive:crypt`, so volumes never share remotes.
- Errors name the key, never its value.
