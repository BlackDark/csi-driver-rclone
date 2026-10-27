package node

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
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

// watch turns handoff events into state; only the connection that got the fd counts.
func (m *fuseMount) watch(events <-chan handoff.Event) {
	var fdConn uint64
	for {
		select {
		case <-m.served:
			return
		case e := <-events:
			switch e.Kind {
			case handoff.EventFDSent:
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

// startMount mounts fuse/ and serves its fd on handoff.sock.
func (s *Server) startMount(id string, spec mountspec.Spec) (*fuseMount, error) {
	kd := s.cfg.Pod.KubeletDir
	m := &fuseMount{
		id: id, name: mountpod.Name(id),
		mountDir: mountpod.MountDir(kd, id), fuseDir: mountpod.FuseDir(kd, id),
		ready: make(chan struct{}), drained: make(chan struct{}), gone: make(chan struct{}),
		served: make(chan struct{}),
	}
	uid, gid := int(s.cfg.Pod.UID), int(s.cfg.Pod.GID)
	if err := s.deps.PrepareDir(m.mountDir, uid, gid); err != nil {
		return nil, fmt.Errorf("prepare mount dir: %w", err)
	}
	// No live mount for id is known, so a mount on fuse/ is a dead leftover of a
	// plugin restart; it would fail mkdir with EEXIST. Adoption (ticket 06) replaces this.
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
	ctx, cancel := context.WithCancel(context.Background())
	m.stopServe = cancel
	events := make(chan handoff.Event, 16)
	cfg := handoff.ServerConfig{SockPath: mountpod.SockPath(kd, id), FD: fd, Mode: handoff.ModeFresh, UID: uid, GID: gid}
	go func() {
		defer close(m.served)
		if err := s.deps.Handoff.Serve(ctx, cfg, events); err != nil {
			s.deps.Log.Error("handoff server", "id", id, "err", err)
		}
	}()
	go m.watch(events)
	return m, nil
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
