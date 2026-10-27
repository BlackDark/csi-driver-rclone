# 04: Tracer: a Static Volume mounted through a Mount Pod

**What to build:** An admin installs the Helm chart on kind, creates a Credential Secret and a Static Volume for MinIO, and one pod reads and writes files through it. The node plugin creates the owned Secret and the Mount Pod in the driver namespace, waits until it is ready and bind-mounts into the target path. Unpublish removes the bind mount and the Mount Pod (no Linger yet).

**Blocked by:** 01, 02, 03

**Status:** ready-for-agent

- [ ] Distroless non-root image with the single binary; node plugin privileged without hostNetwork; controller absent or idle
- [ ] CSIDriver object as specified in the spec (no attacher)
- [ ] Mount Pod securityContext follows ADR 0002
- [ ] The Mount Pod manifest builder is a pure function with tests: ownerReference, no credentials in args or env literals
- [ ] RBAC: pods and Secrets in the driver namespace only
- [ ] kind + MinIO e2e: write, read, delete the pod, and the Mount Pod and owned Secret are gone
