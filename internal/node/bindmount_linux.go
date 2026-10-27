//go:build linux

package node

import "golang.org/x/sys/unix"

type realBindSys struct{}

func (realBindSys) bind(source, target string) error {
	return unix.Mount(source, target, "", unix.MS_BIND, "")
}

func (realBindSys) remountReadOnly(target string) error {
	return unix.Mount("", target, "", unix.MS_BIND|unix.MS_REMOUNT|unix.MS_RDONLY, "")
}

func (realBindSys) unmount(target string) error { return unix.Unmount(target, 0) }
