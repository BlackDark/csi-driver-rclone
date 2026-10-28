# csi-driver-rclone

Kubernetes CSI driver mounting rclone remotes as volumes, for cluster operators. Pre-alpha. Baseline: Linux, Kubernetes ≥1.34 (external-provisioner v6 needs `storage/v1` VolumeAttributesClass; chart `kubeVersion`).

## Verify

`mise run check` green before every commit. Workflow changes: `mise exec -- zizmor .github/workflows` and `mise exec -- actionlint` clean.

## Stack

Follows https://github.com/BlackDark/agent-presets/blob/main/stack/core.md.
Deviations:
- Go, not TypeScript: CSI drivers, client-go, mount-utils and rclone are Go.
- Helm is the only install method (OCI chart on GHCR); no kustomize or raw manifests.
- Images: distroless, non-root, multi-stage, native amd64 + arm64 runners.
- golangci-lint in `mise run check` beside gofmt and go vet.
- CI: `check` gates `mise run check` (includes csi-sanity, `test/sanity`), zizmor + actionlint, `test:fuse:host`, e2e. Go caches restore everywhere and save only on pushes to main; release.yml uses none. Docs-only PRs (`docs/**`, `*.md`) skip `test:fuse` and e2e; pushes to main run everything. `release.yml` on `v*` tags reruns only `mise run check`: `hack/release/check-main-ci.sh` requires the tag commit on main with a passing CI `check` there, and `hack/release/check-version.sh` ties the tag to Chart.yaml.

## Map

- `cmd/csi-driver-rclone`: the one binary; modes `node`, `controller`, `mount`.
- `charts/csi-driver-rclone`: the Helm chart. `hack/e2e` + `test/e2e`: kind + MinIO e2e (`mise run e2e`); the driver binary builds on the host (`Dockerfile` `BIN=local`). e2e tests that restart the node plugin, kill rclone or roll the chart stay serial; the rest `t.Parallel()`, scoped to their own volume.
- Domain terms: `GLOSSARY.md`. Volume keys: `docs/reference/volume-parameters.md`. Decisions: `docs/adr/`. Scope: `docs/scope.md`. Spec: `docs/spec/`. Work items: `docs/tickets/`.
- `refs/`: gitignored reference checkouts; read only, never edit or import.

## Every change

Same PR, only what the change touches:
- tests for the changed behaviour (unit, csi-sanity, e2e)
- docs in `docs/` that describe it; new terms into `GLOSSARY.md`
- README when a user-visible feature changes

## Code style

- Data in, data out: pure functions, side effects (I/O, time, randomness) at the edges and passed in.
- Expected failures are Go `error` values; panic for bugs only.
- Composition over inheritance.
- Small units: split a file past ~400 lines, a function past ~60.
- Names explain what, comments explain why.
- Refactors keep behaviour and land in their own commit.

## Stewardship

Leave every touched area cleaner than you found it.
- Small fixes beside the change (dead code, stale comment or doc, missing test, unclear name): own commit, same PR.
- Whatever your change makes obsolete (code, flags, config, docs, deps) goes in the same PR.
- Larger problems: report to the user with `file:line` and a proposal; the PR stays on topic.

## Git

- Conventional Commits. One topic per PR.
- Merge: rebase when the commits are logically separate, squash otherwise.
- PR already merged → branch anew from `origin/main`, open a new PR.
- Force-push: `--force-with-lease` on own branches only.

## Boundaries

- `sudo`, system packages, global installs: the user runs them; ask with what and why.
- Running user processes stay up; test on other ports.
- Language: code, comments, docs, commits in English.

## Gotchas

- mise task scripts are Tera templates: wrap `{{ }}` (e.g. `go list -f`) in `{% raw %}`.
- `gofmt -l .` would walk `refs/`; the check formats only `go list ./...` package dirs.
