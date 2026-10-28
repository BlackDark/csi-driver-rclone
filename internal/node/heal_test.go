package node

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/mount-utils"

	"github.com/BlackDark/csi-driver-rclone/internal/handoff"
	"github.com/BlackDark/csi-driver-rclone/internal/mountpod"
)

func ownedPod(uid types.UID) *corev1.Pod {
	yes := true
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Namespace: "app", Name: "web-0", UID: uid,
		OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "web", UID: "rs", Controller: &yes}},
	}}
}

func TestHealDecision(t *testing.T) {
	ref := corev1.ObjectReference{Namespace: "app", Name: "web-0", UID: "u1"}
	bare := ownedPod("u1")
	bare.OwnerReferences = nil
	notController := ownedPod("u1")
	notController.OwnerReferences[0].Controller = nil
	deleting := ownedPod("u1")
	deleting.DeletionTimestamp = &metav1.Time{}
	for name, tc := range map[string]struct {
		ref  corev1.ObjectReference
		pod  *corev1.Pod
		want healAction
	}{
		"owned":          {ref, ownedPod("u1"), healEvict},
		"owned, no uid":  {corev1.ObjectReference{Namespace: "app", Name: "web-0"}, ownedPod("u1"), healEvict},
		"gone":           {ref, nil, healGone},
		"deleting":       {ref, deleting, healGone},
		"recreated":      {ref, ownedPod("u2"), healGone},
		"bare":           {ref, bare, healBare},
		"not controller": {ref, notController, healBare},
	} {
		if got := healDecision(tc.ref, tc.pod); got != tc.want {
			t.Errorf("%s: %v, want %v", name, got, tc.want)
		}
	}
}

func TestNextBackoff(t *testing.T) {
	if nextBackoff(time.Second) != 2*time.Second || nextBackoff(20*time.Second) != healMaxBackoff {
		t.Fatal("backoff")
	}
}

// evictions records eviction calls; errs are returned in order, then success.
type evictions struct {
	mu   sync.Mutex
	got  []*policyv1.Eviction
	errs []error
}

func (e *evictions) count() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.got)
}

func (h *harness) healing(t *testing.T, pod *corev1.Pod, errs ...error) *evictions {
	t.Helper()
	h.srv.cfg.HealWorkloads = true
	if pod != nil {
		if err := h.cs.Tracker().Add(pod); err != nil { // the harness reactor would rewrite the UID
			t.Fatal(err)
		}
	}
	ev := &evictions{errs: errs}
	var n int
	h.cs.PrependReactor("create", "events", func(a k8stesting.Action) (bool, runtime.Object, error) {
		e := a.(k8stesting.CreateAction).GetObject().(*corev1.Event)
		n++ // the fake does not apply GenerateName
		e.Name = fmt.Sprintf("%s%d", e.GenerateName, n)
		return false, nil, nil
	})
	h.cs.PrependReactor("create", "pods", func(a k8stesting.Action) (bool, runtime.Object, error) {
		if a.GetSubresource() != "eviction" {
			return false, nil, nil
		}
		ev.mu.Lock()
		defer ev.mu.Unlock()
		ev.got = append(ev.got, a.(k8stesting.CreateAction).GetObject().(*policyv1.Eviction))
		if len(ev.errs) > 0 {
			err := ev.errs[0]
			ev.errs = ev.errs[1:]
			return true, nil, err
		}
		return true, nil, nil
	})
	return ev
}

// publishWeb0 publishes target for app/web-0 with UID u1.
func (h *harness) publishWeb0(t *testing.T, target string) {
	t.Helper()
	req := publishReq(target, creds)
	req.VolumeContext[keyPodName] = "web-0"
	req.VolumeContext[keyPodNamespace] = "app"
	req.VolumeContext[keyPodUID] = "u1"
	if _, err := h.srv.NodePublishVolume(context.Background(), req); err != nil {
		t.Fatal(err)
	}
}

func (h *harness) loseMountPod(t *testing.T) {
	t.Helper()
	h.ho.send(handoff.Event{Kind: handoff.EventClosed, Conn: 1})
	eventually(t, "recreated Mount Pod", func() bool { return h.ho.serveCount() == 2 })
}

func heals(h *harness, result string) float64 {
	return testutil.ToFloat64(h.srv.deps.Metrics.heals.WithLabelValues(result))
}

var pdbBlocked = apierrors.NewTooManyRequests("Cannot evict pod as it would violate the pod's disruption budget.", 0)

func TestHealEvictsOwnedPodWithUIDPrecondition(t *testing.T) {
	h := newHarness(t)
	ev := h.healing(t, ownedPod("u1"))
	h.publishWeb0(t, filepath.Join(h.dir, "a"))
	h.loseMountPod(t)
	h.srv.Wait()
	if ev.count() != 1 {
		t.Fatalf("evictions: %d", ev.count())
	}
	e := ev.got[0]
	if e.Namespace != "app" || e.Name != "web-0" || e.DeleteOptions == nil || e.DeleteOptions.Preconditions == nil ||
		*e.DeleteOptions.Preconditions.UID != "u1" {
		t.Fatalf("eviction: %+v", e)
	}
	if app := h.events(t, "app"); !slices.Contains(app, ReasonWorkloadHealed+"/web-0") {
		t.Errorf("events: %v", app)
	}
	if heals(h, HealEvicted) != 1 {
		t.Error("evicted metric")
	}
}

func TestHealSkipsBarePod(t *testing.T) {
	h := newHarness(t)
	bare := ownedPod("u1")
	bare.OwnerReferences = nil
	ev := h.healing(t, bare)
	h.publishWeb0(t, filepath.Join(h.dir, "a"))
	h.loseMountPod(t)
	h.srv.Wait()
	if ev.count() != 0 {
		t.Fatalf("bare pod evicted")
	}
	if app := h.events(t, "app"); !slices.Contains(app, ReasonHealBlocked+"/web-0") {
		t.Errorf("events: %v", app)
	}
	if heals(h, HealSkipped) != 1 {
		t.Error("skipped metric")
	}
}

func TestHealLeavesRecreatedPod(t *testing.T) {
	h := newHarness(t)
	ev := h.healing(t, ownedPod("u2"))
	h.publishWeb0(t, filepath.Join(h.dir, "a"))
	h.loseMountPod(t)
	h.srv.Wait()
	if ev.count() != 0 {
		t.Fatal("recreated pod evicted")
	}
	for _, r := range healResults {
		if heals(h, r) != 0 {
			t.Errorf("metric %s", r)
		}
	}
}

func TestHealDisabledDoesNothing(t *testing.T) {
	h := newHarness(t)
	ev := h.healing(t, ownedPod("u1"))
	h.srv.cfg.HealWorkloads = false
	h.publishWeb0(t, filepath.Join(h.dir, "a"))
	h.loseMountPod(t)
	h.srv.Wait()
	if ev.count() != 0 {
		t.Fatal("evicted while disabled")
	}
}

// stepUntil advances the fake clock while the heal goroutine waits, until done.
func (h *harness) stepUntil(t *testing.T, done func() bool) {
	t.Helper()
	eventually(t, "heal retries", func() bool {
		if h.clk.HasWaiters() {
			h.clk.Step(healMaxBackoff)
		}
		return done()
	})
}

func TestHealRetriesWhilePDBBlocks(t *testing.T) {
	h := newHarness(t)
	ev := h.healing(t, ownedPod("u1"), pdbBlocked, pdbBlocked)
	h.publishWeb0(t, filepath.Join(h.dir, "a"))
	h.loseMountPod(t)
	h.stepUntil(t, func() bool { return heals(h, HealEvicted) == 1 })
	h.srv.Wait()
	if ev.count() != 3 {
		t.Fatalf("evictions: %d", ev.count())
	}
}

func TestHealGivesUpWhenPDBKeepsBlocking(t *testing.T) {
	h := newHarness(t)
	errs := make([]error, 100)
	for i := range errs {
		errs[i] = pdbBlocked
	}
	ev := h.healing(t, ownedPod("u1"), errs...)
	h.publishWeb0(t, filepath.Join(h.dir, "a"))
	h.loseMountPod(t)
	h.stepUntil(t, func() bool { return heals(h, HealBlocked) == 1 })
	h.srv.Wait()
	if n := ev.count(); n < 3 || n > 20 {
		t.Fatalf("evictions: %d", n)
	}
	if app := h.events(t, "app"); !slices.Contains(app, ReasonHealBlocked+"/web-0") {
		t.Errorf("events: %v", app)
	}
}

func TestHealConflictMeansReplaced(t *testing.T) {
	h := newHarness(t)
	conflict := apierrors.NewConflict(schema.GroupResource{Resource: "pods"}, "web-0", nil)
	h.healing(t, ownedPod("u1"), conflict)
	h.publishWeb0(t, filepath.Join(h.dir, "a"))
	h.loseMountPod(t)
	h.srv.Wait()
	for _, r := range healResults {
		if heals(h, r) != 0 {
			t.Errorf("metric %s", r)
		}
	}
}

func TestHealAfterAdoptCleanup(t *testing.T) {
	h := newHarness(t)
	ev := h.healing(t, ownedPod("u1"))
	id := "1dead"
	target := filepath.Join(h.dir, "pods", "u1", "volumes", "kubernetes.io~csi", "pv", "mount")
	h.infos = []mount.MountInfo{{Minor: 50, MountPoint: mountpod.FuseDir(h.dir, id)}, {Minor: 50, MountPoint: target}}
	p := mountPodObj(id, corev1.PodFailed)
	p.Annotations = map[string]string{mountpod.AnnotationUsers: `{"` + target + `":{"ns":"app","name":"web-0","uid":"u1"}}`}
	h.createPod(t, p)
	if err := h.srv.Adopt(context.Background()); err != nil {
		t.Fatal(err)
	}
	h.srv.Wait()
	if ev.count() != 1 || heals(h, HealEvicted) != 1 {
		t.Fatalf("evictions: %d", ev.count())
	}
}
