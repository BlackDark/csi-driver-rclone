package node

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/mount-utils"

	"github.com/BlackDark/csi-driver-rclone/internal/mountpod"
	"github.com/BlackDark/csi-driver-rclone/internal/mountspec"
)

const ephemeralKey = "csi.storage.k8s.io/ephemeral"

// NodePublishVolume ensures the Mount Pod for (volume, node) and bind-mounts it on the target.
func (s *Server) NodePublishVolume(ctx context.Context, req *csi.NodePublishVolumeRequest) (*csi.NodePublishVolumeResponse, error) {
	readOnly, err := validatePublish(req)
	if err != nil {
		return nil, err
	}
	spec, err := mountspec.Resolve(mountspec.Input{
		Secrets:       req.GetSecrets(),
		VolumeContext: req.GetVolumeContext(),
		VolumeID:      req.GetVolumeId(),
		ReadOnly:      readOnly,
		Mode:          mountspec.Persistent,
	})
	if err != nil {
		return nil, specError(err)
	}
	id := mountpod.ID(spec.Identity, s.cfg.NodeID)
	unlock := s.locks.lock(id)
	defer unlock()

	target := req.GetTargetPath()
	mounted, err := s.prepareTarget(target)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "target %s: %v", target, err)
	}
	if m := s.getMount(id); m != nil && mounted {
		s.cancelLinger(m)
		s.addUser(m, target, workloadPod(req.GetVolumeContext()))
		return &csi.NodePublishVolumeResponse{}, nil
	}
	m, err := s.ensureMount(ctx, id, req.GetVolumeId(), spec)
	if err != nil {
		return nil, err
	}
	opts := []string{"bind"}
	if readOnly {
		opts = append(opts, "ro")
	}
	if err := s.deps.Mounter.Mount(m.fuseDir, target, "", opts); err != nil {
		return nil, status.Errorf(codes.Internal, "bind mount %s: %v", target, err)
	}
	s.addUser(m, target, workloadPod(req.GetVolumeContext()))
	return &csi.NodePublishVolumeResponse{}, nil
}

func validatePublish(req *csi.NodePublishVolumeRequest) (readOnly bool, err error) {
	switch {
	case req.GetVolumeId() == "":
		return false, status.Error(codes.InvalidArgument, "volume id missing")
	case req.GetTargetPath() == "":
		return false, status.Error(codes.InvalidArgument, "target path missing")
	case req.GetVolumeCapability() == nil:
		return false, status.Error(codes.InvalidArgument, "volume capability missing")
	case req.GetVolumeCapability().GetMount() == nil:
		return false, status.Error(codes.InvalidArgument, "only mount access type is supported")
	case req.GetVolumeContext()[ephemeralKey] == "true":
		return false, status.Error(codes.InvalidArgument, "inline volumes are not supported yet")
	}
	mode := req.GetVolumeCapability().GetAccessMode().GetMode()
	readerOnly := mode == csi.VolumeCapability_AccessMode_SINGLE_NODE_READER_ONLY ||
		mode == csi.VolumeCapability_AccessMode_MULTI_NODE_READER_ONLY
	return req.GetReadonly() || readerOnly, nil
}

// specError keeps mountspec messages, which name keys but never values.
func specError(err error) error {
	if errors.Is(err, mountspec.ErrInvalid) {
		return status.Error(codes.InvalidArgument, err.Error())
	}
	return status.Errorf(codes.Internal, "resolve mount spec: %v", err)
}

// prepareTarget creates the target dir and reports whether it is already a healthy
// mount. A corrupted (stale) mount is unmounted so it can be republished.
func (s *Server) prepareTarget(target string) (bool, error) {
	notMnt, err := s.deps.Mounter.IsLikelyNotMountPoint(target)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return false, os.MkdirAll(target, 0o750)
	case mount.IsCorruptedMnt(err):
		return false, s.deps.Mounter.Unmount(target)
	case err != nil:
		return false, err
	}
	return !notMnt, nil
}

// ensureMount returns a ready mount for id, starting the FUSE mount, the handoff
// server, the Mount Pod and its Secret when needed. Failures clean up after themselves.
func (s *Server) ensureMount(ctx context.Context, id, volumeID string, spec mountspec.Spec) (*fuseMount, error) {
	if m := s.getMount(id); m != nil {
		if !m.isGone() {
			s.cancelLinger(m)
			return m, nil
		}
		// Mount Pod died and recovery has not replaced it yet.
		s.abort(m)
	}
	pod, secret, err := mountpod.Build(mountpod.Input{Config: s.cfg.Pod, NodeName: s.cfg.NodeID, VolumeID: volumeID, Spec: spec})
	if err != nil {
		return nil, specError(err)
	}
	if spec.CacheVolume == "" {
		dir := mountpod.CacheDir(s.cfg.Pod.CacheRoot, volumeID)
		if err := s.deps.PrepareDir(dir, int(s.cfg.Pod.UID), int(s.cfg.Pod.GID)); err != nil {
			return nil, status.Errorf(codes.Internal, "prepare cache dir: %v", err)
		}
	}
	m, err := s.startMount(id, volumeID, spec)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "fuse mount: %v", err)
	}
	s.setMount(id, m)
	created, err := s.deps.Kube.CreatePod(ctx, &pod)
	if err == nil && created.DeletionTimestamp != nil {
		err = fmt.Errorf("mount pod %s is still terminating", pod.Name)
	}
	if err == nil {
		m.podUID = created.UID
		owned := mountpod.OwnedBy(secret, created)
		err = s.deps.Kube.CreateSecret(ctx, &owned)
	}
	if err == nil {
		err = s.waitReady(ctx, m)
	}
	if err != nil {
		s.abort(m)
		return nil, publishError(err)
	}
	go s.monitor(m)
	return m, nil
}

func publishError(err error) error {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return status.Errorf(codes.DeadlineExceeded, "mount pod not ready: %v", err)
	case errors.Is(err, context.Canceled):
		return status.Errorf(codes.Canceled, "publish canceled: %v", err)
	}
	return status.Errorf(codes.Unavailable, "mount pod: %v", err)
}

// abort removes a mount that never became ready or whose Mount Pod died.
func (s *Server) abort(m *fuseMount) {
	s.cancelLinger(m)
	m.releasing.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), kubeCallTimeout)
	defer cancel()
	if err := s.deps.Kube.DeletePod(ctx, m.name); err != nil {
		s.deps.Log.Warn("delete mount pod", "id", m.id, "err", err)
	}
	if err := s.deps.Kube.DeleteSecret(ctx, m.name); err != nil {
		s.deps.Log.Warn("delete mount secret", "id", m.id, "err", err)
	}
	s.stopMount(m)
}
