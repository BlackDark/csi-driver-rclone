package node

import (
	"context"
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/BlackDark/csi-driver-rclone/internal/mountpod"
)

// ReasonMountLost is the Event reason for a Mount Pod that stopped unasked.
const ReasonMountLost = "MountLost"

// addUser records target as a user of m. Callers hold the <id> lock.
func (s *Server) addUser(ctx context.Context, m *fuseMount, target string, ref *corev1.ObjectReference) {
	if ref != nil {
		m.lastPod = ref
	}
	s.deps.Users.Add(m.id, target)
	s.mu.Lock()
	delete(s.dead, target)
	changed := false
	if ref != nil && !sameRef(s.refs[target], ref) {
		s.refs[target] = ref
		changed = true
	}
	s.mu.Unlock()
	if changed {
		s.syncUsers(ctx, m)
	}
}

func (s *Server) forgetTarget(target string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.dead, target)
	delete(s.refs, target)
}

func (s *Server) markDead(targets []string, reason string) []corev1.ObjectReference {
	s.mu.Lock()
	defer s.mu.Unlock()
	var refs []corev1.ObjectReference
	for _, t := range targets {
		s.dead[t] = reason
		if r := s.refs[t]; r != nil {
			refs = append(refs, *r)
		}
	}
	return refs
}

func (s *Server) deadReason(target string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.dead[target]
	return r, ok
}

// MountPodChanged is fed by the Mount Pod watch. A Mount Pod that fails, finishes or
// is deleted while the plugin did not stop it counts as lost.
func (s *Server) MountPodChanged(pod *corev1.Pod, deleted bool) {
	m := s.getMount(pod.Labels[mountpod.LabelID])
	if m == nil || m.podUID != pod.UID || m.releasing.Load() {
		return
	}
	ph := pod.Status.Phase
	if deleted || pod.DeletionTimestamp != nil || ph == corev1.PodFailed || ph == corev1.PodSucceeded {
		m.signalLost()
	}
}

// monitor waits for a ready mount to lose its Mount Pod and starts the recovery.
func (s *Server) monitor(m *fuseMount) {
	var reason string
	select {
	case <-m.gone:
		reason = "its handoff connection closed"
	case <-m.lost:
		reason = "the Mount Pod stopped or was deleted"
	case <-m.served:
		return
	}
	if m.releasing.Load() {
		return
	}
	s.bg.Add(1)
	go func() {
		defer s.bg.Done()
		s.recoverMount(m, reason)
	}()
}

// recoverMount reports the dead binds of a lost Mount Pod and replaces the Pod, so new
// publishes work. A running FUSE connection cannot be resumed (ADR 0003): existing
// binds stay dead until their pod restarts.
func (s *Server) recoverMount(m *fuseMount, reason string) {
	unlock := s.locks.lock(m.id)
	defer unlock()
	if s.getMount(m.id) != m || m.releasing.Load() {
		return
	}
	s.cancelLinger(m)
	m.releasing.Store(true)
	targets := s.deps.Users.Targets(m.id)
	msg := fmt.Sprintf("Mount Pod %s for volume %s is lost: %s. The volume is unreachable in this pod "+
		"(transport endpoint is not connected); restart the pod to remount it", m.name, m.volumeID, reason)
	s.deps.Log.Warn("mount pod lost", "id", m.id, "pod", m.name, "reason", reason, "targets", len(targets))
	refs := s.markDead(targets, msg)
	s.deps.Metrics.lost.Inc()
	s.emit(s.mountLostEvents(m, refs, msg))

	ctx, cancel := context.WithTimeout(context.Background(), s.teardownTimeout()+s.cfg.PublishTimeout+kubeCallTimeout)
	defer cancel()
	if err := s.deps.Kube.DeletePod(ctx, m.name); err != nil {
		s.deps.Log.Warn("delete lost mount pod", "id", m.id, "err", err)
	}
	s.finishTeardown(m)
	if len(targets) == 0 || m.spec == nil {
		return
	}
	if err := s.waitPodGone(ctx, m.name); err != nil {
		s.deps.Log.Warn("lost mount pod not gone; next publish recreates it", "id", m.id, "err", err)
		return
	}
	nm, err := s.ensureMount(ctx, m.id, m.volumeID, *m.spec)
	if err != nil {
		s.deps.Log.Warn("recreate mount pod; next publish retries", "id", m.id, "err", err)
		return
	}
	nm.lastPod = m.lastPod
	s.syncUsers(ctx, nm)
	s.deps.Log.Info("mount pod recreated", "id", m.id, "deadTargets", len(targets))
}

// waitPodGone polls until the named Mount Pod no longer exists: its name is reused.
func (s *Server) waitPodGone(ctx context.Context, name string) error {
	for {
		p, err := s.deps.Kube.GetPod(ctx, name)
		if err == nil && p == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.deps.Clock.After(time.Second):
		}
	}
}

func (s *Server) mountLostEvents(m *fuseMount, refs []corev1.ObjectReference, msg string) []corev1.Event {
	all := append([]corev1.ObjectReference{{Kind: "Pod", APIVersion: "v1", Namespace: s.cfg.Pod.Namespace, Name: m.name, UID: m.podUID}}, refs...)
	now := metav1.NewTime(s.deps.Clock.Now())
	out := make([]corev1.Event, 0, len(all))
	for _, r := range all {
		out = append(out, warningEvent(r, ReasonMountLost, msg, s.cfg.NodeID, now))
	}
	return out
}

func (s *Server) emit(events []corev1.Event) {
	ctx, cancel := context.WithTimeout(context.Background(), kubeCallTimeout)
	defer cancel()
	for _, e := range events {
		if err := s.deps.Kube.CreateEvent(ctx, &e); err != nil {
			s.deps.Log.Warn("emit event", "reason", e.Reason, "object", e.InvolvedObject.Name, "err", err)
		}
	}
}

// ReasonMountPodLost is the CSI health reason for a target whose Mount Pod is gone.
const ReasonMountPodLost = "MountPodLost"

const notConnected = "the Mount Pod is gone (transport endpoint is not connected); restart the pod to remount"

// NodeGetVolumeHealth reports a target Inaccessible when its Mount Pod was lost or the
// mount answers ENOTCONN; kubelet writes it to pod.status.volumeHealth. The publish
// path is optional in CSI; without one there is no target to judge, so no abnormality.
func (s *Server) NodeGetVolumeHealth(_ context.Context, req *csi.NodeGetVolumeHealthRequest) (*csi.NodeGetVolumeHealthResponse, error) {
	path := req.GetVolumePublishPath()
	if req.GetVolumeId() == "" {
		return nil, status.Error(codes.InvalidArgument, "volume id is required")
	}
	health := &csi.VolumeHealth{VolumeId: req.GetVolumeId()}
	if path == "" {
		return &csi.NodeGetVolumeHealthResponse{VolumeHealth: health}, nil
	}
	msg, dead := s.deadReason(path)
	if !dead {
		_, err := s.deps.Statfs(path)
		switch {
		case errors.Is(err, os.ErrNotExist):
			return nil, status.Errorf(codes.NotFound, "volume path %s not found", path)
		case errors.Is(err, syscall.ENOTCONN):
			msg, dead = notConnected, true
		case err != nil:
			return nil, status.Errorf(codes.Internal, "statfs %s: %v", path, err)
		}
	}
	if dead {
		health.HealthStatuses = []*csi.VolumeHealth_VolumeHealthEntry{{
			Status: csi.VolumeHealthErrorType_INACCESSIBLE, Reason: ReasonMountPodLost, Message: msg,
		}}
	}
	return &csi.NodeGetVolumeHealthResponse{VolumeHealth: health}, nil
}

// NodeGetVolumeStats reports statfs usage of a target; a dead target is Unavailable.
func (s *Server) NodeGetVolumeStats(_ context.Context, req *csi.NodeGetVolumeStatsRequest) (*csi.NodeGetVolumeStatsResponse, error) {
	path := req.GetVolumePath()
	if req.GetVolumeId() == "" || path == "" {
		return nil, status.Error(codes.InvalidArgument, "volume id and volume path are required")
	}
	u, err := s.deps.Statfs(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil, status.Errorf(codes.NotFound, "volume path %s not found", path)
	case errors.Is(err, syscall.ENOTCONN):
		return nil, status.Error(codes.Unavailable, notConnected)
	case err != nil:
		return nil, status.Errorf(codes.Internal, "statfs %s: %v", path, err)
	}
	return &csi.NodeGetVolumeStatsResponse{Usage: []*csi.VolumeUsage{
		{Unit: csi.VolumeUsage_BYTES, Total: u.TotalBytes, Available: u.AvailableBytes, Used: u.UsedBytes},
		{Unit: csi.VolumeUsage_INODES, Total: u.TotalInodes, Available: u.FreeInodes, Used: u.UsedInodes},
	}}, nil
}
