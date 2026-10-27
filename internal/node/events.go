package node

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// ReasonDrainTimeout is the Event reason for uploads left in the Cache on stop.
const ReasonDrainTimeout = "DrainTimeout"

// Volume context keys kubelet adds with podInfoOnMount.
const (
	keyPodName      = "csi.storage.k8s.io/pod.name"
	keyPodNamespace = "csi.storage.k8s.io/pod.namespace"
	keyPodUID       = "csi.storage.k8s.io/pod.uid"
)

// workloadPod is the publishing pod from podInfoOnMount, or nil without it.
func workloadPod(vc map[string]string) *corev1.ObjectReference {
	name, ns := vc[keyPodName], vc[keyPodNamespace]
	if name == "" || ns == "" {
		return nil
	}
	return &corev1.ObjectReference{Kind: "Pod", APIVersion: "v1", Namespace: ns, Name: name, UID: types.UID(vc[keyPodUID])}
}

// drainTimeoutEvents targets the Mount Pod and the last workload pod that used it.
func (s *Server) drainTimeoutEvents(m *fuseMount) []corev1.Event {
	msg := fmt.Sprintf("Mount Pod %s did not drain its upload queue within %s; pending writes stay in the Cache",
		m.name, s.cfg.Pod.DrainTimeout)
	refs := []corev1.ObjectReference{{Kind: "Pod", APIVersion: "v1", Namespace: s.cfg.Pod.Namespace, Name: m.name, UID: m.podUID}}
	if m.lastPod != nil {
		refs = append(refs, *m.lastPod)
	}
	now := metav1.NewTime(s.deps.Clock.Now())
	out := make([]corev1.Event, 0, len(refs))
	for _, r := range refs {
		out = append(out, warningEvent(r, ReasonDrainTimeout, msg, s.cfg.NodeID, now))
	}
	return out
}

func warningEvent(ref corev1.ObjectReference, reason, msg, node string, now metav1.Time) corev1.Event {
	return corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{GenerateName: ref.Name + ".", Namespace: ref.Namespace},
		InvolvedObject: ref,
		Reason:         reason,
		Message:        msg,
		Type:           corev1.EventTypeWarning,
		Source:         corev1.EventSource{Component: "csi-driver-rclone-node", Host: node},
		FirstTimestamp: now,
		LastTimestamp:  now,
		Count:          1,
	}
}

func (s *Server) reportDrainTimeout(m *fuseMount) {
	s.deps.Log.Warn("mount pod drain timed out", "id", m.id, "pod", m.name, "timeout", s.cfg.Pod.DrainTimeout)
	s.emit(s.drainTimeoutEvents(m))
}
