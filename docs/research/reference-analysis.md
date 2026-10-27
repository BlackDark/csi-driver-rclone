# Reference analysis

Sources (cloned under `refs/`, not committed):

- `veloxpack/csi-driver-rclone` at `b15b528`: upstream of the fork
- `BlackDark/fork-csi-driver-rclone`: 52 commits on top of upstream HEAD
- `wunderio/csi-rclone` v3.1.0 at `0744872`
- `BlackDark/agent-presets`: general project presets

## Ideas to keep

- Per-volume remote-name isolation: config remotes are prefixed with a hash of the volume (veloxpack `utils.go:282-350`).
- A `configData` rclone INI, which supports nested remotes such as crypt or union.
- The on-the-fly `:backend:` syntax, so no config file is needed for simple remotes (wunderio).
- `remotePath` templating from PVC metadata through `--extra-create-metadata` (both repos).
- Wait for the VFS upload queue to drain before unmount, bounded by ctx (wunderio `nodeserver.go:244-283`, veloxpack `waitForVFSCacheSync`).
- A bounded mount timeout that reaps orphan mounts (fork `nodeserver.go:1131-1151`).
- Idempotent NodePublish, and stale-mount detection with cleanup ("transport endpoint is not connected").
- `VOLUME_CONDITION` reported from mount health probes (fork).
- Request logging through `protosanitizer.StripSecrets`.
- Per-volume VFS metrics (veloxpack `metrics_collector.go`).
- Builds from the module proxy, with no vendor/ and no patch files (fork).
- A kind + MinIO e2e harness (fork `hack/e2e`).

## Problems to avoid

| Problem | Where | Consequence |
|---|---|---|
| FUSE runs inside the node plugin (in-process or `--daemon` child) | all three | A plugin restart or upgrade kills every mount. Pods see `ENOTCONN`. |
| Remount state with credentials stored in Secrets | fork `mount_state.go` | Needs cluster-wide secrets write RBAC. Credentials are copied. |
| Privileged recovery operator that deletes or patches workloads | fork `pkg/operator` | Large blast radius. It only exists to work around the item above. |
| `volumeContext` overrides secrets, and any key is passed through as an rclone flag | veloxpack `nodeserver.go:276-330`, wunderio | A PV author can inject options. Credentials end up in PV objects. |
| One global credential Secret, with secrets `list` cluster-wide | wunderio | No tenant isolation. |
| Controller runs privileged with hostNetwork while DeleteVolume is a no-op | veloxpack chart | Privilege with no use. Undocumented retain semantics. |
| `attachRequired: true` with no ControllerPublish | wunderio | Dead attacher sidecar and extra RBAC. |
| RC credentials in args, an `--rc-no-auth` option, rc over unauthenticated TCP | veloxpack chart, wunderio | Local takeover of rclone. Credentials visible in `ps`. |
| Temp config files never deleted. `%+v` logging of requests. | wunderio `nodeserver.go:69,429` | Credentials leak to disk and logs. |
| Root image. curl\|bash install of an unpinned rclone. trivy `@master`. | wunderio, veloxpack | Supply-chain and runtime risk. |
| 1.6k-line `nodeserver.go`, commented-out code, `RPC_UNKNOWN` capability | fork | Hard to maintain. |
| Personal go-fuse fork in `replace` | fork `go.mod` | Maintenance burden. |
| No tests and no csi-sanity | wunderio | No conformance guarantee. |

## Presets that apply

- `mise.toml` with pinned tools, and `mise run check` = gofmt + `go vet` + `go test`.
- One `ci.yml` with a single required `check` job. Renovate `config:best-practices` with the Go toolchain group.
- A multi-arch GHCR image built on native runners. Tag `v<version>` starts the release. Conventional Commits.
- Gaps in the presets: Helm, base image, non-root, golangci-lint, zizmor. Each is a decision for this repo.
