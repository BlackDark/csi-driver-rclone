package node

import (
	"github.com/prometheus/client_golang/prometheus"

	"github.com/BlackDark/csi-driver-rclone/internal/metrics"
)

// Mount Pod states in csi_rclone_mount_pods.
const (
	StateStarting  = "starting"
	StateReady     = "ready"
	StateLingering = "lingering"
	StateStopping  = "stopping"
)

var mountStates = []string{StateStarting, StateReady, StateLingering, StateStopping}

// Drain results in csi_rclone_drain_total; gone means the Mount Pod exited without a report.
const (
	DrainDrained = "drained"
	DrainTimeout = "timeout"
	DrainGone    = "gone"
)

// Adoption results in csi_rclone_adoptions_total.
const (
	AdoptAdopted = "adopted"
	AdoptCleaned = "cleaned"
)

// Metrics are the node plugin's event counters.
type Metrics struct {
	drain     *prometheus.CounterVec
	lost      prometheus.Counter
	adoptions *prometheus.CounterVec
}

// NewMetrics registers the counters on reg; a nil reg leaves them unregistered.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		drain: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: metrics.Namespace, Name: "drain_total", Help: "Mount Pod stops by drain result.",
		}, []string{"result"}),
		lost: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: metrics.Namespace, Name: "mount_lost_total", Help: "Mount Pods that stopped without the node plugin asking.",
		}),
		adoptions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: metrics.Namespace, Name: "adoptions_total", Help: "Mounts found by a node plugin restart, by result.",
		}, []string{"result"}),
	}
	for _, r := range []string{DrainDrained, DrainTimeout, DrainGone} {
		m.drain.WithLabelValues(r)
	}
	for _, r := range []string{AdoptAdopted, AdoptCleaned} {
		m.adoptions.WithLabelValues(r)
	}
	if reg != nil {
		reg.MustRegister(m.drain, m.lost, m.adoptions)
	}
	return m
}

var (
	descPods = prometheus.NewDesc(metrics.Namespace+"_mount_pods",
		"Mount Pods on this node by state.", []string{"state"}, nil)
	descInfo = prometheus.NewDesc(metrics.Namespace+"_mount_pod_info",
		"One per Mount Pod on this node, with its image.", []string{"id", "volume_id", "image"}, nil)
	descHealthy = prometheus.NewDesc(metrics.Namespace+"_mount_healthy",
		"1 when the Mount Pod serves its mount, 0 while starting or after it was lost.", []string{"id", "volume_id"}, nil)
)

// mountSample is what a scrape reads from one fuseMount.
type mountSample struct {
	id, volumeID, image, state string
	healthy                    bool
}

func (m *fuseMount) sample() mountSample {
	out := mountSample{id: m.id, volumeID: m.volumeID, image: m.image}
	ready := closed(m.ready)
	switch {
	case m.releasing.Load():
		out.state = StateStopping
	case !ready:
		out.state = StateStarting
	case m.lingering.Load():
		out.state = StateLingering
	default:
		out.state = StateReady
	}
	out.healthy = ready && !m.isGone() && !closed(m.lost)
	return out
}

func closed(c <-chan struct{}) bool {
	select {
	case <-c:
		return true
	default:
		return false
	}
}

// collectMounts turns samples into series; pure so tests need no Server.
func collectMounts(samples []mountSample, ch chan<- prometheus.Metric) {
	counts := map[string]int{}
	for _, s := range samples {
		counts[s.state]++
		ch <- prometheus.MustNewConstMetric(descInfo, prometheus.GaugeValue, 1, s.id, s.volumeID, s.image)
		h := 0.0
		if s.healthy {
			h = 1
		}
		ch <- prometheus.MustNewConstMetric(descHealthy, prometheus.GaugeValue, h, s.id, s.volumeID)
	}
	for _, st := range mountStates {
		ch <- prometheus.MustNewConstMetric(descPods, prometheus.GaugeValue, float64(counts[st]), st)
	}
}

// Collector reports the live mounts at scrape time.
func (s *Server) Collector() prometheus.Collector { return mountCollector{s} }

type mountCollector struct{ s *Server }

func (c mountCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- descPods
	ch <- descInfo
	ch <- descHealthy
}

func (c mountCollector) Collect(ch chan<- prometheus.Metric) {
	c.s.mu.Lock()
	samples := make([]mountSample, 0, len(c.s.mounts))
	for _, m := range c.s.mounts {
		samples = append(samples, m.sample())
	}
	c.s.mu.Unlock()
	collectMounts(samples, ch)
}
