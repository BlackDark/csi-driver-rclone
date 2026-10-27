# Shared Mount Pod per volume and node

Status: accepted

The rclone FUSE process runs in a dedicated Mount Pod. There is one per volume per node, and every workload pod on that node using the volume shares it. The node plugin bind-mounts the Mount Pod's mount into each pod's target path.

## Why

- Several apps on one node (Sonarr, Radarr, Plex, Jellyfin) need one view of the remote and one VFS cache. rclone does not support two processes sharing one cache dir.
- Mounts must survive node plugin restarts and upgrades. In all three reference drivers FUSE runs inside the plugin, so every plugin restart breaks every mount. The fork works around this with remount state stored in Secrets and a privileged recovery operator, and this design makes both unnecessary.

## Considered: sidecar in the workload pod (GKE gcsfuse-csi pattern)

The node plugin opens `/dev/fuse` and passes the fd to an unprivileged rclone sidecar that a webhook injects into each workload pod.

Strengths: credentials stay in the workload namespace, there is no privileged long-lived FUSE process, the FUSE lifetime and resources follow the pod, and it is unaffected by plugin upgrades.

Rejected because:
- Each pod gets its own rclone and its own VFS cache. Four apps on one remote means four caches, four times the API calls and quota, and no shared view of recent writes.
- It needs a mutating webhook as an extra component, plus native sidecars.
- Supporting both patterns doubles the mount code paths.

## Considered: FUSE in the node plugin

This is the simplest option, but every plugin upgrade breaks every mount. Rejected.

## Consequences

- The plugin needs RBAC to create and delete pods and Secrets, in the driver namespace only.
- Credentials are copied into a Secret owned by the Mount Pod and garbage-collected with it.
- The Mount Pod's privilege depends on whether FUSE fd passing works. See the open spike.
