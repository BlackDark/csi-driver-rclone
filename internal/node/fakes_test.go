package node

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/mount-utils"

	"github.com/BlackDark/csi-driver-rclone/internal/fusemount"
	"github.com/BlackDark/csi-driver-rclone/internal/handoff"
	"github.com/BlackDark/csi-driver-rclone/internal/mountpod"
)

type fakeFuse struct {
	mu        sync.Mutex
	mounted   map[string]fusemount.Opts
	unmounted []string
}

func (f *fakeFuse) Mount(target string, o fusemount.Opts) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.mounted[target] = o
	return -1, nil
}

func (f *fakeFuse) Unmount(target string, _ bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.mounted, target)
	f.unmounted = append(f.unmounted, target)
	return nil
}

func (f *fakeFuse) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.mounted)
}

// fakeHandoff acts as a Mount Pod that connects and becomes ready at once.
type fakeHandoff struct {
	mu     sync.Mutex
	serves int
	events chan<- handoff.Event
}

func (h *fakeHandoff) Serve(ctx context.Context, _ handoff.ServerConfig, ev chan<- handoff.Event) error {
	h.mu.Lock()
	h.serves++
	h.events = ev
	h.mu.Unlock()
	ev <- handoff.Event{Kind: handoff.EventFDSent, Conn: 1}
	ev <- handoff.Event{Kind: handoff.EventReady, Conn: 1}
	<-ctx.Done()
	return nil
}

func (h *fakeHandoff) send(e handoff.Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.events <- e
}

type harness struct {
	srv     *Server
	cs      *fake.Clientset
	mounter *mount.FakeMounter
	fuse    *fakeFuse
	ho      *fakeHandoff
	dir     string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{
		cs:      fake.NewClientset(),
		mounter: mount.NewFakeMounter(nil),
		fuse:    &fakeFuse{mounted: map[string]fusemount.Opts{}},
		ho:      &fakeHandoff{},
	}
	dir, err := filepath.EvalSymlinks(t.TempDir()) // darwin: /var -> /private/var
	if err != nil {
		t.Fatal(err)
	}
	h.dir = dir
	h.cs.PrependReactor("create", "pods", func(a k8stesting.Action) (bool, runtime.Object, error) {
		p := a.(k8stesting.CreateAction).GetObject().(*corev1.Pod)
		p.UID = types.UID("uid-" + p.Name)
		return false, nil, nil
	})
	cfg := Config{
		NodeID: "node-a",
		Pod: mountpod.Config{
			Namespace: "drv", Image: "img", KubeletDir: h.dir, CacheRoot: h.dir + "/cache",
			UID: 65532, GID: 65532, DrainTimeout: time.Second, UnmountWait: time.Second,
		},
		PublishTimeout: 5 * time.Second,
	}
	h.srv = New(cfg, Deps{
		Kube: NewKube(h.cs, "drv"), Mounter: h.mounter, Fuse: h.fuse, Handoff: h.ho,
		PrepareDir: func(string, int, int) error { return nil },
		Log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	return h
}

func publishReq(target string, secrets map[string]string) *csi.NodePublishVolumeRequest {
	return &csi.NodePublishVolumeRequest{
		VolumeId:   "vol-1",
		TargetPath: target,
		VolumeCapability: &csi.VolumeCapability{
			AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{}},
			AccessMode: &csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER},
		},
		Secrets:       secrets,
		VolumeContext: map[string]string{"remotePath": "bucket"},
	}
}

var creds = map[string]string{"remoteType": "s3", "access_key_id": "AKIAVALUE", "secret_access_key": "s3cr3tVALUE"}
