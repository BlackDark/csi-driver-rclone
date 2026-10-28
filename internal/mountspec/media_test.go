package mountspec

import (
	"reflect"
	"testing"
)

// The media-stack migration example in docs/guides/migrate-from-systemd.md.
func TestMediaStack(t *testing.T) {
	const configData = `[onedrive]
type = onedrive
token = {"access_token":"x"}
drive_id = abc
chunk_size = 5M
delta = true

[media]
type = crypt
remote = onedrive:crypt
password = obscured
`
	options := map[string]string{
		"vfs-cache-mode": "full", "vfs-cache-max-size": "70G", "vfs-cache-min-free-space": "15G",
		"vfs-cache-max-age": "24h", "vfs-write-back": "5m", "vfs-read-ahead": "128M",
		"buffer-size": "64M", "dir-cache-time": "5000h", "poll-interval": "10s",
		"tpslimit": "8", "tpslimit-burst": "8", "bwlimit-file": "25M:off", "transfers": "1",
		"retries": "10", "low-level-retries": "20", "timeout": "1h", "contimeout": "60s",
		"uid": "1000", "gid": "1000", "umask": "002", "user-agent": "MediaArr",
	}
	vc := map[string]string{KeyRemote: "media"}
	for k, v := range options {
		vc[k] = v
	}
	got, err := Resolve(Input{
		Secrets:       map[string]string{KeyConfigData: configData, KeyConfigPassword: "pw"},
		VolumeContext: vc,
		VolumeID:      "media",
	})
	if err != nil {
		t.Fatal(err)
	}
	od, media := isolatedName("onedrive", "media"), isolatedName("media", "media")
	wantConfig := "[" + od + `]
type = onedrive
token = {"access_token":"x"}
drive_id = abc
chunk_size = 5M
delta = true

[` + media + `]
type = crypt
remote = ` + od + `:crypt
password = obscured
`
	wantOptions := map[string]string{"allow-other": "true"}
	for k, v := range options {
		wantOptions[k] = v
	}
	want := Spec{
		Remote:         Remote{ConfigData: wantConfig, FsPath: media + ":"},
		ConfigPassword: "pw",
		Options:        wantOptions,
		Identity:       got.Identity,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}
	if len(got.Identity) != 64 {
		t.Errorf("identity %q", got.Identity)
	}
}
