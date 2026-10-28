package node

import (
	"context"
	"errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Event reasons of workload healing (--heal-workloads).
const (
	ReasonWorkloadHealed = "WorkloadHealed"
	ReasonHealBlocked    = "HealBlocked"
)

// Heal results in csi_rclone_workload_heals_total.
const (
	HealEvicted = "evicted"
	HealSkipped = "skipped"
	HealBlocked = "blocked"
)

// ErrEvictionBlocked means a PodDisruptionBudget refused the eviction (HTTP 429).
var ErrEvictionBlocked = errors.New("eviction blocked by a PodDisruptionBudget")

// ErrPodReplaced means the pod is gone or its UID changed, so it was replaced already.
var ErrPodReplaced = errors.New("pod already replaced")

const (
	healFirstBackoff = time.Second
	healMaxBackoff   = 30 * time.Second
	// healTimeout bounds the retries of one pod against a blocking PDB.
	healTimeout = 5 * time.Minute
)

type healAction int

const (
	healGone healAction = iota // pod gone or replaced: nothing to do
	healBare                   // no controller would recreate it
	healEvict
)

// healDecision judges one dead workload pod. pod is nil when it no longer exists.
func healDecision(ref corev1.ObjectReference, pod *corev1.Pod) healAction {
	switch {
	case pod == nil || pod.DeletionTimestamp != nil:
		return healGone
	case ref.UID != "" && pod.UID != ref.UID:
		return healGone
	case metav1.GetControllerOf(pod) == nil:
		return healBare
	}
	return healEvict
}

// nextBackoff doubles d up to healMaxBackoff.
func nextBackoff(d time.Duration) time.Duration {
	return min(2*d, healMaxBackoff)
}

// startHeal evicts the workload pods behind dead binds in the background, so their
// controllers recreate them and the replacements remount. No-op unless enabled.
func (s *Server) startHeal(refs []corev1.ObjectReference) {
	if !s.cfg.HealWorkloads || len(refs) == 0 {
		return
	}
	s.bg.Add(1)
	go func() {
		defer s.bg.Done()
		for _, r := range refs {
			s.healPod(r)
		}
	}()
}

func (s *Server) healPod(ref corev1.ObjectReference) {
	ctx, cancel := context.WithTimeout(context.Background(), healTimeout+kubeCallTimeout)
	defer cancel()
	pod, err := s.deps.Kube.GetWorkloadPod(ctx, ref.Namespace, ref.Name)
	if err != nil {
		s.deps.Log.Warn("heal: get workload pod", "pod", ref.Namespace+"/"+ref.Name, "err", err)
		s.healResult(ref, HealBlocked, corev1.EventTypeWarning, ReasonHealBlocked,
			fmt.Sprintf("Could not read the pod to heal it: %v; restart it manually", err))
		return
	}
	switch healDecision(ref, pod) {
	case healGone:
		return
	case healBare:
		s.healResult(ref, HealSkipped, corev1.EventTypeWarning, ReasonHealBlocked,
			"The pod has no controller to recreate it, so it is not evicted; restart it manually to remount the volume")
		return
	}
	ref.UID = pod.UID
	err = s.evictWithBackoff(ctx, ref)
	switch {
	case err == nil:
		s.healResult(ref, HealEvicted, corev1.EventTypeNormal, ReasonWorkloadHealed,
			"Evicted after its Mount Pod was lost, so its controller recreates it with a fresh mount")
	case errors.Is(err, ErrPodReplaced):
	default:
		s.healResult(ref, HealBlocked, corev1.EventTypeWarning, ReasonHealBlocked,
			fmt.Sprintf("Could not evict the pod after its Mount Pod was lost: %v; restart it manually", err))
	}
}

// evictWithBackoff retries an eviction a PDB blocks until healTimeout.
func (s *Server) evictWithBackoff(ctx context.Context, ref corev1.ObjectReference) error {
	deadline := s.deps.Clock.Now().Add(healTimeout)
	wait := healFirstBackoff
	for {
		err := s.deps.Kube.EvictPod(ctx, ref.Namespace, ref.Name, ref.UID)
		if !errors.Is(err, ErrEvictionBlocked) {
			return err
		}
		if !s.deps.Clock.Now().Add(wait).Before(deadline) {
			return err
		}
		s.deps.Log.Info("heal: eviction blocked, retrying", "pod", ref.Namespace+"/"+ref.Name, "in", wait)
		select {
		case <-ctx.Done():
			return err
		case <-s.deps.Clock.After(wait):
		}
		wait = nextBackoff(wait)
	}
}

func (s *Server) healResult(ref corev1.ObjectReference, result, typ, reason, msg string) {
	s.deps.Metrics.heals.WithLabelValues(result).Inc()
	s.deps.Log.Info("heal workload pod", "pod", ref.Namespace+"/"+ref.Name, "result", result)
	e := newEvent(ref, typ, reason, msg, s.cfg.NodeID, metav1.NewTime(s.deps.Clock.Now()))
	s.emit([]corev1.Event{e})
}
