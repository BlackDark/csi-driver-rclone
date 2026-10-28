package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"

	"github.com/BlackDark/csi-driver-rclone/internal/csiserver"
	"github.com/BlackDark/csi-driver-rclone/internal/identity"
	"github.com/BlackDark/csi-driver-rclone/internal/metrics"
	"github.com/BlackDark/csi-driver-rclone/internal/node"
)

// version is set with -ldflags "-X main.version=...".
var version = "dev"

type nodeFlags struct {
	endpoint    string
	cfg         node.Config
	uid, gid    int64
	tolerations string
	metricsAddr string
	logLevel    slog.Level
}

func parseNodeFlags(args []string, stderr io.Writer) (nodeFlags, error) {
	var f nodeFlags
	p := &f.cfg.Pod
	fs := flag.NewFlagSet("node", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&f.endpoint, "endpoint", "unix:///csi/csi.sock", "CSI endpoint")
	fs.StringVar(&f.cfg.NodeID, "node-id", os.Getenv("NODE_ID"), "node name (env NODE_ID)")
	fs.StringVar(&p.Namespace, "namespace", os.Getenv("POD_NAMESPACE"), "driver namespace for Mount Pods (env POD_NAMESPACE)")
	fs.StringVar(&p.KubeletDir, "kubelet-dir", "/var/lib/kubelet", "kubelet root dir")
	fs.StringVar(&p.CacheRoot, "cache-root", "/var/lib/rclone-csi/cache", "host dir holding the hostPath Caches")
	fs.StringVar(&p.Image, "mount-image", "", "Mount Pod image")
	fs.Int64Var(&f.uid, "mount-uid", 65532, "Mount Pod uid")
	fs.Int64Var(&f.gid, "mount-gid", 65532, "Mount Pod gid")
	fs.StringVar(&p.PriorityClassName, "mount-priority-class", "system-node-critical", "Mount Pod priorityClassName")
	fs.StringVar(&f.tolerations, "mount-tolerations", "", "Mount Pod tolerations as a JSON list")
	fs.DurationVar(&f.cfg.PublishTimeout, "publish-timeout", 2*time.Minute, "bound on waiting for a Mount Pod to be ready")
	fs.DurationVar(&f.cfg.Linger, "linger", 2*time.Minute, "how long an unused Mount Pod stays up; 0 stops it at once")
	fs.DurationVar(&p.DrainTimeout, "drain-timeout", 5*time.Minute, "Mount Pod upload drain bound on stop")
	fs.DurationVar(&p.UnmountWait, "unmount-wait", 30*time.Second, "Mount Pod wait for the external unmount after draining")
	fs.StringVar(&f.metricsAddr, "metrics-addr", ":9809", "address for /metrics; empty disables")
	fs.TextVar(&f.logLevel, "log-level", slog.LevelInfo, "log level")
	if err := fs.Parse(args); err != nil {
		return f, err
	}
	return f, f.finish(fs)
}

func (f *nodeFlags) finish(fs *flag.FlagSet) error {
	p := &f.cfg.Pod
	switch {
	case fs.NArg() > 0:
		return fmt.Errorf("node: unexpected arguments %v", fs.Args())
	case f.cfg.NodeID == "" || p.Namespace == "" || p.Image == "":
		return fmt.Errorf("node: --node-id, --namespace and --mount-image are required")
	case f.uid <= 0 || f.gid <= 0:
		return fmt.Errorf("node: --mount-uid and --mount-gid must be non-root")
	}
	p.UID, p.GID = f.uid, f.gid
	if f.tolerations != "" {
		var t []corev1.Toleration
		if err := json.Unmarshal([]byte(f.tolerations), &t); err != nil {
			return fmt.Errorf("node: --mount-tolerations: %w", err)
		}
		p.Tolerations = t
	}
	return nil
}

func runNode(args []string, stderr io.Writer) error {
	f, err := parseNodeFlags(args, stderr)
	if err != nil {
		return err
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: f.logLevel}))
	rc, err := rest.InClusterConfig()
	if err != nil {
		return fmt.Errorf("node: in-cluster config: %w", err)
	}
	cs, err := kubernetes.NewForConfig(rc)
	if err != nil {
		return fmt.Errorf("node: kube client: %w", err)
	}
	reg := metrics.NewRegistry()
	srv := node.New(f.cfg, node.Deps{
		Kube:    node.NewKube(cs, f.cfg.Pod.Namespace),
		Mounter: node.NewBindMounter(),
		Fuse:    node.RealFuse{},
		Handoff: node.RealHandoff{},
		Log:     log,
		Metrics: node.NewMetrics(reg),
	})
	reg.MustRegister(srv.Collector())
	rpc := metrics.NewGRPC(reg)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	metrics.ServeBackground(ctx, f.metricsAddr, metrics.Handler(reg, nil), log)
	if err := srv.Adopt(ctx); err != nil {
		return fmt.Errorf("node: %w", err)
	}
	watchMountPods(ctx, cs, f.cfg, srv)
	return csiserver.Serve(ctx, f.endpoint, log, func(g *grpc.Server) {
		csi.RegisterIdentityServer(g, &identity.Server{Version: version})
		csi.RegisterNodeServer(g, srv)
	}, rpc.Interceptor(time.Now))
}

// watchMountPods feeds this node's Mount Pod changes to srv until ctx is done.
func watchMountPods(ctx context.Context, cs kubernetes.Interface, cfg node.Config, srv *node.Server) {
	factory := informers.NewSharedInformerFactoryWithOptions(cs, 0,
		informers.WithNamespace(cfg.Pod.Namespace),
		informers.WithTweakListOptions(func(o *metav1.ListOptions) { o.LabelSelector = node.MountPodSelector(cfg.NodeID) }))
	inf := factory.Core().V1().Pods().Informer()
	_, _ = inf.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(o any) { podChanged(srv, o, false) },
		UpdateFunc: func(_, o any) { podChanged(srv, o, false) },
		DeleteFunc: func(o any) { podChanged(srv, o, true) },
	})
	factory.Start(ctx.Done())
}

func podChanged(srv *node.Server, o any, deleted bool) {
	if t, ok := o.(cache.DeletedFinalStateUnknown); ok {
		o = t.Obj
	}
	if p, ok := o.(*corev1.Pod); ok {
		srv.MountPodChanged(p, deleted)
	}
}
