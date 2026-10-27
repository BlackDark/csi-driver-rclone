// Package mountspec turns CSI inputs into a validated Mount Spec. It is pure: no I/O.
package mountspec

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
)

// Mode is the volume lifecycle mode.
type Mode int

// Lifecycle modes.
const (
	Persistent Mode = iota // Static and Dynamic Volumes
	Ephemeral              // Inline Volumes
)

// Volume context keys.
const (
	KeyRemote          = "remote"
	KeyRemotePath      = "remotePath"
	KeyAllowAllOptions = "allowAllOptions"
	KeyCacheVolume     = "cacheVolume"
	KeyCPURequest      = "mountPod.cpuRequest"
	KeyCPULimit        = "mountPod.cpuLimit"
	KeyMemoryRequest   = "mountPod.memoryRequest"
	KeyMemoryLimit     = "mountPod.memoryLimit"
)

// Secret keys.
const (
	KeyConfigData     = "configData"
	KeyConfigPassword = "configPassword"
	KeyRemoteType     = "remoteType"
)

// DefaultMemoryRequest applies when no memory request is set (docs/scope.md).
const DefaultMemoryRequest = "64Mi"

const shorthandRemoteName = "remote"

// Input is everything Resolve needs from a CSI request.
type Input struct {
	Secrets       map[string]string
	VolumeContext map[string]string
	VolumeID      string
	ReadOnly      bool // per-publish; does not change the shared mount
	Mode          Mode
}

// Remote is an isolated rclone config plus the fs path to mount.
type Remote struct {
	ConfigData string
	FsPath     string // "<isolatedRemote>:<path>"
}

// Resources are Kubernetes quantity strings for the Mount Pod; empty means unset.
type Resources struct {
	CPURequest, CPULimit, MemoryRequest, MemoryLimit string
}

// Spec is a validated Mount Spec.
type Spec struct {
	Remote         Remote
	ConfigPassword string
	Options        map[string]string // normalized flag name -> value
	Unrestricted   bool
	Resources      Resources
	CacheVolume    string // raw JSON volume source, empty for hostPath default
	ReadOnly       bool
	Identity       string // hex sha256 of everything that defines the shared mount
}

// Resolve validates the inputs and builds a Spec, or returns an *Error matching ErrInvalid.
func Resolve(in Input) (Spec, error) {
	if in.VolumeID == "" {
		return Spec{}, newErr("volumeID", ReasonMissing, "")
	}
	ctx, opts, err := splitContext(in.VolumeContext)
	if err != nil {
		return Spec{}, err
	}
	unrestricted, err := parseUnrestricted(ctx[KeyAllowAllOptions], in.Mode)
	if err != nil {
		return Spec{}, err
	}
	if in.Mode == Ephemeral {
		if err := rejectAdminKeys(ctx); err != nil {
			return Spec{}, err
		}
	}
	remote, err := resolveRemote(in.Secrets, ctx, in.VolumeID)
	if err != nil {
		return Spec{}, err
	}
	options, err := resolveOptions(opts, in.Mode, unrestricted)
	if err != nil {
		return Spec{}, err
	}
	cache, err := validateCacheVolume(ctx[KeyCacheVolume])
	if err != nil {
		return Spec{}, err
	}
	s := Spec{
		Remote:         remote,
		ConfigPassword: in.Secrets[KeyConfigPassword],
		Options:        options,
		Unrestricted:   unrestricted,
		Resources:      resolveResources(ctx),
		CacheVolume:    cache,
		ReadOnly:       in.ReadOnly,
	}
	s.Identity = identity(in.VolumeID, s)
	return s, nil
}

var reservedContextKeys = map[string]bool{
	KeyRemote: true, KeyRemotePath: true, KeyAllowAllOptions: true, KeyCacheVolume: true,
	KeyCPURequest: true, KeyCPULimit: true, KeyMemoryRequest: true, KeyMemoryLimit: true,
}

var credentialHints = []string{"password", "pass", "secret", "token", "key", "credential"}

// splitContext separates driver keys from Mount Option keys. Keys with "/" are
// Kubernetes-injected (csi.storage.k8s.io/pod.name, ...) and ignored.
func splitContext(vc map[string]string) (map[string]string, map[string]string, error) {
	ctx, opts := map[string]string{}, map[string]string{}
	for k, v := range vc {
		switch {
		case strings.Contains(k, "/"):
		case reservedContextKeys[k]:
			ctx[k] = v
		case isCredentialKey(k):
			return nil, nil, newErr(k, ReasonCredentialInContext, "use the Credential Secret")
		default:
			opts[k] = v
		}
	}
	return ctx, opts, nil
}

func isCredentialKey(k string) bool {
	if k == KeyConfigData || k == KeyConfigPassword || k == KeyRemoteType {
		return true
	}
	lk := strings.ToLower(k)
	for _, h := range credentialHints {
		if strings.Contains(lk, h) {
			return true
		}
	}
	return false
}

func parseUnrestricted(v string, mode Mode) (bool, error) {
	switch v {
	case "", "false":
		return false, nil
	case "true":
		if mode == Ephemeral {
			return false, newErr(KeyAllowAllOptions, ReasonNotForEphemeral, "")
		}
		return true, nil
	}
	return false, newErr(KeyAllowAllOptions, ReasonInvalidValue, "want true or false")
}

// rejectAdminKeys keeps node-level knobs (cache placement, Mount Pod resources) away from pod authors.
func rejectAdminKeys(ctx map[string]string) error {
	for _, k := range []string{KeyCacheVolume, KeyCPURequest, KeyCPULimit, KeyMemoryRequest, KeyMemoryLimit} {
		if _, ok := ctx[k]; ok {
			return newErr(k, ReasonNotForEphemeral, "")
		}
	}
	return nil
}

func resolveResources(ctx map[string]string) Resources {
	r := Resources{
		CPURequest:    ctx[KeyCPURequest],
		CPULimit:      ctx[KeyCPULimit],
		MemoryRequest: ctx[KeyMemoryRequest],
		MemoryLimit:   ctx[KeyMemoryLimit],
	}
	if r.MemoryRequest == "" {
		r.MemoryRequest = DefaultMemoryRequest
	}
	return r
}

func validateCacheVolume(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &obj); err != nil || obj == nil {
		return "", newErr(KeyCacheVolume, ReasonInvalidValue, "want a JSON object volume source")
	}
	return raw, nil
}

// identity excludes the per-publish ReadOnly so read-only and read-write pods share one Mount Pod.
// Secrets are hashed together with the rest; the result is a fingerprint, not a lookup key.
func identity(volumeID string, s Spec) string {
	canon, err := json.Marshal(struct {
		VolumeID, ConfigData, FsPath, ConfigPassword, CacheVolume string
		Options                                                   map[string]string
		Unrestricted                                              bool
		Resources                                                 Resources
	}{volumeID, s.Remote.ConfigData, s.Remote.FsPath, s.ConfigPassword, s.CacheVolume,
		s.Options, s.Unrestricted, s.Resources})
	if err != nil {
		panic(err) // only strings, maps of strings and bools: cannot fail
	}
	sum := sha256.Sum256(canon)
	return hex.EncodeToString(sum[:])
}
