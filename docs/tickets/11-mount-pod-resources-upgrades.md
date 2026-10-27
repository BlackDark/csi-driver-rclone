# 11: Mount Pod resources, priority and upgrade semantics

**What to build:** Mount Pod requests and limits can be set per StorageClass or PV, with chart defaults (64Mi request, no memory limit). Mount Pods run at `system-node-critical` priority with the node plugin's tolerations. A driver upgrade never replaces a ready Mount Pod in use. New Mount Pods use the new image.

**Blocked by:** 05

**Status:** ready-for-agent

- [ ] e2e: after a chart upgrade, an in-use Mount Pod keeps its image; reuse after the Linger gets the new one
- [ ] The docs include the memory sizing formula
