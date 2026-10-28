package oauthtoken

import (
	"context"
	"fmt"
	"sync"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/clock"
	"k8s.io/utils/ptr"
)

// Locker takes the refresh Lease of a Token Secret: one refresher across all
// processes and nodes.
type Locker struct {
	CS        kubernetes.Interface
	Namespace string
	Clock     clock.Clock
	// Duration is the Lease duration; the holder renews it every Duration/3.
	Duration time.Duration
	// MaxHold stops renewing a Lease whose holder never releases it.
	MaxHold time.Duration
	// Poll is the retry interval while another holder has the Lease.
	Poll time.Duration
}

// Acquire blocks until holder has Lease name or ctx is done. release is idempotent.
func (l Locker) Acquire(ctx context.Context, name, holder string) (release func(), err error) {
	for {
		ok, err := l.try(ctx, name, holder)
		if err != nil {
			return nil, err
		}
		if ok {
			return l.hold(name, holder), nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-l.Clock.After(l.Poll):
		}
	}
}

// try takes the Lease if it is free, expired or already ours; false when held by
// another holder or lost to a concurrent writer.
func (l Locker) try(ctx context.Context, name, holder string) (bool, error) {
	api := l.CS.CoordinationV1().Leases(l.Namespace)
	now := metav1.NewMicroTime(l.Clock.Now())
	cur, err := api.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = api.Create(ctx, &coordinationv1.Lease{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: l.Namespace},
			Spec:       l.spec(holder, now),
		}, metav1.CreateOptions{})
		if apierrors.IsAlreadyExists(err) {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("create lease: %w", err)
		}
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("get lease: %w", err)
	}
	if !l.free(cur, holder) {
		return false, nil
	}
	cur.Spec = l.spec(holder, now)
	_, err = api.Update(ctx, cur, metav1.UpdateOptions{})
	if apierrors.IsConflict(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("update lease: %w", err)
	}
	return true, nil
}

func (l Locker) free(cur *coordinationv1.Lease, holder string) bool {
	h := ptr.Deref(cur.Spec.HolderIdentity, "")
	if h == "" || h == holder || cur.Spec.RenewTime == nil {
		return true
	}
	d := time.Duration(ptr.Deref(cur.Spec.LeaseDurationSeconds, 0)) * time.Second
	return l.Clock.Now().After(cur.Spec.RenewTime.Add(d))
}

func (l Locker) spec(holder string, now metav1.MicroTime) coordinationv1.LeaseSpec {
	secs := int32(l.Duration / time.Second)
	return coordinationv1.LeaseSpec{
		HolderIdentity: ptr.To(holder), LeaseDurationSeconds: ptr.To(secs),
		AcquireTime: &now, RenewTime: &now,
	}
}

// hold renews the Lease until release or MaxHold, then gives it up.
func (l Locker) hold(name, holder string) func() {
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		deadline := l.Clock.After(l.MaxHold)
		for {
			select {
			case <-stop:
				return
			case <-deadline:
				return // let it expire: the holder is stuck
			case <-l.Clock.After(l.Duration / 3):
				l.renew(name, holder)
			}
		}
	}()
	return sync.OnceFunc(func() {
		close(stop)
		<-done
		l.release(name, holder)
	})
}

func (l Locker) renew(name, holder string) {
	ctx, cancel := context.WithTimeout(context.Background(), l.Duration/3)
	defer cancel()
	api := l.CS.CoordinationV1().Leases(l.Namespace)
	cur, err := api.Get(ctx, name, metav1.GetOptions{})
	if err != nil || ptr.Deref(cur.Spec.HolderIdentity, "") != holder {
		return
	}
	cur.Spec.RenewTime = ptr.To(metav1.NewMicroTime(l.Clock.Now()))
	_, _ = api.Update(ctx, cur, metav1.UpdateOptions{})
}

func (l Locker) release(name, holder string) {
	ctx, cancel := context.WithTimeout(context.Background(), l.Duration)
	defer cancel()
	api := l.CS.CoordinationV1().Leases(l.Namespace)
	cur, err := api.Get(ctx, name, metav1.GetOptions{})
	if err != nil || ptr.Deref(cur.Spec.HolderIdentity, "") != holder {
		return
	}
	cur.Spec.HolderIdentity = ptr.To("")
	cur.Spec.RenewTime = nil
	_, _ = api.Update(ctx, cur, metav1.UpdateOptions{})
}
