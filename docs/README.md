# Docs

Users:
- [Scope](scope.md): what is possible, what is not, and why.
- [Volume parameters](reference/volume-parameters.md): Credential Secret keys, Mount Options, Inline and Dynamic Volume rules.
- [Operations](operations.md): Mount Pod resources and memory sizing, upgrades.
- [Metrics](reference/metrics.md): series, ports, monitors, dashboard.
- [Migrate from systemd](guides/migrate-from-systemd.md): host `rclone mount` + hostPath PV to a Static Volume.
- [Glossary](../GLOSSARY.md).

Design:
- [Mount Pod contract](design/mount-pod-contract.md): names, paths, fd handoff, adoption, health.
- ADRs: [0001 shared Mount Pod](adr/0001-shared-mount-pod-per-volume-and-node.md), [0002 Mount Pod privilege](adr/0002-mount-pod-privilege.md), [0003 crash recovery](adr/0003-mount-pod-crash-recovery.md).
