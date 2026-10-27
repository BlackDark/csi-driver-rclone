<div align="center">

# csi-driver-rclone

**Kubernetes CSI driver for rclone: mount S3, WebDAV, SFTP and other rclone remotes as volumes.**

[![CI](https://github.com/BlackDark/csi-driver-rclone/actions/workflows/ci.yml/badge.svg)](https://github.com/BlackDark/csi-driver-rclone/actions/workflows/ci.yml)

</div>

## Status

Pre-alpha: nothing works yet. What v1 covers: [docs/scope.md](docs/scope.md).

## Development

Requires [mise](https://mise.jdx.dev).

```bash
git clone https://github.com/BlackDark/csi-driver-rclone && cd csi-driver-rclone
mise install
mise run check
```
