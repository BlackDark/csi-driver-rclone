package main

import (
	"errors"
	"io"
	"testing"
)

func TestDispatch(t *testing.T) {
	for _, m := range modes {
		if err := dispatch(m, nil, io.Discard); !errors.Is(err, errNotImplemented) {
			t.Errorf("%s: got %v, want not implemented", m, err)
		}
	}
	if err := dispatch("bogus", nil, io.Discard); !errors.Is(err, errUnknownMode) {
		t.Errorf("bogus: got %v, want unknown mode", err)
	}
}

func TestRunExitCodes(t *testing.T) {
	cases := map[string]struct {
		args []string
		want int
	}{
		"help":       {[]string{"--help"}, 0},
		"mode help":  {[]string{"node", "--help"}, 0},
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
