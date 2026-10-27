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
			KeyRemotePath: "data", "vfs-cache-mode": "writes",
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
	want := Spec{Remote: s.Remote, ConfigPassword: "pw", Options: s.Options, Identity: s.Identity}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestLoadRejects(t *testing.T) {
	for name, in := range map[string]string{
		"not json":      "{",
		"wrong version": `{"version":2,"configData":"[r]","fsPath":"r:"}`,
		"empty":         `{"version":1}`,
	} {
		if _, err := Load([]byte(in), ""); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}
