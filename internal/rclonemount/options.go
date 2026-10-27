// Package rclonemount runs one rclone mount2 in-process for a Mount Pod: config from
// the Mount Spec, the FUSE fd from the handoff socket, drain on stop.
package rclonemount

import (
	"fmt"
	"sort"
	"strings"

	"github.com/rclone/rclone/cmd/mountlib"
	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config/configmap"
	"github.com/rclone/rclone/fs/config/configstruct"
	"github.com/rclone/rclone/vfs/vfscommon"
)

// Options are Mount Spec options split by the rclone option group they belong to.
type Options struct {
	Mount  mountlib.Options
	VFS    vfscommon.Options
	Global configmap.Simple // main options (transfers, bwlimit, log_level, ...), applied to fs.ConfigInfo
}

// forcedMount are mount settings fd passing depends on (ADR 0002): rclone must not
// list /dev/fd/N as a directory, must serve other uids, and must not daemonize.
var forcedMount = map[string]string{"allow_non_empty": "true", "allow_other": "true", "daemon": "false"}

// BuildOptions maps Mount Spec option keys ("vfs-cache-mode") to rclone options,
// on top of rclone's defaults. Unknown keys and bad values are errors.
func BuildOptions(spec map[string]string) (Options, error) {
	mount, vfs, global := configmap.Simple{}, configmap.Simple{}, configmap.Simple{}
	for _, k := range sortedKeys(spec) {
		name := strings.ReplaceAll(k, "-", "_")
		switch {
		case mountlib.OptionsInfo.Get(name) != nil:
			mount[name] = spec[k]
		case vfscommon.OptionsInfo.Get(name) != nil:
			vfs[name] = spec[k]
		case fs.ConfigOptionsInfo.Get(name) != nil:
			global[name] = spec[k]
		default:
			return Options{}, fmt.Errorf("rclonemount: option %q is not a mount, vfs or global rclone option", k)
		}
	}
	for k, v := range forcedMount {
		mount[k] = v
	}
	var out Options
	if err := configstruct.Set(fs.ConfigMap("", mountlib.OptionsInfo, "", mount), &out.Mount); err != nil {
		return Options{}, fmt.Errorf("rclonemount: mount options: %w", err)
	}
	if err := configstruct.Set(fs.ConfigMap("", vfscommon.OptionsInfo, "", vfs), &out.VFS); err != nil {
		return Options{}, fmt.Errorf("rclonemount: vfs options: %w", err)
	}
	// Validate global values now; they are applied to the live config in Run.
	var probe fs.ConfigInfo
	if err := configstruct.Set(global, &probe); err != nil {
		return Options{}, fmt.Errorf("rclonemount: global options: %w", err)
	}
	out.Global = global
	return out, nil
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
