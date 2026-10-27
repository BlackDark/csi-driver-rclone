package mountspec

import (
	"encoding/json"
	"fmt"
)

// SpecFileVersion is what Serialize writes. Version 2 adds the fields the node plugin
// needs to recreate a Mount Pod after adopting it; Load still reads version 1.
const SpecFileVersion = 2

const minSpecFileVersion = 1

// File is spec.json in the owned Secret: the Mount Spec without the config password,
// which travels as a separate Secret key.
type File struct {
	Version      int               `json:"version"`
	ConfigData   string            `json:"configData"`
	FsPath       string            `json:"fsPath"`
	Options      map[string]string `json:"options"`
	Identity     string            `json:"identity"`
	Unrestricted bool              `json:"unrestricted,omitempty"`
	Resources    *Resources        `json:"resources,omitempty"`
	CacheVolume  string            `json:"cacheVolume,omitempty"`
	Ephemeral    bool              `json:"ephemeral,omitempty"`
}

// Serialize renders spec.json for s, without the config password. ReadOnly is per
// publish and not part of the shared mount, so it is not stored.
func (s Spec) Serialize() ([]byte, error) {
	f := File{
		Version:      SpecFileVersion,
		ConfigData:   s.Remote.ConfigData,
		FsPath:       s.Remote.FsPath,
		Options:      s.Options,
		Identity:     s.Identity,
		Unrestricted: s.Unrestricted,
		CacheVolume:  s.CacheVolume,
		Ephemeral:    s.Mode == Ephemeral,
	}
	if s.Resources != (Resources{}) {
		f.Resources = &s.Resources
	}
	return json.Marshal(f)
}

// Load parses spec.json and attaches the separately stored config password.
func Load(data []byte, configPassword string) (Spec, error) {
	var f File
	if err := json.Unmarshal(data, &f); err != nil {
		return Spec{}, fmt.Errorf("mountspec: parse spec file: %w", err)
	}
	if f.Version < minSpecFileVersion || f.Version > SpecFileVersion {
		return Spec{}, fmt.Errorf("mountspec: spec file version %d, want %d to %d", f.Version, minSpecFileVersion, SpecFileVersion)
	}
	if f.ConfigData == "" || f.FsPath == "" {
		return Spec{}, fmt.Errorf("mountspec: spec file lacks configData or fsPath")
	}
	s := Spec{
		Remote:         Remote{ConfigData: f.ConfigData, FsPath: f.FsPath},
		ConfigPassword: configPassword,
		Options:        f.Options,
		Identity:       f.Identity,
		Unrestricted:   f.Unrestricted,
		CacheVolume:    f.CacheVolume,
	}
	if f.Resources != nil {
		s.Resources = *f.Resources
	}
	if f.Ephemeral {
		s.Mode = Ephemeral
	}
	return s, nil
}
