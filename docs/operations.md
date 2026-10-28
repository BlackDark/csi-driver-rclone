# Operations

## Mount Pod resources

Chart `mountPod.resources` sets the default for every Mount Pod: requests `cpu: 10m`, `memory: 64Mi`, no limits. The volume context keys `mountPod.cpuRequest`, `mountPod.cpuLimit`, `mountPod.memoryRequest` and `mountPod.memoryLimit` ([volume parameters](reference/volume-parameters.md)) override it per field for one StorageClass or PV. The chart default is node-wide and outside the Mount Spec, so changing it replaces no Mount Pod; the new value applies to Mount Pods started afterwards. A per-volume key is part of the Mount Spec Identity, so changing it takes effect once the old Mount Pod is released.

There is no default memory limit: an OOM kill ends the Mount Pod and drops uploads still pending in memory, and every pod using it gets `MountLost`. Set a limit only with headroom over the estimate below.

Mount Pods run at `mountPod.priorityClassName` (default `system-node-critical`), so the kubelet evicts the apps using them first. Their tolerations are `mountPod.tolerations`, or the node plugin's `node.tolerations` when unset (default), so a Mount Pod can run wherever the node plugin runs.

### Sizing memory

```
memory ≈ base + open files × buffer-size + backend upload buffers + directory cache
```

- base: rclone itself, about 50 to 100Mi.
- `buffer-size` (default 16Mi): each open file buffers up to this much in memory ahead of reads; the maximum is `buffer-size × open files` ([rclone mount, VFS File Buffering](https://rclone.org/commands/rclone_mount/#vfs-file-buffering)). Count the files workloads hold open at once across all pods sharing the Mount Pod.
- `vfs-read-ahead` (only with `vfs-cache-mode: full`) is buffered on disk in the Cache, not in memory ([rclone mount, cache mode full](https://rclone.org/commands/rclone_mount/#vfs-cache-mode-full)). It costs Cache space, not memory.
- Backend upload buffers: multipart uploads hold chunks in memory, e.g. S3 holds up to `upload_concurrency × chunk_size` per transfer (defaults 4 × 5Mi), times `transfers` (default 4) ([rclone S3](https://rclone.org/s3/#s3-upload-concurrency)).
- Directory cache: entries listed within `dir-cache-time`, roughly 1KiB each (an estimate, not an rclone figure). `csi_rclone_vfs_dir_cache_files` and `csi_rclone_vfs_dir_cache_dirs` ([metrics](reference/metrics.md)) show the live count.

Example: 20 open files at the default 16Mi buffer, S3 defaults and 100k cached entries: 100Mi + 320Mi + 80Mi + 100Mi ≈ 600Mi. Check against the Mount Pod's actual working set (`container_memory_working_set_bytes`) under load.

## Workload healing

Off by default. A lost Mount Pod leaves its workload pods with a dead mount (`ENOTCONN`) until they restart (ADR 0003). `mountPod.healWorkloads.enabled=true` (node flag `--heal-workloads`) makes the node plugin evict them once the replacement Mount Pod is ready, so their controller recreates them and they mount at once. Mechanics: [contract](design/mount-pod-contract.md#workload-healing).

- Only pods with a controller ownerReference (ReplicaSet, StatefulSet, DaemonSet, Job, …) are evicted. Bare pods get a `HealBlocked` Event and must be restarted by hand.
- Eviction goes through the Eviction API, so PodDisruptionBudgets apply. A blocking PDB is retried for up to 5 minutes, then `HealBlocked`.
- The UID recorded at publish is a precondition: a pod recreated under the same name is never evicted.

Risks:
- Eviction ends the pod: in-flight work is lost, as with any restart, and a Job pod may count as a failure toward `backoffLimit`.
- A Mount Pod that keeps crashing (bad config, OOM kill under a memory limit) turns into repeated evictions of its workload pods. Watch `csi_rclone_workload_heals_total` and `csi_rclone_mount_lost_total`.
- A StatefulSet pod with a single replica and a PDB of `minAvailable: 1` is never evicted; it gets `HealBlocked`.
- RBAC: the node plugin gains cluster-wide pods `get` and `pods/eviction` `create`. A compromised node plugin, already privileged on its node, could then evict pods anywhere in the cluster.

## Upgrades

A Mount Pod's image is fixed for its lifetime. The image is not part of the Mount Spec Identity, so a new driver image never makes an existing Mount Pod stale.

- `helm upgrade` (new `image.tag`) rolls the node plugin. The new plugin adopts every running Mount Pod as is, in use or lingering; workload IO carries on.
- A publish that finds a Mount Pod already running or lingering reuses it, old image included. Replacing a lingering Mount Pod at publish would only add churn.
- A Mount Pod started after the upgrade uses the new image: on first use on a node, after the Linger of the old one expired, on a node reboot, or after a drain.
- Never: a live Mount Pod is not restarted (a FUSE connection cannot move to a new rclone; ADR 0003).

`csi_rclone_mount_pod_info{image=…}` shows which Mount Pods still run an old image. To move one onto the new image, stop every workload pod using that volume on the node (delete or scale down) and wait out `mountPod.linger`, or cordon and drain the node. The next publish starts a Mount Pod on the new image.
