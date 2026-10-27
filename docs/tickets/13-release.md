# 13: Release pipeline

**What to build:** Tag `v<version>` publishes multi-arch images (amd64, arm64, native runners) and the OCI Helm chart to GHCR, and creates a GitHub release with generated notes.

**Blocked by:** 04

**Status:** ready-for-agent

- [ ] Actions are pinned by digest; zizmor is clean
- [ ] The version check reads the chart version and appVersion
