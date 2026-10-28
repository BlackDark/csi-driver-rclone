# 14: User docs and migration guide

**What to build:** A README and docs that state what is possible and what is not (from the scope doc), install steps, examples for Static, Dynamic and Inline Volumes, and a migration guide from a host systemd `rclone mount` with a hostPath PV to this driver (the media-stack example). The planning docs (spec, tickets, research) stay until the driver is published; stale statements in them are fixed.

**Blocked by:** 06, 07, 08, 09, 10, 11

**Status:** done

- [x] The media-stack example works end to end on `oci_test_cluster`

README, [docs map](../README.md), [migration guide](../guides/migrate-from-systemd.md). Documented baseline is Kubernetes ≥1.34 (external-provisioner v6 needs `storage/v1` VolumeAttributesClass); chart `kubeVersion` follows.

Validation on `oci_test_cluster` (k3s v1.37.1, arm64, single node), image built locally and imported into k3s containerd, chart rendered with `helm template` and applied with kubectl (helm 4.3.1 could not reach the API: TLS handshake timeout, while kubectl worked). MinIO stood in for OneDrive: Credential Secret with crypt over s3 (`chunk_size = 5M` in the s3 section), obscured crypt passwords, config encrypted with `rclone config encryption set` and `configPassword`. PV `media` with the guide's Mount Options (cache sizes 2G/1G and `vfs-write-back: 5s` for the test). Busybox stand-ins as uid 1000: sonarr (`subPath: tv`), radarr (`subPath: movies`), plex and jellyfin read-only on the root.

- One Mount Pod for four pods. Writes from sonarr and radarr read back from plex and jellyfin (md5 of 20MiB match), files owned 1000:1000 mode `rw-rw-r--`, write on read-only plex fails `EROFS`. Bucket holds only crypt-named objects.
- Mount Pod metrics: transfers 2, ~20MB transferred, dir cache counts; node plugin: `mount_pods{state="ready"} 1`, `mount_healthy 1`, image label.
- Node plugin pod deleted during a 1/s append loop in sonarr: no write failed, Mount Pod not restarted, plugin adopted it.
- Move CronJob (Job from it) on the PVC root: `mv` downloads into `tv`, visible in jellyfin.
- After deleting the pods, the Mount Pod stopped after the Linger with no `DrainTimeout`; no FUSE mount left on the node.

Found and fixed during validation:
- Encrypted `configData` was rejected (`no remote sections`): Mount Spec resolution parsed it as INI before isolating remote names. It now decrypts it with `configPassword` (`internal/mountspec/decrypt.go`).
- After a plugin restart, kubelet's `volume-subpaths` binds were adopted as publish targets, so a Mount Pod used with `subPath` was never released. Adoption now counts only `<pods>/<uid>/volumes/kubernetes.io~csi/<name>/mount`.

Cleanup: driver objects (including CSIDriver and cluster RBAC), PV, namespaces `csi-rclone-test` and `csi-rclone-test-minio`, `/var/lib/rclone-csi`, the kubelet plugin dir and the imported image removed; nothing outside these touched.
