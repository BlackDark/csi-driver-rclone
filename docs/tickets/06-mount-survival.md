# 06: Mount survival and health

**What to build:** Restarting or upgrading the node plugin leaves every running mount working. After a restart the plugin rebuilds its view of the users of each Mount Pod from the node, with no persisted state. Stale mounts are detected and reported as volume health `INACCESSIBLE` (`NodeGetVolumeHealth`, capability `GET_VOLUME_HEALTH`; ADR 0003). Publish is idempotent and bounded by a timeout, and any orphan cleaned up.

**Blocked by:** 05

**Status:** done

- [x] e2e: kill the node plugin pod during IO; the IO continues and a later unpublish works (also across a DaemonSet rollout)
- [x] e2e: kill the Mount Pod; the condition becomes abnormal and the Mount Pod is recreated (rclone SIGKILL; resume rejected in ADR 0003)
- [x] A repeated publish is a no-op; a hung mount returns within the timeout and leaks nothing
