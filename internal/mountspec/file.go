package mountspec

import (
	"encoding/json"
	"fmt"
)

// SpecFileVersion is bumped on incompatible changes to File.
const SpecFileVersion = 1

// File is spec.json in the owned Secret: what a Mount Pod needs to run the mount.
// The config password travels as a separate Secret key, never in here.
type File struct {
	Version    int               `json:"version"`
	ConfigData string            `json:"configData"`
	FsPath     string            `json:"fsPath"`
	Options    map[string]string `json:"options"`
	Identity   string            `json:"identity"`
}

// Serialize renders spec.json for s, without the config password.
func (s Spec) Serialize() ([]byte, error) {
	return json.Marshal(File{
		Version:    SpecFileVersion,
		ConfigData: s.Remote.ConfigData,
		FsPath:     s.Remote.FsPath,
		Options:    s.Options,
		Identity:   s.Identity,
	})
}

// Load parses spec.json and attaches the separately stored config password.
// Only the fields a Mount Pod uses are restored.
func Load(data []byte, configPassword string) (Spec, error) {
	var f File
	if err := json.Unmarshal(data, &f); err != nil {
		return Spec{}, fmt.Errorf("mountspec: parse spec file: %w", err)
	}
	if f.Version != SpecFileVersion {
		return Spec{}, fmt.Errorf("mountspec: spec file version %d, want %d", f.Version, SpecFileVersion)
	}
	if f.ConfigData == "" || f.FsPath == "" {
		return Spec{}, fmt.Errorf("mountspec: spec file lacks configData or fsPath")
	}
	return Spec{
		Remote:         Remote{ConfigData: f.ConfigData, FsPath: f.FsPath},
		ConfigPassword: configPassword,
		Options:        f.Options,
		Identity:       f.Identity,
	}, nil
}
