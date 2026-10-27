# 03: Mount Spec resolution

**What to build:** A pure, table-tested function that turns CSI inputs (secrets, volume context, capability, read-only, lifecycle mode) into a validated Mount Spec or a typed error. It enforces the credential source rules, the Mount Option allowlist, Unrestricted Options (admin-only, never Ephemeral), the Inline Volume subset, forced `allow-other`, always-rejected keys, per-volume remote isolation, `configData`, `configPassword` and the backend-type shorthand.

**Blocked by:** 01

**Status:** ready-for-agent

- [ ] Credentials or `configData` in volume context are rejected with the key named
- [ ] Unknown option keys are rejected with the key named; `allowAllOptions` lifts this except for Ephemeral
- [ ] `log-file`, `rc*`, `daemon*`, `cache-dir` and `config` are always rejected
- [ ] Two volumes with the same remote name resolve to non-colliding remotes
- [ ] The media-stack example from the spec resolves to the expected Mount Spec
- [ ] Error messages never contain secret values
