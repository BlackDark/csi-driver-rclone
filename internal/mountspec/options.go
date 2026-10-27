package mountspec

import (
	"strings"
)

// allowlist holds generic rclone mount flags; a trailing "*" matches by prefix.
var allowlist = []string{
	"vfs-cache-mode", "vfs-cache-max-size", "vfs-cache-min-free-space", "vfs-cache-max-age",
	"vfs-cache-poll-interval", "vfs-read-ahead", "vfs-read-chunk-size*", "vfs-write-back",
	"vfs-fast-fingerprint",
	"dir-cache-time", "poll-interval", "attr-timeout", "buffer-size",
	"tpslimit", "tpslimit-burst", "bwlimit", "bwlimit-file",
	"transfers", "checkers", "retries", "low-level-retries", "timeout", "contimeout",
	"user-agent", "uid", "gid", "umask", "dir-perms", "file-perms",
	"read-only", "log-level", "no-modtime", "no-checksum",
}

// ephemeralAllowlist is the Inline Volume subset: options that shape only the pod
// author's own view of the mount. Rate limits, transfers, retries, user-agent,
// write-back and cache free-space/polling stay with admins.
var ephemeralAllowlist = []string{
	"read-only", "vfs-cache-mode", "vfs-cache-max-size", "vfs-cache-max-age",
	"vfs-read-ahead", "vfs-read-chunk-size*", "buffer-size", "dir-cache-time",
	"poll-interval", "attr-timeout", "uid", "gid", "umask", "dir-perms", "file-perms",
	"log-level", "no-modtime", "no-checksum",
}

// alwaysRejected applies even under Unrestricted Options: these would escape the
// Mount Pod's control (log/cache paths, config file, rc server, daemonizing).
var alwaysRejected = []string{"log-file", "rc*", "daemon*", "cache-dir", "config"}

var vfsCacheModes = map[string]bool{"off": true, "minimal": true, "writes": true, "full": true}

const forcedAllowOther = "allow-other"

// normalizeOptionKey accepts "--vfs_cache_mode", "vfs_cache_mode" or "vfs-cache-mode".
func normalizeOptionKey(k string) string {
	return strings.ReplaceAll(strings.TrimLeft(strings.ToLower(k), "-"), "_", "-")
}

func matches(patterns []string, key string) bool {
	for _, p := range patterns {
		if prefix, ok := strings.CutSuffix(p, "*"); ok {
			if strings.HasPrefix(key, prefix) {
				return true
			}
		} else if key == p {
			return true
		}
	}
	return false
}

// resolveOptions validates and normalizes option keys taken from the volume context.
func resolveOptions(raw map[string]string, mode Mode, unrestricted bool) (map[string]string, error) {
	out := map[string]string{}
	for orig, v := range raw {
		k := normalizeOptionKey(orig)
		if err := checkOptionKey(orig, k, mode, unrestricted); err != nil {
			return nil, err
		}
		if _, dup := out[k]; dup {
			return nil, newErr(orig, ReasonConflict, "option given more than once")
		}
		if k == "vfs-cache-mode" && !vfsCacheModes[v] {
			return nil, newErr(orig, ReasonInvalidValue, "want off, minimal, writes or full")
		}
		out[k] = v
	}
	out[forcedAllowOther] = "true"
	return out, nil
}

func checkOptionKey(orig, k string, mode Mode, unrestricted bool) error {
	switch {
	case matches(alwaysRejected, k):
		return newErr(orig, ReasonRejectedOption, "")
	case k == forcedAllowOther:
		return nil
	case mode == Ephemeral && !matches(ephemeralAllowlist, k):
		return newErr(orig, ReasonNotForEphemeral, "")
	case !unrestricted && !matches(allowlist, k):
		return newErr(orig, ReasonUnknownOption, "")
	}
	return nil
}
