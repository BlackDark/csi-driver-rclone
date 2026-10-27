package mountspec

import (
	"reflect"
	"strings"
	"testing"
)

func TestSerializeLoadRoundTrip(t *testing.T) {
	s, err := Resolve(Input{
		VolumeID: "vol",
		Secrets:  map[string]string{KeyConfigData: "[r]\ntype = local\n", KeyConfigPassword: "hunter2"},
		VolumeContext: map[string]string{
			KeyRemotePath: "data", "vfs-cache-mode": "writes", KeyAllowAllOptions: "true",
			KeyCacheVolume: `{"emptyDir":{}}`, KeyCPULimit: "1",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Serialize()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "hunter2") {
		t.Fatal("spec.json leaks the config password")
	}
	got, err := Load(b, "pw")
	if err != nil {
		t.Fatal(err)
	}
	want := s
	want.ConfigPassword = "pw"
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestLoadReadsVersion1(t *testing.T) {
	got, err := Load([]byte(`{"version":1,"configData":"[r]","fsPath":"r:","options":{"a":"b"},"identity":"x"}`), "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Remote.FsPath != "r:" || got.Identity != "x" || got.Mode != Persistent || got.Resources != (Resources{}) {
		t.Fatalf("got %+v", got)
	}
}

func TestEphemeralRoundTrip(t *testing.T) {
	s, err := Resolve(Input{
		VolumeID: "csi-abc", Mode: Ephemeral,
		Secrets:       map[string]string{KeyConfigData: "[r]\ntype = local\n"},
		VolumeContext: map[string]string{KeyRemotePath: "data"},
	})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := s.Serialize()
	got, err := Load(b, "")
	if err != nil || !reflect.DeepEqual(got, s) {
		t.Fatalf("got %+v, %v; want %+v", got, err, s)
	}
}

func TestLoadRejects(t *testing.T) {
	for name, in := range map[string]string{
		"not json":      "{",
		"wrong version": `{"version":3,"configData":"[r]","fsPath":"r:"}`,
		"empty":         `{"version":1}`,
	} {
		if _, err := Load([]byte(in), ""); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}
