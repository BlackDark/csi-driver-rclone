# 03: Mount Spec resolution

**What to build:** A pure, table-tested function that turns CSI inputs (secrets, volume context, capability, read-only, lifecycle mode) into a validated Mount Spec or a typed error. It enforces the credential source rules, the Mount Option allowlist, Unrestricted Options (admin-only, never Ephemeral), the Inline Volume subset, forced `allow-other`, always-rejected keys, per-volume remote isolation, `configData`, `configPassword` and the backend-type shorthand.

**Blocked by:** 01

**Status:** done

- [x] Credentials or `configData` in volume context are rejected with the key named
- [x] Unknown option keys are rejected with the key named; `allowAllOptions` lifts this except for Ephemeral
- [x] `log-file`, `rc*`, `daemon*`, `cache-dir` and `config` are always rejected
- [x] Two volumes with the same remote name resolve to non-colliding remotes
- [x] The media-stack example from the spec resolves to the expected Mount Spec
- [x] Error messages never contain secret values
