package mountspec

import (
	"errors"
	"strings"
	"testing"
)

const oauthConfig = "[box]\ntype = box\ntoken = {\"refresh_token\":\"r1\"}\n\n[crypt]\ntype = crypt\nremote = box:x\npassword = p\n\n[od]\ntype = onedrive\ntoken = {\"refresh_token\":\"o1\"}\n"

func oauthInput(configData string, vc map[string]string) Input {
	if vc == nil {
		vc = map[string]string{}
	}
	vc[KeyRemote] = "crypt"
	return Input{Secrets: map[string]string{KeyConfigData: configData}, VolumeContext: vc, VolumeID: "v"}
}

func TestResolveTokenSections(t *testing.T) {
	s, err := Resolve(oauthInput(oauthConfig, nil))
	if err != nil {
		t.Fatal(err)
	}
	want := []TokenSection{
		{Section: "box", Isolated: isolatedName("box", "v"), SeedHash: HashToken(`{"refresh_token":"r1"}`)},
		{Section: "od", Isolated: isolatedName("od", "v"), SeedHash: HashToken(`{"refresh_token":"o1"}`)},
	}
	if len(s.Tokens) != 2 || s.Tokens[0] != want[0] || s.Tokens[1] != want[1] {
		t.Fatalf("tokens %+v", s.Tokens)
	}
}

func TestIdentityIgnoresTokenValues(t *testing.T) {
	a, _ := Resolve(oauthInput(oauthConfig, nil))
	b, _ := Resolve(oauthInput(strings.ReplaceAll(oauthConfig, "r1", "r2"), nil))
	if a.Identity != b.Identity {
		t.Error("token change changed the identity")
	}
	c, _ := Resolve(oauthInput(strings.ReplaceAll(oauthConfig, "password = p", "password = q"), nil))
	if a.Identity == c.Identity {
		t.Error("non-token change kept the identity")
	}
}

func TestIdentityUnchangedWithoutTokens(t *testing.T) {
	in := oauthInput("[crypt]\ntype = local\n", nil)
	s, _ := Resolve(in)
	if s.Tokens != nil {
		t.Fatalf("tokens %+v", s.Tokens)
	}
	if want := identity("v", Spec{Remote: s.Remote, Options: s.Options}); s.Identity != want {
		t.Error("identity of a config without tokens changed")
	}
}

func TestTokenWriteBackOptOut(t *testing.T) {
	off := map[string]string{KeyTokenWriteBack: "false"}
	a, err := Resolve(oauthInput(oauthConfig, off))
	if err != nil {
		t.Fatal(err)
	}
	if a.Tokens != nil {
		t.Errorf("opt-out kept tokens %+v", a.Tokens)
	}
	b, _ := Resolve(oauthInput(strings.ReplaceAll(oauthConfig, "r1", "r2"), map[string]string{KeyTokenWriteBack: "false"}))
	if a.Identity == b.Identity {
		t.Error("opt-out identity ignores tokens")
	}
	_, err = Resolve(oauthInput(oauthConfig, map[string]string{KeyTokenWriteBack: "no"}))
	var e *Error
	if !errors.As(err, &e) || e.Key != KeyTokenWriteBack {
		t.Errorf("err %v", err)
	}
	if err := ValidateContext(map[string]string{KeyTokenWriteBack: "no"}, Persistent); err == nil {
		t.Error("ValidateContext accepted a bad tokenWriteBack")
	}
}

func TestSetAndGetToken(t *testing.T) {
	got := SetToken(oauthConfig, "od", `{"refresh_token":"o2"}`)
	if TokenOf(got, "od") != `{"refresh_token":"o2"}` || TokenOf(got, "box") != `{"refresh_token":"r1"}` {
		t.Fatalf("got %q", got)
	}
	if strings.Replace(got, "o2", "o1", 1) != oauthConfig {
		t.Error("other lines changed")
	}
	if TokenOf(oauthConfig, "crypt") != "" {
		t.Error("token for a section without one")
	}
}

func TestTokensInEncryptedConfig(t *testing.T) {
	s, err := Resolve(Input{
		Secrets:       map[string]string{KeyConfigData: encryptWithRclone(t, oauthConfig, "pw"), KeyConfigPassword: "pw"},
		VolumeContext: map[string]string{KeyRemote: "crypt"}, VolumeID: "v",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Tokens) != 2 || TokenOf(s.Remote.ConfigData, s.Tokens[0].Isolated) != `{"refresh_token":"r1"}` {
		t.Fatalf("tokens %+v", s.Tokens)
	}
}

func TestSpecFileKeepsTokens(t *testing.T) {
	s, _ := Resolve(oauthInput(oauthConfig, nil))
	raw, err := s.Serialize()
	if err != nil {
		t.Fatal(err)
	}
	got, err := Load(raw)
	if err != nil || len(got.Tokens) != 2 || got.Tokens[1] != s.Tokens[1] {
		t.Fatalf("got %+v %v", got.Tokens, err)
	}
}
