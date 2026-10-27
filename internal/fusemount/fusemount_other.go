//go:build !linux

package fusemount

// Mount always fails off Linux.
func Mount(string, Opts) (int, error) { return -1, ErrUnsupported }

// Unmount always fails off Linux.
func Unmount(string, bool) error { return ErrUnsupported }
