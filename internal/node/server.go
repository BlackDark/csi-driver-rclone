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
}

// Deps are the side effects, injected so tests can fake them.
type Deps struct {
	Kube       Kube
	Mounter    mount.Interface
	Fuse       FuseMounter
	Handoff    HandoffServer
	Users      Users
	PrepareDir DirPreparer
	Clock      clock.WithDelayedExecution
	Log        *slog.Logger
}

// Server is the CSI Node service.
type Server struct {
	csi.UnimplementedNodeServer
	cfg  Config
	deps Deps

	locks keyedLock
	mu    sync.Mutex
	// mounts are the live FUSE mounts by <id>. Restart adoption (ticket 06) will
	// rebuild this from Mount Pods and the mount table.
	mounts map[string]*fuseMount
	bg     sync.WaitGroup
}

// New returns a Server; nil Users, PrepareDir or Clock get the defaults.
func New(cfg Config, deps Deps) *Server {
	if deps.Users == nil {
		deps.Users = NewUsers()
	}
	if deps.PrepareDir == nil {
		deps.PrepareDir = PrepareDir
	}
	if deps.Clock == nil {
		deps.Clock = clock.RealClock{}
	}
	return &Server{cfg: cfg, deps: deps, mounts: map[string]*fuseMount{}}
}

// Wait blocks until pending Lingers and background teardowns have finished.
func (s *Server) Wait() { s.bg.Wait() }

// NodeGetCapabilities lists none yet; VOLUME_CONDITION and GET_VOLUME_STATS come in 06/10.
func (s *Server) NodeGetCapabilities(context.Context, *csi.NodeGetCapabilitiesRequest) (*csi.NodeGetCapabilitiesResponse, error) {
	return &csi.NodeGetCapabilitiesResponse{}, nil
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
