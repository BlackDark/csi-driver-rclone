//go:build !linux

package node

// Statfs is Linux only.
func Statfs(string) (Usage, error) { return Usage{}, errNotLinux }
