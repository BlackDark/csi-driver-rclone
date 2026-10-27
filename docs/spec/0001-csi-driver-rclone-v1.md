# Spec 0001: csi-driver-rclone v1

Status: ready-for-agent. This is a temporary planning doc, to be removed when the driver is published.

Related: [GLOSSARY](../../GLOSSARY.md), [ADR 0001](../adr/0001-shared-mount-pod-per-volume-and-node.md), [Scope](../scope.md), [Reference analysis](../research/reference-analysis.md).

## Problem Statement

A homelab or cluster operator mounts rclone remotes (for example a crypt remote over OneDrive) into Kubernetes workloads. Today that takes a systemd `rclone mount` on the host plus a hostPath PV, outside Kubernetes. That setup has four problems:
- It is not portable across nodes.
- Its credentials sit on the host.
- Its flags are not reviewed.
- It has no metrics.

The existing rclone CSI drivers fail in other ways:
- Every node plugin restart or upgrade breaks every mount.
- Credentials leak into PV objects, logs or `/tmp`.
- Any PV author can inject arbitrary flags.
- The node plugin needs cluster-wide Secret access.
- The controller is privileged for no reason.
- The code is large or untested.

## Solution

A Kubernetes CSI driver, `rclone.csi.blackdark.github.io`, that mounts any rclone Remote as a volume.

- For each volume on each node, the driver starts one Mount Pod in its own namespace. That Mount Pod runs `rclone mount` and owns the volume's Cache.
- Every workload pod on that node using the volume receives a bind mount of that Mount Pod's mount. Several apps (Sonarr, Radarr, Plex, Jellyfin) therefore share one rclone process, one VFS cache and one view of the remote.
- Mounts survive node plugin restarts and upgrades.
- Credentials come only from Credential Secrets.
- rclone options are allowlisted unless an admin opts into Unrestricted Options.
- Metrics are exposed for Prometheus.
- Installation is a Helm chart, with distroless non-root images for amd64 and arm64.

## User Stories

### Volumes

1. As an admin, I want to define a Static Volume pointing at a Remote and path, so that existing data on a remote is available to pods.
2. As an admin, I want a StorageClass that creates a Dynamic Volume per PVC under a templated subpath (`${pvc.namespace}`, `${pvc.name}`, `${pv.name}`), so that teams get isolated directories without manual PVs.
3. As an admin, I want deleting a Dynamic Volume to leave remote data untouched, so that a deleted PVC never destroys data.
4. As a developer, I want to declare an Inline Volume in my pod spec, so that I can mount a remote without a PV or PVC.
5. As a developer, I want Inline Volumes to read credentials from a Secret in my own namespace, so that I never need access to other namespaces.
6. As an admin, I want the driver to declare ReadWriteMany, ReadOnlyMany and ReadWriteOnce, so that existing PVC manifests work.

### Shared mounts

7. As an operator, I want all pods on a node using the same volume to share one Mount Pod, so that there is one rclone process, one cache and one set of API calls.
8. As an operator, I want each pod to use its own `subPath` of a shared volume, so that apps see only their folder while sharing the cache.
9. As an operator, I want an individual pod to mount a shared volume read-only, so that consumers like Plex cannot write even when the volume is writable.
10. As an operator, I want the same volume usable on several nodes, so that pods can be scheduled freely.
11. As an operator, I want each node to get its own Mount Pod and Cache for a volume, so that nodes never share mutable cache state.
12. As an operator, I want an unused Mount Pod to stay up for a configurable Linger (default 2m) after the last pod leaves, so that rolling updates do not tear down and rebuild the mount.
13. As an operator, I want a Mount Pod to drain its upload queue before stopping, bounded by a timeout, so that pending writes reach the remote.
14. As an operator, I want a timed-out drain to be reported as an event and a metric, so that I know data may still be in the Cache.

### Survival and upgrades

15. As an operator, I want node plugin restarts and upgrades to leave running mounts untouched, so that upgrading the driver never breaks my apps.
16. As an operator, I want a running Mount Pod to keep its image until it is replaced (on node reboot, drain, or reuse after the Linger expires), so that upgrades never restart a live mount.
17. As an operator, I want a metric showing the version each Mount Pod runs, so that I can see which mounts still use an old image.
18. As an operator, I want a crashed Mount Pod to be recreated, and new publishes to be blocked until it is ready, so that a mount failure is visible and recovers.
19. As an operator, I want a stale mount ("transport endpoint is not connected") detected and reported as CSI `VOLUME_CONDITION` abnormal, so that I can act on it.
20. As an operator, I want publish to be idempotent, so that kubelet retries never double-mount.
21. As an operator, I want publish bounded by a timeout, with any orphaned in-flight mount cleaned up, so that a hung remote never wedges the node plugin.

### Credentials

22. As an admin, I want credentials accepted only from Credential Secrets (`nodePublishSecretRef`, or the pod-namespace Secret for Inline Volumes), so that anyone who can read PVs cannot read credentials.
23. As an admin, I want `configData` or credential-like keys in `volumeAttributes` or StorageClass parameters to be rejected with a clear error, so that mistakes fail loudly.
24. As an admin, I want `configData` to hold a full rclone config with nested remotes (crypt over OneDrive, union), so that complex setups work.
25. As an admin, I want `configPassword` for encrypted configs, so that I can keep my config encrypted at rest.
26. As an admin, I want a simple Remote definable by backend type plus options, without a full config, so that easy cases stay easy.
27. As an admin, I want remote names isolated per volume, so that two volumes with the same remote name never collide.
28. As a security reviewer, I want credentials passed to rclone only through files or environment inside the Mount Pod, never in args or logs, so that `ps` and logs leak nothing.
29. As a security reviewer, I want the credentials copied for a Mount Pod held in a Secret owned by that pod and garbage-collected with it, so that no credential copies outlive the mount.
30. As a security reviewer, I want all gRPC request logging sanitized, so that secrets never reach logs at any verbosity.

### Mount Options

31. As an admin, I want to set allowlisted Mount Options (`vfs-*`, `dir-cache-time`, `poll-interval`, `buffer-size`, `tpslimit*`, `bwlimit*`, `transfers`, `retries`, timeouts, `user-agent`, `uid`, `gid`, `umask`, `log-level`) on a StorageClass or Static Volume, so that I can tune each volume.
32. As an admin, I want unknown options rejected with the name of the offending key, so that typos fail fast.
33. As an admin, I want `allowAllOptions: "true"` on a StorageClass or Static Volume to accept any rclone option, so that I can use rare flags without waiting for a release.
34. As a security reviewer, I want Unrestricted Options refused on Inline Volumes, with an event emitted whenever they are used, so that pod authors cannot escalate.
35. As a developer, I want Inline Volumes to accept only a safe subset of Mount Options, so that I can tune read-only and cache settings myself.
36. As an admin, I want backend options (OneDrive chunk size, delta) set in `configData`, so that the allowlist stays small.
37. As an operator, I want `allow-other` always on and logs always on stdout, so that every pod uid can access the mount and logs reach my log stack.
38. As an operator, I want ownership fixed per volume through `uid`, `gid` and `umask`, with `fsGroupPolicy: None`, so that kubelet never chowns a remote recursively.

### Cache

39. As an operator, I want the Cache on a node hostPath (configurable root) by default, so that it survives Mount Pod replacement.
40. As an operator, I want to set `cacheVolume` to any Kubernetes volume source (for example an LVM-backed volume), so that the cache lives on dedicated storage.
41. As an operator, I want each volume's Cache in its own directory, so that volumes never share a cache dir.
42. As an operator, I want the cache size bounded by `vfs-cache-max-size` and `vfs-cache-min-free-space`, so that the node disk never fills.

### Resources and scheduling

43. As an operator, I want Mount Pod requests and limits settable per StorageClass or PV, with chart defaults (64Mi request, no memory limit), so that I can size heavy mounts.
44. As an operator, I want Mount Pods at `system-node-critical` priority and tolerating whatever the node plugin tolerates, so that they are not evicted before the apps that use them.
45. As an operator, I want docs on sizing memory from `buffer-size`, `vfs-read-ahead` and the number of open files, so that I can set limits safely.

### Observability

46. As an operator, I want Prometheus metrics for CSI operations (count, latency, errors by RPC), so that I can alert on driver failures.
47. As an operator, I want per-volume VFS and transfer metrics (bytes, errors, cache use, upload queue), so that I can see cache efficiency and pending uploads.
48. As an operator, I want a mount health gauge per volume and node, so that I can alert on broken mounts.
49. As an operator, I want an optional ServiceMonitor, PodMonitor and Grafana dashboard in the chart, so that observability works out of the box.
50. As an operator, I want Kubernetes events on the Mount Pod and workload pod for mount failures, drain timeouts and Unrestricted Options use, so that `kubectl describe` shows the cause.
51. As an operator, I want liveness and readiness probes on every component, so that Kubernetes restarts broken components.
52. As an operator, I want metrics to need no rclone rc server or rc credentials, so that no control endpoint is exposed.

### Security

53. As a security reviewer, I want the node plugin to be the only privileged component, with no hostNetwork, so that the privileged surface is minimal.
54. As a security reviewer, I want the controller unprivileged and non-root, so that dynamic provisioning carries no node access.
55. As a security reviewer, I want the Mount Pod unprivileged if FUSE fd passing works (privileged otherwise, and documented), so that the long-running rclone process has the least privilege possible.
56. As a security reviewer, I want RBAC limited to pods and Secrets in the driver namespace plus what the CSI sidecars need, so that no component has cluster-wide Secret access.
57. As a security reviewer, I want distroless non-root images with read-only root filesystems and pinned dependencies, so that the supply chain and runtime surface are small.
58. As a security reviewer, I want CI to run zizmor and pin actions by digest, so that workflows are hardened.

### Install, release, development

59. As an operator, I want a Helm chart published as an OCI chart on GHCR, so that I install with one command.
60. As an operator, I want multi-arch images (amd64, arm64) on GHCR, so that it runs on mixed clusters.
61. As an operator, I want Kubernetes ≥1.29 on Linux as the documented baseline, so that I know what is supported.
62. As an operator, I want the docs to state clearly what is possible and what is not, so that I can judge fit before installing.
63. As an operator, I want a migration guide from a host systemd `rclone mount` with a hostPath PV, so that I can move my media stack.
64. As a contributor, I want `mise run check` to run gofmt, go vet, golangci-lint and go test, so that one command verifies everything.
65. As a contributor, I want csi-sanity and a kind + MinIO e2e suite in CI, so that conformance and real mounts are verified.
66. As a maintainer, I want releases started by tag `v<version>`, with Conventional Commits and Renovate, so that upgrades are routine.

## Implementation Decisions

### Components

- **One binary with three modes**: `node`, `controller` and `mount`. One image.
- **Node plugin** (DaemonSet). Implements Identity and Node: `NodePublishVolume`, `NodeUnpublishVolume`, `NodeGetCapabilities`, `NodeGetInfo` and `NodeGetVolumeStats`. Capabilities: `VOLUME_CONDITION` and `GET_VOLUME_STATS`, nothing else.
  - No `STAGE_UNSTAGE` in v1. Sharing is done through the Mount Pod, not through kubelet staging.
  - Responsibilities: resolve the Mount Spec, ensure the Mount Pod for (volume, node), wait until it is ready, bind-mount its mount (with an optional read-only remount) into the target path, and track users so the Linger can start.
  - Privileged, with Bidirectional propagation on the kubelet dir and no hostNetwork.
- **Controller** (Deployment). Implements Identity and Controller with `CREATE_DELETE_VOLUME` only, behind external-provisioner with `--extra-create-metadata`.
  - `CreateVolume` resolves the `remotePath` template and returns a volume ID and context. It never contains credentials.
  - `DeleteVolume` succeeds without touching data. A future delete policy is kept possible, because the volume context records the Remote reference and the resolved path.
  - Unprivileged, non-root, no hostPath.
- **Mount Pod**: runs in `mount` mode. It reads the Mount Spec and credentials from its owned Secret, runs rclone in-process through the rclone library using the `mount2` (go-fuse) implementation, and serves metrics.
  - On SIGTERM it drains the upload queue (bounded), then unmounts.
  - One Mount Pod per (volume ID, node). Its name is derived from a hash of both, so creating it is idempotent.
- **CSIDriver object**: `attachRequired: false`, `podInfoOnMount: true`, `fsGroupPolicy: None`, `volumeLifecycleModes: [Persistent, Ephemeral]`, `requiresRepublish: false`. No attacher sidecar.

### Mount Spec (central pure module)

A pure function turns the CSI inputs into a validated Mount Spec, or a typed error:
- Inputs: secrets, volume context, volume capability, read-only flag, and the lifecycle mode (Persistent or Ephemeral).
- Validation:
  - Credentials and `configData`/`configPassword` are accepted only from secrets.
  - Option keys are checked against the allowlist, or against Unrestricted Options when they are permitted (never for Ephemeral). Inline Volumes use a smaller subset.
- Normalization: options are normalized and remote names are isolated per volume.
- Output: the Remote (config plus fs path), the Mount Options, the Mount Pod resources, the cache source, and an identity hash for sharing.

The same function runs in the controller (for parameter validation where possible) and in the node plugin.

Allowlist (generic flags only):
- VFS: `vfs-cache-mode`, `vfs-cache-max-size`, `vfs-cache-min-free-space`, `vfs-cache-max-age`, `vfs-cache-poll-interval`, `vfs-read-ahead`, `vfs-read-chunk-size*`, `vfs-write-back`, `vfs-fast-fingerprint`
- Caching and polling: `dir-cache-time`, `poll-interval`, `attr-timeout`, `buffer-size`
- Rate limits: `tpslimit`, `tpslimit-burst`, `bwlimit`, `bwlimit-file`
- Transfers and retries: `transfers`, `checkers`, `retries`, `low-level-retries`, `timeout`, `contimeout`
- Identity and permissions: `user-agent`, `uid`, `gid`, `umask`, `dir-perms`, `file-perms`
- Other: `read-only`, `log-level`, `no-modtime`, `no-checksum`

`allow-other` is forced on. `log-file`, `rc*`, `daemon*`, `cache-dir` and `config` are always rejected.

### Mount Pod contract

- The node plugin creates, in the driver namespace:
  - a Secret holding the serialized Mount Spec and credentials,
  - the Mount Pod, which owns that Secret through an ownerReference.
- The Mount Pod mounts at a node-local path under the plugin's dir. The node plugin bind-mounts from that path.
- The Cache is a hostPath subdirectory per volume ID under a configurable root, unless `cacheVolume` is set.
- Users are tracked from the target paths found on the node at publish and unpublish time. They are reconstructed from mount table and kubelet dirs after a plugin restart. There is no persisted state.
- The Linger timer runs in the node plugin. It is reset on publish, and when it expires the plugin deletes the Mount Pod. The plugin's termination grace period covers the drain timeout.
- Upgrades: Mount Pods carry an image label. The node plugin never replaces a ready Mount Pod that is in use.

### Privilege spike (first ticket, blocking)

Find out whether the node plugin can open `/dev/fuse`, perform the FUSE mount, and pass the fd over a unix socket to an unprivileged Mount Pod running rclone `mount2` with a `/dev/fd/N` mountpoint. The result decides the Mount Pod's securityContext:
- If it works, the Mount Pod is unprivileged with no `SYS_ADMIN`.
- If it does not, the Mount Pod is privileged, and that is documented in the scope doc.

### Metrics

- Node plugin and controller: CSI RPC count, latency and errors, Mount Pod count by state and version, mount health gauge, drain outcome.
- Mount Pod: per-volume VFS and transfer stats, taken in-process from rclone accounting and VFS stats. No rc server.
- Chart: optional ServiceMonitor (plugin and controller), PodMonitor (Mount Pods) and Grafana dashboard.

### Packaging and conventions

- Go, module `github.com/BlackDark/csi-driver-rclone`. CSI spec Go bindings, the kubernetes mount-utils library, client-go, the rclone library, and the Prometheus client.
- Distroless non-root image built multi-stage on native amd64 and arm64 runners, following the presets.
- Helm is the only install method, as an OCI chart on GHCR. No kustomize.
- Tooling: `mise.toml` with pinned tools. `mise run check` runs gofmt, go vet, golangci-lint and go test. CI is one `check` gate, plus zizmor, csi-sanity and e2e. Renovate uses `config:best-practices`. Releases are cut by tag `v<version>`. These deviations from the presets are recorded in AGENTS.md.
- Files stay around 400 lines or less and functions around 60 lines. Expected failures are Go `error` values.

## Testing Decisions

- Tests check external behavior only:
  - Mount Spec inputs produce outputs or errors.
  - CSI RPCs produce responses and observable mounts.
  - Kubernetes objects are created or deleted.
- Seams, highest first:
  1. **e2e** (kind + MinIO, ported from the fork's harness):
     - Static, Dynamic and Inline Volumes.
     - Two pods on one node share one Mount Pod.
     - Read-only per pod.
     - Restarting the node plugin keeps the mounts alive.
     - The Linger, then the Mount Pod is deleted.
     - Drain on stop.
     - Credentials rejected in `volumeAttributes`.
  2. **csi-sanity** against the node plugin and the controller.
  3. **Mount Spec** pure function, table-driven: allowlist, Unrestricted Options, Ephemeral restrictions, credential source rules, remote isolation, path templating.
  4. **Mount Pod manifest builder** (a pure function from a Mount Spec to Pod and Secret objects), checked against a fake client: ownerReferences, securityContext, priority, cache source, no credentials in args or env literals.
- Prior art: the fork's kind + MinIO e2e, and the veloxpack utils tests for remote isolation and templating.
- No tests that mock rclone internals.

## Out of Scope

- A sidecar mount pattern (see ADR 0001), and FUSE inside the node plugin.
- Deleting remote data (planned later, kept possible).
- rc API, and a `vfs/refresh` trigger (planned later, kept possible).
- A cache on a PVC owned by the driver.
- Per-pod uid or gid (idmapped mounts are a possible later option), and `fsGroup` handling.
- Cross-node cache consistency.
- Capacity, quotas, snapshots, cloning and expansion.
- mergerfs and composing with other drivers (to be designed in `csi-driver-mergerfs`).
- Windows nodes, and Kubernetes < 1.29.
- Kustomize manifests.

## Further Notes

- Media stack migration example: one Static Volume `media` with `ReadWriteMany`.
  - The Credential Secret holds `configData` (crypt over OneDrive, with `chunk_size = 5M` and `delta = true` in the OneDrive section) and `configPassword`.
  - Mount Options taken from the current systemd unit: `vfs-cache-mode: full`, `vfs-cache-max-size: 70G`, `vfs-cache-min-free-space: 15G`, `vfs-cache-max-age: 24h`, `vfs-write-back: 5m`, `vfs-read-ahead: 128M`, `buffer-size: 64M`, `dir-cache-time: 5000h`, `poll-interval: 10s`, `tpslimit: 8`, `tpslimit-burst: 8`, `bwlimit-file: 25M:off`, `transfers: 1`, `retries: 10`, `low-level-retries: 20`, `timeout: 1h`, `contimeout: 60s`, `uid: 1000`, `gid: 1000`, `umask: 002`, `user-agent: MediaArr`.
  - The move job becomes a CronJob. Polling replaces `vfs/refresh`.
- The config password from the systemd units was shared in plain text during planning and must be rotated.
