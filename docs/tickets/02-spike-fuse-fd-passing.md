# 02: Spike: unprivileged Mount Pod through FUSE fd passing

**What to build:** A yes/no answer, with a throwaway proof, to the question: can a privileged process open `/dev/fuse` and mount it, then pass the fd over a unix socket to an unprivileged container running rclone `mount2` on `/dev/fd/N`, so that a third container sees the files through Bidirectional propagation?

**Blocked by:** None (can start immediately)

**Status:** ready-for-agent

- [ ] Proof runs on a real cluster node (e.g. `oci_test_cluster`) against a MinIO or local remote
- [ ] Result, the kernel and rclone versions, and the exact securityContext needed are recorded in a short ADR (0002)
- [ ] If it fails, the ADR states the failure and that Mount Pods run privileged
- [ ] The proof code is deleted, not merged
