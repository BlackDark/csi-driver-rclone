package main

import (
	"context"
	"log/slog"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/utils/clock"

	"github.com/BlackDark/csi-driver-rclone/internal/node"
	"github.com/BlackDark/csi-driver-rclone/internal/oauthtoken"
)

// Refresh Lease timing: short enough that a crashed holder blocks others briefly,
// long enough for an OAuth refresh round trip.
const (
	tokenLeaseDuration = 30 * time.Second
	tokenLeaseMaxHold  = 2 * time.Minute
	tokenLeasePoll     = time.Second
	tokenLockWait      = 45 * time.Second
)

func tokenManager(cs kubernetes.Interface, cfg node.Config, m *node.Metrics, log *slog.Logger) *oauthtoken.Manager {
	ns := cfg.Pod.Namespace
	return &oauthtoken.Manager{
		Store: oauthtoken.Store{CS: cs, Namespace: ns},
		Locker: oauthtoken.Locker{CS: cs, Namespace: ns, Clock: clock.RealClock{},
			Duration: tokenLeaseDuration, MaxHold: tokenLeaseMaxHold, Poll: tokenLeasePoll},
		Node: cfg.NodeID, LockWait: tokenLockWait, Log: log,
		ObserveSave:     func(r oauthtoken.SaveResult) { m.TokenSaved(string(r)) },
		ObserveLockWait: m.TokenLockWaited,
	}
}

// watchTokenSecrets pushes Token Secret changes, from any node, to local Mount Pods.
func watchTokenSecrets(ctx context.Context, cs kubernetes.Interface, cfg node.Config, tokens *oauthtoken.Manager) {
	factory := informers.NewSharedInformerFactoryWithOptions(cs, 0,
		informers.WithNamespace(cfg.Pod.Namespace),
		informers.WithTweakListOptions(func(o *metav1.ListOptions) { o.LabelSelector = oauthtoken.Selector() }))
	inf := factory.Core().V1().Secrets().Informer()
	_, _ = inf.AddEventHandler(cache.ResourceEventHandlerFuncs{
		UpdateFunc: func(_, o any) {
			if s, ok := o.(*corev1.Secret); ok {
				tokens.Updated(s)
			}
		},
	})
	factory.Start(ctx.Done())
}
