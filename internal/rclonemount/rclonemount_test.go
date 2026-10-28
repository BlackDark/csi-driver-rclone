package rclonemount

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config"
	"github.com/rclone/rclone/fs/rc"
	"github.com/rclone/rclone/vfs/vfscommon"
)

func TestBuildOptions(t *testing.T) {
	o, err := BuildOptions(map[string]string{
		"vfs-cache-mode": "writes", "attr-timeout": "2s", "transfers": "8", "allow-other": "true",
	})
	if err != nil {
		t.Fatal(err)
	}
	if o.VFS.CacheMode != vfscommon.CacheModeWrites {
		t.Errorf("cache mode %v", o.VFS.CacheMode)
	}
	if time.Duration(o.Mount.AttrTimeout) != 2*time.Second {
		t.Errorf("attr timeout %v", o.Mount.AttrTimeout)
	}
	if !o.Mount.AllowNonEmpty || !o.Mount.AllowOther || o.Mount.Daemon {
		t.Errorf("forced mount options not applied: %+v", o.Mount)
	}
	if o.Global["transfers"] != "8" {
		t.Errorf("global %v", o.Global)
	}
	if o.VFS.DirCacheTime != vfscommon.Opt.DirCacheTime {
		t.Errorf("defaults lost: dir cache time %v", o.VFS.DirCacheTime)
	}
}

func TestBuildOptionsRejects(t *testing.T) {
	for name, in := range map[string]map[string]string{
		"unknown":   {"no-such-flag": "1"},
		"bad value": {"vfs-cache-mode": "bogus"},
		"bad int":   {"transfers": "many"},
	} {
		if _, err := BuildOptions(in); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func TestPendingUploads(t *testing.T) {
	if n := pendingUploads(rc.Params{}); n != 0 {
		t.Errorf("no cache: %d", n)
	}
	s := rc.Params{"diskCache": rc.Params{"uploadsInProgress": 2, "uploadsQueued": 3}}
	if n := pendingUploads(s); n != 5 {
		t.Errorf("got %d", n)
	}
}

func TestDrain(t *testing.T) {
	calls := 0
	settles := func() rc.Params {
		calls++
		if calls < 3 {
			return rc.Params{"diskCache": rc.Params{"uploadsQueued": 1}}
		}
		return rc.Params{}
	}
	if !drain(context.Background(), settles, time.Second, time.Millisecond) {
		t.Error("want drained")
	}
	stuck := func() rc.Params { return rc.Params{"diskCache": rc.Params{"uploadsInProgress": 1}} }
	if drain(context.Background(), stuck, 20*time.Millisecond, time.Millisecond) {
		t.Error("want timeout")
	}
}

func TestLoadConfigInMemory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := LoadConfig("[cfgtest]\ntype = local\n"); err != nil {
		t.Fatal(err)
	}
	f, err := fs.NewFs(context.Background(), "cfgtest:"+dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.NewObject(context.Background(), "f"); err != nil {
		t.Fatalf("object via in-memory remote: %v", err)
	}
	if !config.LoadedData().HasSection("cfgtest") {
		t.Error("section missing from in-memory storage")
	}
}
