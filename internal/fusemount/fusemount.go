// Package fusemount opens /dev/fuse and mounts it for a Mount Pod (ADR 0002). The
// node plugin calls it; the Mount Pod serves the fd it gets over the handoff socket.
package fusemount

import (
	"errors"
	"strconv"
	"strings"
)

// FSType keeps rclone's mount readiness check and mount listings consistent.
const FSType = "fuse.rclone"

// rootMode is S_IFDIR in octal, as the kernel expects it in the mount data.
const rootMode = "40000"

// ErrUnsupported is returned on platforms without Linux FUSE mount(2).
var ErrUnsupported = errors.New("fusemount: only supported on linux")

// Opts are the kernel-side mount settings. rclone's own mount flags do not reach the
// kernel with fd passing, so the plugin derives these from the Mount Spec.
type Opts struct {
	Source             string // device name shown in the mount table, e.g. the rclone fs path
	ReadOnly           bool
	UID, GID           int // the Mount Pod's uid/gid: the FUSE connection owner
	DefaultPermissions bool
}

// Data builds the mount(2) data string. allow_other is always set: without it only
// UID could access the mount and workload pods would get EACCES.
func Data(fd int, o Opts) string {
	parts := []string{
		"fd=" + strconv.Itoa(fd),
		"rootmode=" + rootMode,
		"user_id=" + strconv.Itoa(o.UID),
		"group_id=" + strconv.Itoa(o.GID),
		"allow_other",
	}
	if o.DefaultPermissions {
		parts = append(parts, "default_permissions")
	}
	return strings.Join(parts, ",")
}
