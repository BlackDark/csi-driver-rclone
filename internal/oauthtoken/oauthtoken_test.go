package oauthtoken

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	clocktesting "k8s.io/utils/clock/testing"
	"k8s.io/utils/ptr"

	"github.com/BlackDark/csi-driver-rclone/internal/mountspec"
)

const ns = "csi"

var seedHash = mountspec.HashToken("seed")

func tok(expiry string) string { return `{"refresh_token":"r","expiry":"` + expiry + `"}` }

func storeOf(cs *fake.Clientset) Store { return Store{CS: cs, Namespace: ns} }

func stored(t *testing.T, cs *fake.Clientset) (token, seed string) {
	t.Helper()
	s, err := cs.CoreV1().Secrets(ns).Get(context.Background(), Name(seedHash), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return string(s.Data[DataToken]), string(s.Data[DataSeedHash])
}

func TestSeedCreatesThenReuses(t *testing.T) {
	cs := fake.NewClientset()
	s := storeOf(cs)
	got, err := s.Seed(context.Background(), "box", seedHash, "seed")
	if err != nil || got != "seed" {
		t.Fatalf("%q %v", got, err)
	}
	if _, err := s.Save(context.Background(), "box", seedHash, tok("2030-01-01T00:00:00Z")); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Seed(context.Background(), "box", seedHash, "seed")
	if got != tok("2030-01-01T00:00:00Z") {
		t.Fatalf("seed did not reuse the stored token: %q", got)
	}
	sec, _ := cs.CoreV1().Secrets(ns).Get(context.Background(), Name(seedHash), metav1.GetOptions{})
	if sec.Labels["app.kubernetes.io/component"] != Component || sec.Annotations[AnnotationSection] != "box" {
		t.Errorf("meta %v %v", sec.Labels, sec.Annotations)
	}
}

func TestSeedReseedsForeignSeed(t *testing.T) {
	cs := fake.NewClientset()
	s := storeOf(cs)
	_, _ = s.Seed(context.Background(), "box", seedHash, "seed")
	sec, _ := cs.CoreV1().Secrets(ns).Get(context.Background(), Name(seedHash), metav1.GetOptions{})
	sec.Data[DataSeedHash] = []byte("other")
	sec.Data[DataToken] = []byte("old")
	_, _ = cs.CoreV1().Secrets(ns).Update(context.Background(), sec, metav1.UpdateOptions{})
	got, err := s.Seed(context.Background(), "box", seedHash, "seed")
	if err != nil || got != "seed" {
		t.Fatalf("%q %v", got, err)
	}
	if tk, sh := stored(t, cs); tk != "seed" || sh != seedHash {
		t.Fatalf("stored %q %q", tk, sh)
	}
}

func TestSaveKeepsLaterToken(t *testing.T) {
	cs := fake.NewClientset()
	s := storeOf(cs)
	ctx := context.Background()
	if r, _ := s.Save(ctx, "box", seedHash, tok("2030-01-02T00:00:00Z")); r != Written {
		t.Fatalf("first save %s", r)
	}
	if r, _ := s.Save(ctx, "box", seedHash, tok("2030-01-02T00:00:00Z")); r != Unchanged {
		t.Fatalf("same save %s", r)
	}
	if r, _ := s.Save(ctx, "box", seedHash, tok("2030-01-01T00:00:00Z")); r != Stale {
		t.Fatalf("older save %s", r)
	}
	if r, _ := s.Save(ctx, "box", seedHash, tok("2030-01-03T00:00:00Z")); r != Written {
		t.Fatalf("newer save %s", r)
	}
	if tk, _ := stored(t, cs); tk != tok("2030-01-03T00:00:00Z") {
		t.Fatalf("stored %q", tk)
	}
}

func TestSaveRetriesOnConflict(t *testing.T) {
	cs := fake.NewClientset()
	s := storeOf(cs)
	ctx := context.Background()
	_, _ = s.Save(ctx, "box", seedHash, tok("2030-01-01T00:00:00Z"))
	var conflicts atomic.Int32
	cs.PrependReactor("update", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
		if conflicts.Add(1) <= 2 {
			return true, nil, apierrors.NewConflict(schema.GroupResource{Resource: "secrets"}, Name(seedHash), errors.New("rv"))
		}
		return false, nil, nil
	})
	if r, err := s.Save(ctx, "box", seedHash, tok("2030-01-02T00:00:00Z")); r != Written || err != nil {
		t.Fatalf("%s %v", r, err)
	}
	conflicts.Store(-100)
	if r, err := s.Save(ctx, "box", seedHash, tok("2030-01-03T00:00:00Z")); r != Failed || err == nil {
		t.Fatalf("endless conflict: %s %v", r, err)
	}
}

func locker(cs *fake.Clientset, clk *clocktesting.FakeClock) Locker {
	return Locker{CS: cs, Namespace: ns, Clock: clk, Duration: 30 * time.Second, MaxHold: 2 * time.Minute, Poll: time.Second}
}

func holder(t *testing.T, cs *fake.Clientset) string {
	t.Helper()
	l, err := cs.CoordinationV1().Leases(ns).Get(context.Background(), "l", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return ptr.Deref(l.Spec.HolderIdentity, "")
}

func TestLeaseSerializesHolders(t *testing.T) {
	cs := fake.NewClientset()
	clk := clocktesting.NewFakeClock(time.Unix(1000, 0))
	lk := locker(cs, clk)
	relA, err := lk.Acquire(context.Background(), "l", "a")
	if err != nil || holder(t, cs) != "a" {
		t.Fatalf("a: %v", err)
	}
	got := make(chan func(), 1)
	go func() {
		rel, err := lk.Acquire(context.Background(), "l", "b")
		if err == nil {
			got <- rel
		}
	}()
	for !clk.HasWaiters() {
		time.Sleep(time.Millisecond)
	}
	select {
	case <-got:
		t.Fatal("b got a held lease")
	case <-time.After(50 * time.Millisecond):
	}
	relA()
	relA() // idempotent
	var relB func()
	for relB == nil {
		clk.Step(time.Second)
		select {
		case relB = <-got:
		case <-time.After(5 * time.Millisecond):
		}
	}
	if holder(t, cs) != "b" {
		t.Fatalf("holder %q", holder(t, cs))
	}
	relB()
	if holder(t, cs) != "" {
		t.Fatal("not released")
	}
}

func TestLeaseTakesExpired(t *testing.T) {
	cs := fake.NewClientset()
	clk := clocktesting.NewFakeClock(time.Unix(1000, 0))
	lk := locker(cs, clk)
	if ok, _ := lk.try(context.Background(), "l", "dead"); !ok {
		t.Fatal("first try")
	}
	if ok, _ := lk.try(context.Background(), "l", "b"); ok {
		t.Fatal("took a live lease")
	}
	clk.Step(31 * time.Second)
	if ok, _ := lk.try(context.Background(), "l", "b"); !ok {
		t.Fatal("did not take an expired lease")
	}
}

func TestLeaseAcquireHonoursContext(t *testing.T) {
	cs := fake.NewClientset()
	lk := locker(cs, clocktesting.NewFakeClock(time.Unix(1000, 0)))
	_, _ = lk.try(context.Background(), "l", "a")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := lk.Acquire(ctx, "l", "b"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err %v", err)
	}
}

func manager(cs *fake.Clientset) *Manager {
	return &Manager{
		Store: storeOf(cs), Locker: locker(cs, clocktesting.NewFakeClock(time.Unix(1000, 0))),
		Node: "n1", LockWait: time.Second, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func TestManagerResolveSubstitutes(t *testing.T) {
	cs := fake.NewClientset()
	m := manager(cs)
	spec, err := mountspec.Resolve(mountspec.Input{
		Secrets:  map[string]string{mountspec.KeyConfigData: "[box]\ntype = box\ntoken = seed\n"},
		VolumeID: "v", VolumeContext: map[string]string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if got := m.Resolve(ctx, spec); got != spec.Remote.ConfigData {
		t.Fatalf("first resolve changed config: %q", got)
	}
	_, _ = m.Store.Save(ctx, "box", seedHash, tok("2030-01-01T00:00:00Z"))
	got := m.Resolve(ctx, spec)
	if mountspec.TokenOf(got, spec.Tokens[0].Isolated) != tok("2030-01-01T00:00:00Z") {
		t.Fatalf("not substituted: %q", got)
	}
	if !strings.Contains(got, "type = box") {
		t.Fatal("lost other lines")
	}
}

func TestManagerHandlerFlow(t *testing.T) {
	cs := fake.NewClientset()
	m := manager(cs)
	ts := []mountspec.TokenSection{{Section: "box", Isolated: "box_1", SeedHash: seedHash}}
	ctx := context.Background()
	_, _ = m.Store.Seed(ctx, "box", seedHash, "seed")
	a, b := m.Handler("a", ts), m.Handler("b", ts)
	if m.Handler("c", nil) != nil {
		t.Fatal("handler without tokens")
	}
	pushedB := make(chan string, 4)
	cancelB := b.Subscribe(func(section, token string) { pushedB <- section + "=" + token })
	defer cancelB()

	got, err := a.Lock(ctx, "box")
	if err != nil || got != "seed" {
		t.Fatalf("lock %q %v", got, err)
	}
	if _, err := b.Lock(ctx, "box"); err == nil {
		t.Fatal("b locked while a holds")
	}
	if err := a.Save(ctx, "box", tok("2030-01-01T00:00:00Z")); err != nil {
		t.Fatal(err)
	}
	if got, err := b.Lock(ctx, "box"); err != nil || got != tok("2030-01-01T00:00:00Z") {
		t.Fatalf("b lock after save %q %v", got, err)
	}
	b.Unlock("box")
	sec, _ := cs.CoreV1().Secrets(ns).Get(ctx, Name(seedHash), metav1.GetOptions{})
	m.Updated(sec)
	if p := <-pushedB; p != "box="+tok("2030-01-01T00:00:00Z") {
		t.Fatalf("push %q", p)
	}
	if _, err := a.Lock(ctx, "nope"); !errors.Is(err, errUnknownSection) {
		t.Fatalf("unknown section %v", err)
	}
}

func TestHandlerSkipsMalformedSeedHash(t *testing.T) {
	m := manager(fake.NewClientset())
	h := m.Handler("a", []mountspec.TokenSection{
		{Section: "box", Isolated: "box_1", SeedHash: "short"},
		{Section: "up", Isolated: "up_1", SeedHash: strings.ToUpper(seedHash)},
	})
	if _, err := h.Lock(context.Background(), "up"); !errors.Is(err, errUnknownSection) {
		t.Fatalf("uppercase: %v", err)
	}
	if _, err := h.Lock(context.Background(), "box"); !errors.Is(err, errUnknownSection) {
		t.Fatalf("err %v", err)
	}
}

func TestLockReleasesLeaseWhenReadFails(t *testing.T) {
	cs := fake.NewClientset()
	m := manager(cs)
	h := m.Handler("a", []mountspec.TokenSection{{Section: "box", Isolated: "box_1", SeedHash: seedHash}})
	cs.PrependReactor("get", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("api down")
	})
	if _, err := h.Lock(context.Background(), "box"); err == nil {
		t.Fatal("lock succeeded without a token read")
	}
	l, _ := cs.CoordinationV1().Leases(ns).Get(context.Background(), Name(seedHash), metav1.GetOptions{})
	if ptr.Deref(l.Spec.HolderIdentity, "") != "" {
		t.Fatal("lease still held")
	}
}

func TestTwoSectionsOnOneGrantAreTwoHolders(t *testing.T) {
	cs := fake.NewClientset()
	m := manager(cs)
	h := m.Handler("a", []mountspec.TokenSection{
		{Section: "box", Isolated: "box_1", SeedHash: seedHash},
		{Section: "box2", Isolated: "box2_1", SeedHash: seedHash},
	})
	if _, err := h.Lock(context.Background(), "box"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Lock(context.Background(), "box2"); err == nil {
		t.Fatal("second section got the lease the first holds")
	}
}
