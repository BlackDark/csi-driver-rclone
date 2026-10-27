package node

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sys/unix"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/clock"

	"github.com/BlackDark/csi-driver-rclone/internal/fusemount"
	"github.com/BlackDark/csi-driver-rclone/internal/handoff"
	"github.com/BlackDark/csi-driver-rclone/internal/mountpod"
	"github.com/BlackDark/csi-driver-rclone/internal/mountspec"
)

var (
	errMountPodGone  = errors.New("mount pod disconnected")
	errHandoffFailed = errors.New("handoff server stopped")
)

// fuseMount is one FUSE mount served to one Mount Pod over its handoff socket.
type fuseMount struct {
	id, name, fuseDir, mountDir string
	fd                          int // plugin's copy; closed once the Mount Pod is ready

	ready, drained, gone chan struct{}
	readyOnce, drainOnce sync.Once
	goneOnce             sync.Once
	stopServe            context.CancelFunc
	served               chan struct{} // closed when Serve returned
	drainTimedOut        bool          // set before drained is closed
	lost                 chan struct{} // closed when the Pod watch sees the Mount Pod stop unasked
	lostOnce             sync.Once
	releasing            atomic.Bool // set while the plugin itself stops the Mount Pod

	// spec and volumeID let a lost Mount Pod be recreated; spec is nil when unknown.
	spec     *mountspec.Spec
	volumeID string

	// Guarded by the <id> lock.
	podUID    types.UID
	lastPod   *corev1.ObjectReference // last workload pod published, target of drain Events
	linger    clock.Timer
	lingerGen uint64
}

func (m *fuseMount) isGone() bool {
	select {
	case <-m.gone:
		return true
	case <-m.served:
		return true
	default:
		return false
	}
}

// signalLost reports that the Mount Pod stopped without the plugin asking.
func (m *fuseMount) signalLost() { m.lostOnce.Do(func() { close(m.lost) }) }

// watch turns handoff events into state; only the connection that got the fd, or
// that adopted the mount, counts.
func (m *fuseMount) watch(events <-chan handoff.Event) {
	var fdConn uint64
	for {
		select {
		case <-m.served:
			return
		case e := <-events:
			switch e.Kind {
			case handoff.EventFDSent, handoff.EventAdopted:
				fdConn = e.Conn
			case handoff.EventReady:
				m.readyOnce.Do(func() { close(m.ready) })
			case handoff.EventDrained:
				m.drainOnce.Do(func() { close(m.drained) })
			case handoff.EventDrainTimeout:
				m.drainOnce.Do(func() { m.drainTimedOut = true; close(m.drained) })
			case handoff.EventClosed:
				if e.Conn == fdConn {
					m.goneOnce.Do(func() { close(m.gone) })
				}
			}
		}
	}
}

// fuseOpts: the shared mount is writable unless the Mount Spec says read-only;
// per-publish read-only is a remount of the bind.
func (s *Server) fuseOpts(spec mountspec.Spec) fusemount.Opts {
	return fusemount.Opts{
		Source:             spec.Remote.FsPath,
		ReadOnly:           spec.Options["read-only"] == "true",
		UID:                int(s.cfg.Pod.UID),
		GID:                int(s.cfg.Pod.GID),
		DefaultPermissions: spec.Options["default-permissions"] == "true",
	}
}

// newFuseMount is the in-memory state of mount id, not yet served.
func (s *Server) newFuseMount(id string) *fuseMount {
	kd := s.cfg.Pod.KubeletDir
	return &fuseMount{
		id: id, name: mountpod.Name(id), fd: -1,
		mountDir: mountpod.MountDir(kd, id), fuseDir: mountpod.FuseDir(kd, id),
		ready: make(chan struct{}), drained: make(chan struct{}), gone: make(chan struct{}),
		served: make(chan struct{}), lost: make(chan struct{}),
	}
}

// startMount mounts fuse/ and serves its fd on handoff.sock.
func (s *Server) startMount(id, volumeID string, spec mountspec.Spec) (*fuseMount, error) {
	m := s.newFuseMount(id)
	m.spec, m.volumeID = &spec, volumeID
	if err := s.deps.PrepareDir(m.mountDir, int(s.cfg.Pod.UID), int(s.cfg.Pod.GID)); err != nil {
		return nil, fmt.Errorf("prepare mount dir: %w", err)
	}
	// Adoption at start took over every live mount, so a mount still on fuse/ is dead
	// (its Mount Pod is gone); it would fail mkdir with EEXIST.
	if err := s.deps.Fuse.Unmount(m.fuseDir, true); err == nil {
		s.deps.Log.Warn("unmounted stale fuse mount", "id", id)
	}
	if err := os.MkdirAll(m.fuseDir, 0o750); err != nil {
		return nil, fmt.Errorf("create fuse dir: %w", err)
	}
	fd, err := s.deps.Fuse.Mount(m.fuseDir, s.fuseOpts(spec))
	if err != nil {
		return nil, err
	}
	m.fd = fd
	s.serve(m, handoff.ModeFresh)
	return m, nil
}

// serve runs m's handoff server until stopMount; ModeAdopt serves no fd.
func (s *Server) serve(m *fuseMount, mode handoff.ServerMode) {
	ctx, cancel := context.WithCancel(context.Background())
	m.stopServe = cancel
	events := make(chan handoff.Event, 16)
	cfg := handoff.ServerConfig{
		SockPath: mountpod.SockPath(s.cfg.Pod.KubeletDir, m.id), FD: m.fd, Mode: mode,
		UID: int(s.cfg.Pod.UID), GID: int(s.cfg.Pod.GID),
	}
	go func() {
		defer close(m.served)
		if err := s.deps.Handoff.Serve(ctx, cfg, events); err != nil {
			s.deps.Log.Error("handoff server", "id", m.id, "err", err)
		}
	}()
	go m.watch(events)
}

// waitReady blocks until the Mount Pod serves the mount, bounded by the publish timeout.
func (s *Server) waitReady(ctx context.Context, m *fuseMount) error {
	t := time.NewTimer(s.cfg.PublishTimeout)
	defer t.Stop()
	select {
	case <-m.ready:
		s.closeFD(m)
		return nil
	case <-m.gone:
		return errMountPodGone
	case <-m.served:
		return errHandoffFailed
	case <-t.C:
		return context.DeadlineExceeded
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Server) closeFD(m *fuseMount) {
	if m.fd >= 0 {
		_ = unix.Close(m.fd)
		m.fd = -1
	}
}

// stopMount lazily unmounts fuse/ and stops the handoff server. It is the last step
// of a teardown and of a failed publish.
func (s *Server) stopMount(m *fuseMount) {
	if err := s.deps.Fuse.Unmount(m.fuseDir, true); err != nil {
		s.deps.Log.Warn("unmount fuse", "id", m.id, "err", err)
	}
	s.closeFD(m)
	m.stopServe()
	<-m.served
	_ = os.Remove(m.fuseDir)
	_ = os.Remove(m.mountDir)
	s.setMount(m.id, nil)
}
