package mountspec

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

const secretValue = "hunter2-SECRET-VALUE"

func s3Secrets() map[string]string {
	return map[string]string{
		KeyRemoteType:       "s3",
		"provider":          "Minio",
		"access_key_id":     "AKIA",
		"secret_access_key": secretValue,
	}
}

func TestResolveErrors(t *testing.T) {
	cfg := map[string]string{KeyConfigData: "[a]\ntype = local\n"}
	tests := []struct {
		name   string
		in     Input
		key    string
		reason Reason
	}{
		{"configData in context", Input{Secrets: cfg, VolumeContext: map[string]string{"configData": secretValue}}, "configData", ReasonCredentialInContext},
		{"configPassword in context", Input{Secrets: cfg, VolumeContext: map[string]string{"configPassword": secretValue}}, "configPassword", ReasonCredentialInContext},
		{"remoteType in context", Input{Secrets: cfg, VolumeContext: map[string]string{"remoteType": "s3"}}, "remoteType", ReasonCredentialInContext},
		{"credential-like key", Input{Secrets: cfg, VolumeContext: map[string]string{"s3-secret-access-key": secretValue}}, "s3-secret-access-key", ReasonCredentialInContext},
		{"token key", Input{Secrets: cfg, VolumeContext: map[string]string{"Token": secretValue}}, "Token", ReasonCredentialInContext},
		{"unknown option", Input{Secrets: cfg, VolumeContext: map[string]string{"onedrive-chunk-size": "5M"}}, "onedrive-chunk-size", ReasonUnknownOption},
		{"log-file", Input{Secrets: cfg, VolumeContext: map[string]string{"log-file": "/x"}}, "log-file", ReasonRejectedOption},
		{"rc-addr unrestricted", Input{Secrets: cfg, VolumeContext: map[string]string{"allowAllOptions": "true", "rc-addr": ":5572"}}, "rc-addr", ReasonRejectedOption},
		{"rc", Input{Secrets: cfg, VolumeContext: map[string]string{"rc": "true"}}, "rc", ReasonRejectedOption},
		{"daemon underscore", Input{Secrets: cfg, VolumeContext: map[string]string{"allowAllOptions": "true", "daemon_timeout": "1m"}}, "daemon_timeout", ReasonRejectedOption},
		{"cache-dir", Input{Secrets: cfg, VolumeContext: map[string]string{"allowAllOptions": "true", "cache_dir": "/"}}, "cache_dir", ReasonRejectedOption},
		{"config", Input{Secrets: cfg, VolumeContext: map[string]string{"allowAllOptions": "true", "--config": "/x"}}, "--config", ReasonRejectedOption},
		{"allowAllOptions ephemeral", Input{Secrets: cfg, Mode: Ephemeral, VolumeContext: map[string]string{"allowAllOptions": "true"}}, KeyAllowAllOptions, ReasonNotForEphemeral},
		{"allowAllOptions bad", Input{Secrets: cfg, VolumeContext: map[string]string{"allowAllOptions": "yes"}}, KeyAllowAllOptions, ReasonInvalidValue},
		{"ephemeral non-subset", Input{Secrets: cfg, Mode: Ephemeral, VolumeContext: map[string]string{"tpslimit": "8"}}, "tpslimit", ReasonNotForEphemeral},
		{"ephemeral cacheVolume", Input{Secrets: cfg, Mode: Ephemeral, VolumeContext: map[string]string{KeyCacheVolume: `{"hostPath":{"path":"/"}}`}}, KeyCacheVolume, ReasonNotForEphemeral},
		{"ephemeral resources", Input{Secrets: cfg, Mode: Ephemeral, VolumeContext: map[string]string{KeyMemoryLimit: "1Gi"}}, KeyMemoryLimit, ReasonNotForEphemeral},
		{"bad vfs-cache-mode", Input{Secrets: cfg, VolumeContext: map[string]string{"vfs-cache-mode": "most"}}, "vfs-cache-mode", ReasonInvalidValue},
		{"duplicate option", Input{Secrets: cfg, VolumeContext: map[string]string{"vfs-cache-mode": "full", "vfs_cache_mode": "off"}}, "", ReasonConflict},
		{"cacheVolume not object", Input{Secrets: cfg, VolumeContext: map[string]string{KeyCacheVolume: "[1]"}}, KeyCacheVolume, ReasonInvalidValue},
		{"no secret", Input{}, KeyConfigData, ReasonMissing},
		{"configData and remoteType", Input{Secrets: map[string]string{KeyConfigData: "[a]\ntype=local", KeyRemoteType: "s3"}}, KeyRemoteType, ReasonConflict},
		{"configData plus backend keys", Input{Secrets: map[string]string{KeyConfigData: "[a]\ntype=local", "token": secretValue}}, KeyConfigData, ReasonConflict},
		{"configData no sections", Input{Secrets: map[string]string{KeyConfigData: "type = local"}}, KeyConfigData, ReasonInvalidConfig},
		{"several remotes no pick", Input{Secrets: map[string]string{KeyConfigData: "[a]\n[b]\n"}}, KeyRemote, ReasonMissing},
		{"remote not defined", Input{Secrets: cfg, VolumeContext: map[string]string{KeyRemote: "zzz"}}, KeyRemote, ReasonInvalidValue},
		{"remotePath twice", Input{Secrets: map[string]string{KeyConfigData: "[a]", KeyRemotePath: "x"}, VolumeContext: map[string]string{KeyRemotePath: "y"}}, KeyRemotePath, ReasonConflict},
		{"multiline backend value", Input{Secrets: map[string]string{KeyRemoteType: "s3", "access_key_id": secretValue + "\n[evil]"}}, "access_key_id", ReasonInvalidValue},
		{"shorthand remote injection", Input{Secrets: s3Secrets(), VolumeContext: map[string]string{KeyRemote: "x]\n[y"}}, KeyRemote, ReasonInvalidValue},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.in.VolumeID == "" {
				tt.in.VolumeID = "vol-1"
			}
			_, err := Resolve(tt.in)
			var e *Error
			if !errors.As(err, &e) {
				t.Fatalf("want *Error, got %v", err)
			}
			if !errors.Is(err, ErrInvalid) {
				t.Errorf("errors.Is(err, ErrInvalid) = false")
			}
			if e.Reason != tt.reason || (tt.key != "" && e.Key != tt.key) {
				t.Errorf("got key %q reason %q, want %q %q", e.Key, e.Reason, tt.key, tt.reason)
			}
			if tt.key != "" && !strings.Contains(err.Error(), tt.key) {
				t.Errorf("message %q does not name key %q", err, tt.key)
			}
			if strings.Contains(err.Error(), secretValue) || strings.Contains(err.Error(), "most") {
				t.Errorf("message leaks a value: %q", err)
			}
		})
	}
}

func TestResolveOptions(t *testing.T) {
	cfg := map[string]string{KeyConfigData: "[a]\ntype = local\n"}
	tests := []struct {
		name string
		mode Mode
		vc   map[string]string
		want map[string]string
	}{
		{"forced allow-other", Persistent, nil, map[string]string{"allow-other": "true"}},
		{"allow-other false overridden", Persistent, map[string]string{"allow_other": "false"}, map[string]string{"allow-other": "true"}},
		{"underscore and dashes normalized", Persistent, map[string]string{"vfs_cache_mode": "full", "--dir-cache-time": "1h", "vfs-read-chunk-size-limit": "1G"},
			map[string]string{"vfs-cache-mode": "full", "dir-cache-time": "1h", "vfs-read-chunk-size-limit": "1G", "allow-other": "true"}},
		{"unrestricted lifts allowlist", Persistent, map[string]string{"allowAllOptions": "true", "onedrive-chunk-size": "5M"},
			map[string]string{"onedrive-chunk-size": "5M", "allow-other": "true"}},
		{"ephemeral subset", Ephemeral, map[string]string{"vfs-cache-mode": "writes", "uid": "1000", "read-only": "true"},
			map[string]string{"vfs-cache-mode": "writes", "uid": "1000", "read-only": "true", "allow-other": "true"}},
		{"k8s-injected keys ignored", Ephemeral, map[string]string{"csi.storage.k8s.io/ephemeral": "true", "csi.storage.k8s.io/serviceAccount.tokens": "{}"},
			map[string]string{"allow-other": "true"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := Resolve(Input{Secrets: cfg, VolumeContext: tt.vc, VolumeID: "v", Mode: tt.mode})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(s.Options, tt.want) {
				t.Errorf("options = %v, want %v", s.Options, tt.want)
			}
		})
	}
}

func TestIsolation(t *testing.T) {
	cfg := map[string]string{KeyConfigData: "[s3]\ntype = s3\n"}
	a, errA := Resolve(Input{Secrets: cfg, VolumeID: "vol-a"})
	b, errB := Resolve(Input{Secrets: cfg, VolumeID: "vol-b"})
	if errA != nil || errB != nil {
		t.Fatal(errA, errB)
	}
	ra, _, _ := strings.Cut(a.Remote.FsPath, ":")
	rb, _, _ := strings.Cut(b.Remote.FsPath, ":")
	if ra == rb || ra == "s3" {
		t.Errorf("remotes collide: %q %q", ra, rb)
	}
	if !strings.Contains(a.Remote.ConfigData, "["+ra+"]") || strings.Contains(a.Remote.ConfigData, "[s3]") {
		t.Errorf("config not isolated: %q", a.Remote.ConfigData)
	}
	if a.Identity == b.Identity {
		t.Error("identities collide")
	}
}

func TestShorthand(t *testing.T) {
	s, err := Resolve(Input{Secrets: s3Secrets(), VolumeID: "v",
		VolumeContext: map[string]string{KeyRemotePath: "bucket/dir"}})
	if err != nil {
		t.Fatal(err)
	}
	name := isolatedName("remote", "v")
	want := "[" + name + "]\ntype = s3\naccess_key_id = AKIA\nprovider = Minio\nsecret_access_key = " + secretValue + "\n"
	if s.Remote.ConfigData != want {
		t.Errorf("config = %q, want %q", s.Remote.ConfigData, want)
	}
	if s.Remote.FsPath != name+":bucket/dir" {
		t.Errorf("fs path = %q", s.Remote.FsPath)
	}
}

func TestIdentity(t *testing.T) {
	in := Input{Secrets: map[string]string{KeyConfigData: "[a]\ntype = local"}, VolumeID: "v",
		VolumeContext: map[string]string{"vfs-cache-mode": "full"}}
	base, _ := Resolve(in)
	in.ReadOnly = true
	ro, _ := Resolve(in)
	if base.Identity != ro.Identity || !ro.ReadOnly {
		t.Error("per-publish read-only must not change identity")
	}
	in.VolumeContext = map[string]string{"vfs-cache-mode": "off"}
	drift, _ := Resolve(in)
	if drift.Identity == base.Identity {
		t.Error("option change must change identity")
	}
	again, _ := Resolve(in)
	if again.Identity != drift.Identity {
		t.Error("identity not stable")
	}
}

func TestResourcesAndCache(t *testing.T) {
	s, err := Resolve(Input{Secrets: map[string]string{KeyConfigData: "[a]"}, VolumeID: "v",
		VolumeContext: map[string]string{KeyCPURequest: "100m", KeyMemoryLimit: "1Gi",
			KeyCacheVolume: `{"emptyDir":{}}`}})
	if err != nil {
		t.Fatal(err)
	}
	want := Resources{CPURequest: "100m", MemoryLimit: "1Gi"}
	if s.Resources != want || s.CacheVolume != `{"emptyDir":{}}` {
		t.Errorf("got %+v %q", s.Resources, s.CacheVolume)
	}
}

func TestValidateContext(t *testing.T) {
	ok := map[string]string{"remote": "m", "remotePath": "b/x", "vfs-cache-mode": "writes", "csi.storage.k8s.io/pvc/name": "p"}
	if err := ValidateContext(ok, Persistent); err != nil {
		t.Fatal(err)
	}
	bad := []struct {
		vc   map[string]string
		mode Mode
	}{
		{map[string]string{"configData": "x"}, Persistent},
		{map[string]string{"access_key_id": "x"}, Persistent},
		{map[string]string{"bogus-flag": "1"}, Persistent},
		{map[string]string{"vfs-cache-mode": "nope"}, Persistent},
		{map[string]string{"cacheVolume": "["}, Persistent},
		{map[string]string{"allowAllOptions": "true"}, Ephemeral},
		{map[string]string{"mountPod.cpuLimit": "1"}, Ephemeral},
	}
	for _, c := range bad {
		if err := ValidateContext(c.vc, c.mode); !errors.Is(err, ErrInvalid) {
			t.Errorf("%v: got %v", c.vc, err)
		}
	}
}
