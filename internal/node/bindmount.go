package node

import (
	"fmt"

	"k8s.io/mount-utils"
)

// BindMounter replaces mount-utils' Mount and Unmount, which exec mount(8) and
// umount(8), with syscalls so the image stays distroless. Mount point checks still
// come from the embedded mount-utils (stat and /proc/self/mountinfo).
type BindMounter struct {
	mount.Interface
	sys bindSys
}

// bindSys is the mount(2)/umount(2) surface; flags are opaque to keep this portable.
type bindSys interface {
	bind(source, target string) error
	remountReadOnly(target string) error
	unmount(target string) error
}

// NewBindMounter returns a BindMounter on the real syscalls.
func NewBindMounter() *BindMounter {
	return &BindMounter{Interface: mount.New(""), sys: realBindSys{}}
}

// bindOptions accepts only what NodePublishVolume passes: bind, optionally ro.
func bindOptions(fstype string, options []string) (readOnly bool, err error) {
	bind := false
	for _, o := range options {
		switch o {
		case "bind":
			bind = true
		case "ro":
			readOnly = true
		case "rw":
		default:
			return false, fmt.Errorf("unsupported mount option %q", o)
		}
	}
	if !bind || (fstype != "" && fstype != "none") {
		return false, fmt.Errorf("only bind mounts are supported")
	}
	return readOnly, nil
}

// Mount bind-mounts source on target; ro needs a second remount (bind ignores MS_RDONLY).
func (b *BindMounter) Mount(source, target, fstype string, options []string) error {
	ro, err := bindOptions(fstype, options)
	if err != nil {
		return err
	}
	if err := b.sys.bind(source, target); err != nil {
		return fmt.Errorf("bind %s on %s: %w", source, target, err)
	}
	if !ro {
		return nil
	}
	if err := b.sys.remountReadOnly(target); err != nil {
		_ = b.sys.unmount(target)
		return fmt.Errorf("remount %s read-only: %w", target, err)
	}
	return nil
}

// MountSensitive ignores sensitive options: bind mounts take none.
func (b *BindMounter) MountSensitive(source, target, fstype string, options, _ []string) error {
	return b.Mount(source, target, fstype, options)
}

// Unmount unmounts target.
func (b *BindMounter) Unmount(target string) error {
	if err := b.sys.unmount(target); err != nil {
		return fmt.Errorf("unmount %s: %w", target, err)
	}
	return nil
}
