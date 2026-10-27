//go:build linux

package fusemount

import (
	"testing"

	"golang.org/x/sys/unix"
)

func TestFlags(t *testing.T) {
	base := uintptr(unix.MS_NOSUID | unix.MS_NODEV)
	if got := Flags(Opts{}); got != base {
		t.Errorf("rw: got %#x", got)
	}
	if got := Flags(Opts{ReadOnly: true}); got != base|unix.MS_RDONLY {
		t.Errorf("ro: got %#x", got)
	}
}
