# 12: csi-sanity in CI

**What to build:** csi-sanity runs in CI against the node plugin and the controller, and is green.

**Blocked by:** 06, 07

**Status:** done

- [x] Known non-applicable tests are skipped explicitly, each with a reason

`test/sanity` runs csi-test v5 in-process with fake Kubernetes, FUSE, handoff and mounter, inside `go test ./...` (part of `mise run check`). Specs for unadvertised capabilities skip themselves; two stateless-controller specs are skipped with reasons in `skipped`. Found and fixed: `NodeGetVolumeHealth` required the optional publish path.
