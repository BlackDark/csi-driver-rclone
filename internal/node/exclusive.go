package node

import (
	"context"
	"fmt"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/BlackDark/csi-driver-rclone/internal/mountpod"
)

// ReasonUnrestrictedOptions is the Event reason for a publish with Unrestricted Options.
const ReasonUnrestrictedOptions = "UnrestrictedOptions"

// volumeLockKey cannot collide with an <id>, which is hex.
func volumeLockKey(volumeID string) string { return "volume/" + volumeID }

// checkExclusive refuses a second <id> of a volume on this node. Both would share the
// volume's Cache dir, and two rclone processes on one VFS cache corrupt it. Callers
// hold the volume lock. Unavailable is retried by kubelet: it succeeds once the other
// <id> is released (its pods left and its Linger expired).
func (s *Server) checkExclusive(ctx context.Context, id, volumeID string) error {
	if other := s.otherMount(id, volumeID); other != "" {
		return busyError(volumeID, other)
	}
	sel := labels.SelectorFromSet(labels.Set{
		mountpod.LabelName: mountpod.AppName, mountpod.LabelComponent: mountpod.Component,
		mountpod.LabelNode: s.cfg.NodeID, mountpod.LabelVolume: mountpod.CacheKey(volumeID),
	}).String()
	pods, err := s.deps.Kube.ListPods(ctx, sel)
	if err != nil {
		return status.Errorf(codes.Unavailable, "list mount pods of volume %s: %v", volumeID, err)
	}
	for _, p := range pods {
		if other := p.Labels[mountpod.LabelID]; other != id {
			return busyError(volumeID, other)
		}
	}
	return nil
}

func (s *Server) otherMount(id, volumeID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for other, m := range s.mounts {
		if other != id && m.volumeID == volumeID {
			return other
		}
	}
	return ""
}

func busyError(volumeID, other string) error {
	return status.Errorf(codes.Unavailable, "volume %s already has Mount Pod %s on this node with a different "+
		"Mount Spec; it owns the volume's Cache until its pods leave and its Linger expires",
		volumeID, mountpod.Name(other))
}

func (s *Server) unrestrictedEvent(ref corev1.ObjectReference, volumeID string) corev1.Event {
	msg := fmt.Sprintf("volume %s uses Unrestricted Options (allowAllOptions): its Mount Options bypass the allowlist", volumeID)
	return warningEvent(ref, ReasonUnrestrictedOptions, msg, s.cfg.NodeID, metav1.NewTime(s.deps.Clock.Now()))
}
