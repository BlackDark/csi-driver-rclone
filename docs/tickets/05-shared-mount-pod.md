# 05: Shared Mount Pod, per-pod read-only, Linger, drain

**What to build:** Two pods on one node use the same volume, with different `subPath`s, one of them read-only, and they share one Mount Pod. When the last one leaves, the Mount Pod lingers (default 2m, configurable), then drains the upload queue (bounded) and stops.

**Blocked by:** 04

**Status:** done

- [x] e2e: two pods, one Mount Pod; the read-only pod gets EROFS on write
- [x] e2e: a republish within the Linger reuses the Mount Pod
- [x] e2e: a write with `vfs-write-back` is on MinIO after the Mount Pod stops
- [x] A drain timeout emits an event
