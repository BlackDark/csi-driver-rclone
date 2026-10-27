package node

import (
	"context"
	"encoding/json"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/BlackDark/csi-driver-rclone/internal/mountpod"
)

// podRef is one workload pod in the users annotation.
type podRef struct {
	Namespace string    `json:"ns"`
	Name      string    `json:"name"`
	UID       types.UID `json:"uid,omitempty"`
}

func sameRef(a, b *corev1.ObjectReference) bool {
	return a != nil && b != nil && a.Namespace == b.Namespace && a.Name == b.Name && a.UID == b.UID
}

// encodeUsers renders the users annotation: target path -> workload pod. Empty when
// no target has a pod ref.
func encodeUsers(targets []string, refs map[string]*corev1.ObjectReference) string {
	out := map[string]podRef{}
	for _, t := range targets {
		if r := refs[t]; r != nil {
			out[t] = podRef{Namespace: r.Namespace, Name: r.Name, UID: r.UID}
		}
	}
	if len(out) == 0 {
		return ""
	}
	b, err := json.Marshal(out)
	if err != nil {
		panic(err) // strings only: cannot fail
	}
	return string(b)
}

// decodeUsers parses the users annotation; a missing or malformed one yields none.
func decodeUsers(v string) map[string]*corev1.ObjectReference {
	var in map[string]podRef
	if v == "" || json.Unmarshal([]byte(v), &in) != nil {
		return nil
	}
	out := make(map[string]*corev1.ObjectReference, len(in))
	for t, r := range in {
		out[t] = &corev1.ObjectReference{Kind: "Pod", APIVersion: "v1", Namespace: r.Namespace, Name: r.Name, UID: r.UID}
	}
	return out
}

// syncUsers writes m's current users onto its Mount Pod, for Adopt after a restart.
// Callers hold the <id> lock. A failure only costs Events after a restart.
func (s *Server) syncUsers(ctx context.Context, m *fuseMount) {
	targets := s.deps.Users.Targets(m.id)
	s.mu.Lock()
	v := encodeUsers(targets, s.refs)
	s.mu.Unlock()
	if err := s.deps.Kube.AnnotatePod(ctx, m.name, mountpod.AnnotationUsers, v); err != nil {
		s.deps.Log.Warn("record mount users", "id", m.id, "err", err)
	}
}

// restoreRefs takes the refs of targets from a Mount Pod's users annotation.
func (s *Server) restoreRefs(pod *corev1.Pod, targets []string) *corev1.ObjectReference {
	saved := decodeUsers(pod.Annotations[mountpod.AnnotationUsers])
	s.mu.Lock()
	defer s.mu.Unlock()
	var last *corev1.ObjectReference
	for _, t := range targets {
		if r := saved[t]; r != nil {
			s.refs[t] = r
			last = r
		}
	}
	return last
}
