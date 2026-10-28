//go:build fuse_integration && linux

// Needs root, /dev/fuse and CAP_SYS_ADMIN: run via `mise run test:fuse` (or test:fuse:host).
package rclonemount_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/BlackDark/csi-driver-rclone/internal/fusemount"
	"github.com/BlackDark/csi-driver-rclone/internal/handoff"
	"github.com/BlackDark/csi-driver-rclone/internal/mountspec"
)

// mountPodID is an unprivileged uid/gid, as in a real Mount Pod.
const mountPodID = 1000

type fixture struct {
	base, fuse, remote, cache, sock, bin, spec string
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	base, err := os.MkdirTemp("/tmp", "fit")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	f := fixture{
		base: base, fuse: filepath.Join(base, "fuse"), remote: filepath.Join(base, "remote"),
		cache: filepath.Join(base, "cache"), sock: filepath.Join(base, "handoff.sock"),
		bin: filepath.Join(base, "csi-driver-rclone"), spec: filepath.Join(base, "spec.json"),
	}
	must(t, os.Chmod(base, 0o755))
	for _, d := range []string{f.fuse, f.remote, f.cache} {
		must(t, os.Mkdir(d, 0o777))
		must(t, os.Chmod(d, 0o777))
	}
	if bin := os.Getenv("FUSE_IT_BIN"); bin != "" { // prebuilt: root may lack go (test:fuse:host)
		f.bin = bin
		return f
	}
	build := exec.Command("go", "build", "-buildvcs=false", "-o", f.bin, "../../cmd/csi-driver-rclone")
	build.Stdout, build.Stderr = os.Stdout, os.Stderr
	must(t, build.Run())
	return f
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func (f fixture) writeSpec(t *testing.T) {
	t.Helper()
	s, err := mountspec.Resolve(mountspec.Input{
		VolumeID: "fuse-it",
		Secrets:  map[string]string{mountspec.KeyConfigData: "[r]\ntype = local\n"},
		VolumeContext: map[string]string{
			mountspec.KeyRemotePath: f.remote,
			"vfs-cache-mode":        "writes",
			"vfs-write-back":        "3s", // leaves an upload queued for the drain to wait on
		},
	})
	must(t, err)
	b, err := s.Serialize()
	must(t, err)
	must(t, os.WriteFile(f.spec, b, 0o644))
}

func expect(t *testing.T, events chan handoff.Event, want handoff.EventKind, within time.Duration) {
	t.Helper()
	select {
	case e := <-events:
		if e.Kind != want {
			t.Fatalf("got %s (%v), want %s", e.Kind, e.Err, want)
		}
	case <-time.After(within):
		t.Fatalf("no %s within %s", want, within)
	}
}

func TestFuseHandoffMount2(t *testing.T) {
	f := newFixture(t)
	f.writeSpec(t)

	fd, err := fusemount.Mount(f.fuse, fusemount.Opts{Source: "r:", UID: mountPodID, GID: mountPodID})
	must(t, err)
	t.Cleanup(func() { _ = fusemount.Unmount(f.fuse, true) })

	events := make(chan handoff.Event, 16)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = handoff.Serve(ctx, handoff.ServerConfig{
			SockPath: f.sock, FD: fd, Mode: handoff.ModeFresh, UID: mountPodID, GID: mountPodID,
		}, events)
	}()

	cmd := exec.Command(f.bin, "mount", "--spec", f.spec, "--socket", f.sock,
		"--cache-dir", f.cache, "--drain-timeout", "30s", "--unmount-wait", "10s")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: mountPodID, Gid: mountPodID}}
	for i := 0; i < 100; i++ {
		if _, err := os.Stat(f.sock); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	must(t, cmd.Start())
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	expect(t, events, handoff.EventFDSent, 10*time.Second)
	must(t, syscall.Close(fd)) // the kernel connection lives on in the Mount Pod
	expect(t, events, handoff.EventReady, 30*time.Second)

	// root writes as another uid than the server's: needs allow_other
	path := filepath.Join(f.fuse, "hello.txt")
	must(t, os.WriteFile(path, []byte("through fuse"), 0o644))
	got, err := os.ReadFile(path)
	must(t, err)
	if string(got) != "through fuse" {
		t.Fatalf("read back %q", got)
	}

	must(t, cmd.Process.Signal(syscall.SIGTERM))
	expect(t, events, handoff.EventDrained, 40*time.Second)
	remote, err := os.ReadFile(filepath.Join(f.remote, "hello.txt"))
	must(t, err)
	if string(remote) != "through fuse" {
		t.Fatalf("remote has %q", remote)
	}

	must(t, fusemount.Unmount(f.fuse, false))
	expect(t, events, handoff.EventClosed, 15*time.Second)
	must(t, cmd.Wait())
}
