// Package sanity runs csi-sanity against the controller and node services in-process.
// Kubernetes, FUSE, the handoff socket and bind mounts are faked, so it needs no root.
package sanity

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/kubernetes-csi/csi-test/v5/pkg/sanity"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"google.golang.org/grpc"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/mount-utils"

	"github.com/BlackDark/csi-driver-rclone/internal/controller"
	"github.com/BlackDark/csi-driver-rclone/internal/csiserver"
	"github.com/BlackDark/csi-driver-rclone/internal/fusemount"
	"github.com/BlackDark/csi-driver-rclone/internal/handoff"
	"github.com/BlackDark/csi-driver-rclone/internal/identity"
	"github.com/BlackDark/csi-driver-rclone/internal/mountpod"
	"github.com/BlackDark/csi-driver-rclone/internal/node"
)

// skipped are sanity specs that do not apply, by spec text substring, with the reason.
// Specs for capabilities the driver does not advertise (snapshots, expansion,
// ListVolumes, GetCapacity, ControllerPublish, NodeStage, modify) skip themselves.
var skipped = map[string]string{
	"already existing name and different capacity": "the controller is stateless (volume ID = name, capacity recorded, " +
		"not enforced), so it cannot see an earlier request; external-provisioner never reuses a PV name",
	"ValidateVolumeCapabilities should fail when the requested volume does not exist": "the controller is stateless " +
		"and holds no volume list; every well-formed volume ID is a remote path that may exist",
}

// fakeFuse mounts nothing; the fd is never used because the handoff is fake too.
type fakeFuse struct{}

func (fakeFuse) Mount(string, fusemount.Opts) (int, error) { return -1, nil }
func (fakeFuse) Unmount(string, bool) error                { return errors.New("not mounted") }

// fakeHandoff is a Mount Pod that becomes ready at once and has nothing to drain.
type fakeHandoff struct{}

func (fakeHandoff) Serve(ctx context.Context, _ handoff.ServerConfig, ev chan<- handoff.Event) error {
	for _, k := range []handoff.EventKind{handoff.EventFDSent, handoff.EventReady, handoff.EventDrained} {
		select {
		case ev <- handoff.Event{Kind: k, Conn: 1}:
		case <-ctx.Done():
			return nil
		}
	}
	<-ctx.Done()
	return nil
}

func statfs(p string) (node.Usage, error) {
	if _, err := os.Stat(p); err != nil {
		return node.Usage{}, err
	}
	return node.Usage{TotalBytes: 100, AvailableBytes: 60, UsedBytes: 40}, nil
}

// shortTempDir keeps unix socket paths under the 104-byte darwin limit.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "san")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	dir, err = filepath.EvalSymlinks(dir) // darwin: /var -> /private/var
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func serve(t *testing.T, dir string) string {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := node.Config{
		NodeID: "sanity-node",
		Pod: mountpod.Config{
			Namespace: "drv", Image: "img", KubeletDir: filepath.Join(dir, "kubelet"),
			CacheRoot: filepath.Join(dir, "cache"), UID: 65532, GID: 65532,
			DrainTimeout: time.Second, UnmountWait: time.Second,
		},
		PublishTimeout: 5 * time.Second,
	}
	srv := node.New(cfg, node.Deps{
		Kube:       node.NewKube(fake.NewClientset(), "drv"),
		Mounter:    mount.NewFakeMounter(nil),
		Fuse:       fakeFuse{},
		Handoff:    fakeHandoff{},
		PrepareDir: func(string, int, int) error { return nil },
		MountInfo:  func() ([]mount.MountInfo, error) { return nil, nil },
		Statfs:     statfs,
		Log:        log,
	})
	endpoint := "unix://" + filepath.Join(dir, "csi.sock")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- csiserver.Serve(ctx, endpoint, log, func(g *grpc.Server) {
			csi.RegisterIdentityServer(g, &identity.Server{Version: "sanity", Controller: true})
			csi.RegisterControllerServer(g, &controller.Server{Log: log})
			csi.RegisterNodeServer(g, srv)
		})
	}()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
		srv.Wait()
	})
	return endpoint
}

func TestSanity(t *testing.T) {
	dir := shortTempDir(t)
	endpoint := serve(t, dir)

	secrets := filepath.Join(dir, "secrets.yaml")
	// One local remote; the same secret goes to every RPC that takes secrets.
	creds := "  configData: |\n    [local]\n    type = local\n"
	yaml := ""
	for _, k := range []string{"CreateVolumeSecret", "DeleteVolumeSecret", "NodePublishVolumeSecret", "NodeStageVolumeSecret",
		"ControllerPublishVolumeSecret", "ControllerUnpublishVolumeSecret", "ControllerValidateVolumeCapabilitiesSecret",
		"ListSnapshotsSecret", "CreateSnapshotSecret", "DeleteSnapshotSecret", "ControllerExpandVolumeSecret", "NodeExpandVolumeSecret"} {
		yaml += k + ":\n" + creds
	}
	if err := os.WriteFile(secrets, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := sanity.NewTestConfig()
	cfg.Address = endpoint
	cfg.TargetPath = filepath.Join(dir, "target")
	cfg.StagingPath = filepath.Join(dir, "staging")
	cfg.SecretsFile = secrets
	cfg.TestVolumeParameters = map[string]string{
		"remotePath":           "sanity/${pv.name}",
		controller.ParamPVName: "pv-sanity",
	}

	sanity.GinkgoTest(&cfg)
	gomega.RegisterFailHandler(ginkgo.Fail)
	suite, reporter := ginkgo.GinkgoConfiguration()
	for text, reason := range skipped {
		t.Logf("skip %q: %s", text, reason)
		suite.SkipStrings = append(suite.SkipStrings, text)
	}
	ginkgo.RunSpecs(t, "csi-sanity", suite, reporter)
}
