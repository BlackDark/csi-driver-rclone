# Metrics

Every component serves Prometheus metrics over plain HTTP at `/metrics`. No rclone rc server runs and no rc credentials exist: Mount Pod stats are read in-process from rclone's accounting and VFS.

| Component | Port (container) | Flag | Paths |
|---|---|---|---|
| Node plugin | 9809 (`metrics.port`) | `node --metrics-addr` | `/metrics` |
| Controller | 9809 (`metrics.port`) | `controller --metrics-addr` | `/metrics` |
| Mount Pod | 9810 | `mount --metrics-addr` | `/metrics`, `/healthz` (readiness) |

Port 9808 is the liveness-probe sidecar's. `metrics.enabled=false` turns off the node plugin and controller endpoints; Mount Pods always serve, since their readiness probe uses `/healthz`.

The endpoints are unauthenticated. Labels carry volume IDs, Mount Pod ids and images, never credentials, remote paths or Mount Options. Restrict access with a NetworkPolicy if volume IDs are sensitive.

All series also come with the Go runtime and process collectors (`go_*`, `process_*`).

## Node plugin and controller

| Series | Type | Labels | Meaning |
|---|---|---|---|
| `csi_rclone_requests_total` | counter | `method`, `code` | CSI RPCs by full gRPC method and gRPC status code |
| `csi_rclone_request_duration_seconds` | histogram | `method` | CSI RPC latency |

## Node plugin only

| Series | Type | Labels | Meaning |
|---|---|---|---|
| `csi_rclone_mount_pods` | gauge | `state` | Mount Pods on this node: `starting` (not ready yet), `ready`, `lingering` (unused, within the Linger), `stopping` (draining or being replaced) |
| `csi_rclone_mount_pod_info` | gauge | `id`, `volume_id`, `image` | 1 per Mount Pod; shows which image each one runs |
| `csi_rclone_mount_healthy` | gauge | `id`, `volume_id` | 1 when the Mount Pod serves its mount; 0 while starting or once lost |
| `csi_rclone_drain_total` | counter | `result` | Mount Pod stops: `drained`, `timeout` (uploads left in the Cache, also a `DrainTimeout` Event), `gone` (exited without a report, e.g. lost) |
| `csi_rclone_mount_lost_total` | counter | | Mount Pods that stopped without the plugin asking (also a `MountLost` Event) |
| `csi_rclone_token_writebacks_total` | counter | `result` | Refreshed OAuth tokens reported by Mount Pods: `written`, `unchanged`, `stale` (a later token was already stored), `error` |
| `csi_rclone_token_lock_wait_seconds` | histogram | | Time a Mount Pod waited for the token refresh Lease |
| `csi_rclone_adoptions_total` | counter | `result` | Mounts found when the node plugin starts: `adopted` or `cleaned` (not adoptable, removed) |
| `csi_rclone_workload_heals_total` | counter | `result` | Workload pods handled by healing after a lost Mount Pod (`mountPod.healWorkloads`): `evicted`, `skipped` (no controller), `blocked` (PDB or API error) |

The node is the scrape target's `node` label (added by the ServiceMonitor relabeling).

## Mount Pod

One Mount Pod serves one volume on one node, so each series has `volume_id`; the PodMonitor adds `node` and `pod`.

| Series | Type | Meaning |
|---|---|---|
| `csi_rclone_transferred_bytes_total` | counter | Bytes moved to and from the remote |
| `csi_rclone_transfers_total` | counter | Completed file transfers |
| `csi_rclone_transfer_errors_total` | counter | rclone errors |
| `csi_rclone_checks_total` | counter | rclone file checks |
| `csi_rclone_deletes_total` | counter | Files deleted on the remote |
| `csi_rclone_vfs_dir_cache_dirs` | gauge | Directories in the VFS directory cache |
| `csi_rclone_vfs_dir_cache_files` | gauge | Files in the VFS directory cache |
| `csi_rclone_vfs_cache_bytes` | gauge | Bytes used by the VFS Cache on disk |
| `csi_rclone_vfs_cache_files` | gauge | Files in the VFS Cache |
| `csi_rclone_vfs_cache_errored_files` | gauge | VFS Cache files whose upload failed |
| `csi_rclone_vfs_uploads_in_progress` | gauge | Uploads running from the VFS Cache |
| `csi_rclone_vfs_uploads_queued` | gauge | Uploads waiting in the VFS Cache |
| `csi_rclone_vfs_cache_out_of_space` | gauge | 1 when the VFS Cache ran out of space |

The `vfs_dir_cache_*` series appear once the mount is up; the `vfs_cache_*` and `vfs_uploads_*` series only with `vfs-cache-mode` other than `off`.

`/healthz` answers 200 while the mount is served and 503 before it is ready and once stopping begins.

## Events

The node plugin records Events (Warning unless noted) on the Mount Pod and the workload pod (via `podInfoOnMount`):

| Reason | When |
|---|---|
| `MountLost` | A Mount Pod stopped unasked, or was lost across a node plugin restart |
| `DrainTimeout` | A stopping Mount Pod did not drain its uploads within `mountPod.drainTimeout` |
| `WorkloadHealed` | Normal: workload pod evicted after its Mount Pod was lost (`mountPod.healWorkloads`) |
| `HealBlocked` | Healing did not evict the workload pod: no controller, PDB still blocking after 5 minutes, or an API error |
| `UnrestrictedOptions` | A publish uses Unrestricted Options (`allowAllOptions`), on the workload pod |

Mount failures reach the workload pod as kubelet's `FailedMount` Event carrying the CSI error.

## Chart

| Value | Default | Effect |
|---|---|---|
| `metrics.enabled` | `true` | Node plugin and controller endpoints plus headless `*-node-metrics` and `*-controller-metrics` Services |
| `metrics.serviceMonitor.enabled` | `false` | ServiceMonitor for the node plugin and controller |
| `metrics.podMonitor.enabled` | `false` | PodMonitor for the Mount Pods |
| `metrics.dashboard.enabled` | `false` | ConfigMap with the Grafana dashboard, labelled `grafana_dashboard: "1"` for the Grafana sidecar |

ServiceMonitor and PodMonitor need the Prometheus Operator CRDs; the chart fails to render without them. The dashboard is `charts/csi-driver-rclone/dashboards/csi-driver-rclone.json`: RPC rate, errors and latency, Mount Pods by state and image, mount health, drain outcomes, lost mounts, adoptions and workload heals, and per volume throughput, errors, Cache use and the upload queue.
