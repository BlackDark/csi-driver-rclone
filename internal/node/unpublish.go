package node

import (
	"context"
	"os"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/mount-utils"

	"github.com/BlackDark/csi-driver-rclone/internal/mountpod"
	"github.com/BlackDark/csi-driver-rclone/internal/mountspec"
)

const kubeCallTimeout = 30 * time.Second

// NodeUnpublishVolume unmounts the target. The last user on this node releases the mount.
func (s *Server) NodeUnpublishVolume(ctx context.Context, req *csi.NodeUnpublishVolumeRequest) (*csi.NodeUnpublishVolumeResponse, error) {
	target := req.GetTargetPath()
	if req.GetVolumeId() == "" || target == "" {
		return nil, status.Error(codes.InvalidArgument, "volume id and target path are required")
	}
	if err := mount.CleanupMountPoint(target, s.deps.Mounter, true); err != nil {
		return nil, status.Errorf(codes.Internal, "unmount %s: %v", target, err)
	}
	s.forgetTarget(target)
	id, ok := s.deps.Users.Lookup(target)
	if !ok {
		return &csi.NodeUnpublishVolumeResponse{}, nil
	}
	unlock := s.locks.lock(id)
	m := s.getMount(id)
	remaining := s.deps.Users.Remove(target)
	if m != nil {
		s.syncUsers(ctx, m)
	}
	if remaining > 0 || m == nil {
		unlock()
		return &csi.NodeUnpublishVolumeResponse{}, nil
	}
	if err := s.startLinger(ctx, m, unlock); err != nil {
		return nil, status.Errorf(codes.Internal, "release mount: %v", err)
	}
	return &csi.NodeUnpublishVolumeResponse{}, nil
}

// release deletes the Mount Pod. It owns unlock; the rest of the teardown runs in the
// background under the lock.
func (s *Server) release(ctx context.Context, m *fuseMount, unlock func()) error {
	m.releasing.Store(true)
	if err := s.deps.Kube.DeletePod(ctx, m.name); err != nil {
		m.releasing.Store(false)
		unlock()
		return err
	}
	s.bg.Add(1)
	go func() {
		defer s.bg.Done()
		defer unlock()
		if !s.finishTeardown(m) {
			s.removeEphemeralCache(m)
		}
	}()
	return nil
}

// finishTeardown waits for the Mount Pod's drain result (or its exit), then unmounts
// fuse/, which lets rclone exit. The Secret is GC'd with the Pod; deleting it is a
// fallback. It reports whether the drain timed out.
func (s *Server) finishTeardown(m *fuseMount) bool {
	t := time.NewTimer(s.teardownTimeout())
	defer t.Stop()
	timedOut := false
	select {
	case <-m.drained:
		timedOut = m.drainTimedOut
		s.deps.Log.Info("mount pod drain result", "id", m.id, "timedOut", timedOut)
	case <-m.gone:
	case <-m.served:
	case <-t.C:
		timedOut = true
		s.deps.Log.Warn("mount pod drain result not received", "id", m.id)
	}
	if timedOut {
		s.reportDrainTimeout(m)
	}
	s.stopMount(m)
	ctx, cancel := context.WithTimeout(context.Background(), kubeCallTimeout)
	defer cancel()
	if err := s.deps.Kube.DeleteSecret(ctx, m.name); err != nil {
		s.deps.Log.Warn("delete mount secret", "id", m.id, "err", err)
	}
	return timedOut
}

// removeEphemeralCache deletes the hostPath Cache of a released Inline Volume: its
// volume id is per pod and never comes back. A timed-out drain keeps it, since
// pending writes are still in there.
func (s *Server) removeEphemeralCache(m *fuseMount) {
	if m.spec == nil || m.spec.Mode != mountspec.Ephemeral || m.spec.CacheVolume != "" || m.volumeID == "" {
		return
	}
	dir := mountpod.CacheDir(s.cfg.Pod.CacheRoot, m.volumeID)
	if err := os.RemoveAll(dir); err != nil {
		s.deps.Log.Warn("remove inline volume cache", "id", m.id, "dir", dir, "err", err)
	}
}
