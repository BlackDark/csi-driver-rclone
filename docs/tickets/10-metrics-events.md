# 10: Metrics, events and monitors

**What to build:** Prometheus metrics for CSI RPCs, Mount Pod count by state and version, mount health and drain outcome from the node plugin and controller. Per-volume VFS and transfer stats come from each Mount Pod, with no rc server. Events for mount failures, drain timeouts and Unrestricted Options use. The chart has an optional ServiceMonitor, PodMonitor and Grafana dashboard.

**Blocked by:** 05

**Status:** ready-for-agent

- [ ] e2e scrapes each endpoint and finds the documented series
- [ ] The dashboard renders against the e2e metrics; probes are on every component
