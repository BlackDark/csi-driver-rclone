//go:build !linux

package node

import "errors"

var errNotLinux = errors.New("bind mounts need linux")

type realBindSys struct{}

func (realBindSys) bind(string, string) error    { return errNotLinux }
func (realBindSys) remountReadOnly(string) error { return errNotLinux }
func (realBindSys) unmount(string) error         { return errNotLinux }
