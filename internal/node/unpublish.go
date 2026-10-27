package node

import (
	"context"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/mount-utils"
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
	if s.deps.Users.Remove(target) > 0 || m == nil {
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
		s.finishTeardown(m)
	}()
	return nil
}

// finishTeardown waits for the Mount Pod's drain result (or its exit), then unmounts
// fuse/, which lets rclone exit. The Secret is GC'd with the Pod; deleting it is a fallback.
func (s *Server) finishTeardown(m *fuseMount) {
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
}
