# 13: Release pipeline

**What to build:** Tag `v<version>` publishes multi-arch images (amd64, arm64, native runners) and the OCI Helm chart to GHCR, and creates a GitHub release with generated notes.

**Blocked by:** 04

**Status:** done

- [x] Actions are pinned by digest; zizmor is clean
- [x] The version check reads the chart version and appVersion

`.github/workflows/release.yml`, `hack/release/check-version.sh`. Images carry SBOM and max provenance; image and chart are signed keyless with cosign. Validated statically (zizmor, actionlint) and the version check locally; the first real run is unverified until the first tag.
