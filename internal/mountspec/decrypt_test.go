package mountspec

import (
	"errors"
	"strings"
	"testing"

	"github.com/rclone/rclone/fs/config"
)

// encryptWithRclone produces a config exactly as `rclone config encryption set` would.
func encryptWithRclone(t *testing.T, plain, password string) string {
	t.Helper()
	if err := config.SetConfigPassword(password); err != nil {
		t.Fatal(err)
	}
	defer config.ClearConfigPassword()
	var b strings.Builder
	if err := config.Encrypt(strings.NewReader(plain), &b); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func TestEncryptedConfigData(t *testing.T) {
	plain := "[s3]\ntype = s3\n\n[media]\ntype = crypt\nremote = s3:bucket/crypt\n"
	enc := encryptWithRclone(t, plain, "hunter2")
	vc := map[string]string{KeyRemote: "media"}

	got, err := Resolve(Input{Secrets: map[string]string{KeyConfigData: enc, KeyConfigPassword: "hunter2"}, VolumeContext: vc, VolumeID: "v"})
	if err != nil {
		t.Fatal(err)
	}
	s3, media := isolatedName("s3", "v"), isolatedName("media", "v")
	if want := "[" + s3 + "]\ntype = s3\n\n[" + media + "]\ntype = crypt\nremote = " + s3 + ":bucket/crypt\n"; got.Remote.ConfigData != want {
		t.Errorf("config %q, want %q", got.Remote.ConfigData, want)
	}
	if got.Remote.FsPath != media+":" {
		t.Errorf("fs path %q", got.Remote.FsPath)
	}

	for name, tc := range map[string]struct {
		secrets map[string]string
		key     string
	}{
		"no password":    {map[string]string{KeyConfigData: enc}, KeyConfigPassword},
		"wrong password": {map[string]string{KeyConfigData: enc, KeyConfigPassword: "nope"}, KeyConfigPassword},
		"garbage":        {map[string]string{KeyConfigData: encryptedHeader + "\n!!", KeyConfigPassword: "x"}, KeyConfigData},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Resolve(Input{Secrets: tc.secrets, VolumeContext: vc, VolumeID: "v"})
			var e *Error
			if !errors.As(err, &e) || e.Key != tc.key {
				t.Fatalf("err %v, want key %q", err, tc.key)
			}
			if strings.Contains(err.Error(), "hunter2") {
				t.Error("error leaks password")
			}
		})
	}
}
