# 01: Repo scaffold and check gate

**What to build:** A contributor runs `mise run check` (gofmt, go vet, golangci-lint, go test) on an empty Go module and CI runs the same as the single required `check` job, plus zizmor. AGENTS.md records the preset deviations (Helm, distroless, golangci-lint, zizmor, csi-sanity).

**Blocked by:** None (can start immediately)

**Status:** ready-for-agent

- [ ] Go module `github.com/BlackDark/csi-driver-rclone` with a binary exposing `node`, `controller` and `mount` modes, each exiting cleanly with a not-implemented error
- [ ] `mise.toml` with pinned tools; `mise run check` green locally
- [ ] `ci.yml` from presets with one `check` gate; zizmor job passes
- [ ] Renovate config from presets with Go toolchain group
- [ ] AGENTS.md and CLAUDE.md from presets with deviations listed
