package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestDispatch(t *testing.T) {
	if err := dispatch("controller", []string{"extra"}, io.Discard); err == nil {
		t.Error("controller with extra args accepted")
	}
	if err := dispatch("bogus", nil, io.Discard); !errors.Is(err, errUnknownMode) {
		t.Errorf("bogus: got %v, want unknown mode", err)
	}
	missing := filepath.Join(t.TempDir(), "spec.json")
	if err := dispatch("mount", []string{"--spec", missing}, io.Discard); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("mount without spec: got %v", err)
	}
}

func TestRunExitCodes(t *testing.T) {
	cases := map[string]struct {
		args []string
		want int
	}{
		"help":            {[]string{"--help"}, 0},
		"mode help":       {[]string{"node", "--help"}, 0},
		"mount help":      {[]string{"mount", "--help"}, 0},
		"no mode":         {nil, 2},
		"unknown":         {[]string{"bogus"}, 2},
		"controller help": {[]string{"controller", "--help"}, 0},
		"controller args": {[]string{"controller", "x"}, 1},
	}
	for name, c := range cases {
		if got := run(c.args, io.Discard); got != c.want {
			t.Errorf("%s: got %d, want %d", name, got, c.want)
		}
	}
}

func TestNodeFlags(t *testing.T) {
	t.Setenv("NODE_ID", "n1")
	t.Setenv("POD_NAMESPACE", "drv")
	f, err := parseNodeFlags([]string{"--mount-image=img", `--mount-tolerations=[{"operator":"Exists"}]`}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	p := f.cfg.Pod
	if f.cfg.NodeID != "n1" || p.Namespace != "drv" || p.UID != 65532 || len(p.Tolerations) != 1 ||
		p.KubeletDir != "/var/lib/kubelet" || f.cfg.PublishTimeout.String() != "2m0s" || f.cfg.Linger.String() != "2m0s" ||
		p.Resources.Requests.Memory().String() != "64Mi" {
		t.Errorf("flags: %+v", f.cfg)
	}
	for _, args := range [][]string{{}, {"--mount-image=img", "--mount-uid=0"}, {"--mount-image=img", "--mount-tolerations=x"}, {"--mount-image=img", "--mount-resources=x"}} {
		if _, err := parseNodeFlags(args, io.Discard); err == nil {
			t.Errorf("%v accepted", args)
		}
	}
}

func TestReadPasswordOptional(t *testing.T) {
	dir := t.TempDir()
	if pw, err := readPassword(filepath.Join(dir, "none")); err != nil || pw != "" {
		t.Fatalf("absent: %q %v", pw, err)
	}
	p := filepath.Join(dir, "pw")
	if err := os.WriteFile(p, []byte("s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if pw, err := readPassword(p); err != nil || pw != "s3cret" {
		t.Fatalf("present: %q %v", pw, err)
	}
}
