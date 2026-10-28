// Package node implements the CSI Node service: it runs one Mount Pod per (volume,
// node) and bind-mounts its FUSE mount into workload targets
// (docs/design/mount-pod-contract.md).
package node

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/mount-utils"
	"k8s.io/utils/clock"

	"github.com/BlackDark/csi-driver-rclone/internal/mountpod"
)

// Config is the node plugin's settings.
type Config struct {
	NodeID         string
	Pod            mountpod.Config
	PublishTimeout time.Duration
	// Linger keeps an unused Mount Pod up this long; zero or less stops it at once.
	Linger time.Duration
	// HealWorkloads evicts controller-owned workload pods whose Mount Pod was lost.
	HealWorkloads bool
}

// Deps are the side effects, injected so tests can fake them.
type Deps struct {
	Kube       Kube
	Mounter    mount.Interface
	Fuse       FuseMounter
	Handoff    HandoffServer
	Users      Users
	PrepareDir DirPreparer
	// MountInfo reads this process's mount table; Statfs reports a target's usage.
	MountInfo func() ([]mount.MountInfo, error)
	Statfs    func(path string) (Usage, error)
	Clock     clock.WithDelayedExecution
	Log       *slog.Logger
	// Metrics are the event counters; nil gets an unregistered set.
	Metrics *Metrics
}

// Server is the CSI Node service.
type Server struct {
	csi.UnimplementedNodeServer
	cfg  Config
	deps Deps

	locks keyedLock
	mu    sync.Mutex
	// mounts are the live FUSE mounts by <id>; Adopt rebuilds them after a restart.
	mounts map[string]*fuseMount
	// dead are targets whose bind outlived its Mount Pod, with the reason; refs are
	// the workload pods behind targets (podInfoOnMount), for Events. Guarded by mu.
	dead map[string]string
	refs map[string]*corev1.ObjectReference
	bg   sync.WaitGroup
}

// New returns a Server; nil Users, PrepareDir, MountInfo, Statfs, Clock or Metrics get the defaults.
func New(cfg Config, deps Deps) *Server {
	if deps.Users == nil {
		deps.Users = NewUsers()
	}
	if deps.PrepareDir == nil {
		deps.PrepareDir = PrepareDir
	}
	if deps.MountInfo == nil {
		deps.MountInfo = func() ([]mount.MountInfo, error) { return mount.ParseMountInfo("/proc/self/mountinfo") }
	}
	if deps.Statfs == nil {
		deps.Statfs = Statfs
	}
	if deps.Clock == nil {
		deps.Clock = clock.RealClock{}
	}
	if deps.Metrics == nil {
		deps.Metrics = NewMetrics(nil)
	}
	return &Server{cfg: cfg, deps: deps, mounts: map[string]*fuseMount{},
		dead: map[string]string{}, refs: map[string]*corev1.ObjectReference{}}
}

// Wait blocks until pending Lingers and background teardowns have finished.
func (s *Server) Wait() { s.bg.Wait() }

// NodeGetCapabilities lists volume stats and volume health (CSI 1.13's successor of
// VOLUME_CONDITION).
func (s *Server) NodeGetCapabilities(context.Context, *csi.NodeGetCapabilitiesRequest) (*csi.NodeGetCapabilitiesResponse, error) {
	caps := []csi.NodeServiceCapability_RPC_Type{
		csi.NodeServiceCapability_RPC_GET_VOLUME_STATS,
		csi.NodeServiceCapability_RPC_GET_VOLUME_HEALTH,
	}
	out := make([]*csi.NodeServiceCapability, 0, len(caps))
	for _, c := range caps {
		out = append(out, &csi.NodeServiceCapability{Type: &csi.NodeServiceCapability_Rpc{
			Rpc: &csi.NodeServiceCapability_RPC{Type: c},
		}})
	}
	return &csi.NodeGetCapabilitiesResponse{Capabilities: out}, nil
}

// NodeGetInfo returns the node id; there is no topology or volume limit.
func (s *Server) NodeGetInfo(context.Context, *csi.NodeGetInfoRequest) (*csi.NodeGetInfoResponse, error) {
	return &csi.NodeGetInfoResponse{NodeId: s.cfg.NodeID}, nil
}

func (s *Server) teardownTimeout() time.Duration {
	return s.cfg.Pod.DrainTimeout + s.cfg.Pod.UnmountWait + 10*time.Second
}

func (s *Server) getMount(id string) *fuseMount {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mounts[id]
}

func (s *Server) setMount(id string, m *fuseMount) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m == nil {
		delete(s.mounts, id)
		return
	}
	s.mounts[id] = m
}
