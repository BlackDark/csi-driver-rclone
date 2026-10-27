package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestDispatch(t *testing.T) {
	for _, m := range []string{"node", "controller"} {
		if err := dispatch(m, nil, io.Discard); !errors.Is(err, errNotImplemented) {
			t.Errorf("%s: got %v, want not implemented", m, err)
		}
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
		"help":       {[]string{"--help"}, 0},
		"mode help":  {[]string{"node", "--help"}, 0},
		"mount help": {[]string{"mount", "--help"}, 0},
		"no mode":    {nil, 2},
		"unknown":    {[]string{"bogus"}, 2},
		"controller": {[]string{"controller"}, 1},
	}
	for name, c := range cases {
		if got := run(c.args, io.Discard); got != c.want {
			t.Errorf("%s: got %d, want %d", name, got, c.want)
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
