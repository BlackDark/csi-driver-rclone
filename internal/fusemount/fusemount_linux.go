//go:build linux

package fusemount

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// Flags returns the mount(2) flags for o.
func Flags(o Opts) uintptr {
	f := uintptr(unix.MS_NOSUID | unix.MS_NODEV)
	if o.ReadOnly {
		f |= unix.MS_RDONLY
	}
	return f
}

// Mount opens /dev/fuse and mounts it on target. The returned fd is the FUSE
// connection to hand to the Mount Pod; the caller closes its copy after the handoff.
func Mount(target string, o Opts) (int, error) {
	fd, err := unix.Open("/dev/fuse", unix.O_RDWR|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, fmt.Errorf("fusemount: open /dev/fuse: %w", err)
	}
	if err := unix.Mount(o.Source, target, FSType, Flags(o), Data(fd, o)); err != nil {
		_ = unix.Close(fd)
		return -1, fmt.Errorf("fusemount: mount %s: %w", target, err)
	}
	return fd, nil
}

// Unmount unmounts target; lazy detaches it even while busy or after the server died.
func Unmount(target string, lazy bool) error {
	flags := 0
	if lazy {
		flags = unix.MNT_DETACH
	}
	if err := unix.Unmount(target, flags); err != nil {
		return fmt.Errorf("fusemount: unmount %s: %w", target, err)
	}
	return nil
}
