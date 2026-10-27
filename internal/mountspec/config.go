package mountspec

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
)

// isolatedName gives each volume its own namespace of remote names, so two volumes
// both defining "[s3]" never share rclone state on a node.
func isolatedName(name, volumeID string) string {
	sum := sha256.Sum256([]byte(volumeID))
	return name + "_" + hex.EncodeToString(sum[:4])
}

// sectionNames returns the remote names defined in an INI config, in order.
func sectionNames(configData string) ([]string, error) {
	var names []string
	seen := map[string]bool{}
	for _, raw := range strings.Split(configData, "\n") {
		name, ok := sectionHeader(raw)
		if !ok {
			continue
		}
		if name == "" || seen[name] {
			return nil, newErr(KeyConfigData, ReasonInvalidConfig, "empty or duplicate section name")
		}
		seen[name] = true
		names = append(names, name)
	}
	if len(names) == 0 {
		return nil, newErr(KeyConfigData, ReasonInvalidConfig, "no remote sections")
	}
	return names, nil
}

func sectionHeader(line string) (string, bool) {
	t := strings.TrimSpace(line)
	if !strings.HasPrefix(t, "[") || !strings.HasSuffix(t, "]") {
		return "", false
	}
	return strings.TrimSpace(t[1 : len(t)-1]), true
}

// isolateConfig renames every section and every "<name>:" reference in values
// (crypt remote, union upstreams, alias) to its isolated name.
func isolateConfig(configData string, rename map[string]string) string {
	lines := strings.Split(strings.ReplaceAll(configData, "\r\n", "\n"), "\n")
	for i, raw := range lines {
		if name, ok := sectionHeader(raw); ok {
			lines[i] = "[" + rename[name] + "]"
			continue
		}
		lines[i] = rewriteRefs(raw, rename)
	}
	return strings.Join(lines, "\n")
}

func rewriteRefs(line string, rename map[string]string) string {
	t := strings.TrimSpace(line)
	if t == "" || t[0] == '#' || t[0] == ';' {
		return line
	}
	key, value, ok := strings.Cut(line, "=")
	if !ok {
		return line
	}
	fields := strings.Fields(value)
	changed := false
	for i, f := range fields {
		name, rest, hasColon := strings.Cut(f, ":")
		if to, known := rename[name]; hasColon && known {
			fields[i] = to + ":" + rest
			changed = true
		}
	}
	if !changed {
		return line
	}
	return strings.TrimRight(key, " ") + " = " + strings.Join(fields, " ")
}

// shorthandConfig builds a one-section config from remoteType and backend keys in secrets.
func shorthandConfig(name, remoteType string, backend map[string]string) (string, error) {
	if strings.ContainsAny(name, "\r\n[]:") {
		return "", newErr(KeyRemote, ReasonInvalidValue, "remote name has forbidden characters")
	}
	keys := make([]string, 0, len(backend))
	for k, v := range backend {
		if k == "" || strings.ContainsAny(k, "\r\n[]= ") || strings.ContainsAny(v, "\r\n") {
			return "", newErr(k, ReasonInvalidValue, "backend keys and values must be single-line")
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString("[" + name + "]\ntype = " + remoteType + "\n")
	for _, k := range keys {
		b.WriteString(strings.ReplaceAll(k, "-", "_") + " = " + backend[k] + "\n")
	}
	return b.String(), nil
}
