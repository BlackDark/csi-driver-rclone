package rclonemount

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/rclone/rclone/fs"

	"github.com/BlackDark/csi-driver-rclone/internal/mountspec"
	"github.com/BlackDark/csi-driver-rclone/test/fakeoauth"
)

// pluginLocker plays the node plugin: one Token Secret, saved tokens become newest.
type pluginLocker struct{ fakeLocker }

func (p *pluginLocker) SaveToken(section, token string) error {
	_ = p.fakeLocker.SaveToken(section, token)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.newest = token
	return nil
}

// openHiDrive opens a HiDrive remote against the fake through the real rclone
// oauthutil, with the token hook installed.
func openHiDrive(t *testing.T, srv *httptest.Server, section, token string, l tokenLocker) error {
	t.Helper()
	cfg := "[" + section + "]\ntype = hidrive\nclient_id = id\nclient_secret = secret\n" +
		"endpoint = " + srv.URL + "/2.1\ntoken_url = " + srv.URL + "/token\ntoken = " + token + "\n"
	if err := LoadConfig(cfg); err != nil {
		t.Fatal(err)
	}
	get, set := fs.ConfigFileGet, fs.ConfigFileSet
	t.Cleanup(func() { fs.ConfigFileGet, fs.ConfigFileSet = get, set })
	h := newTokenHook([]mountspec.TokenSection{{Section: "hd", Isolated: section, SeedHash: "x"}},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	h.install()
	h.attach(l)
	_, err := fs.NewFs(context.Background(), section+":")
	return err
}

func fakeServer(t *testing.T) (*fakeoauth.Server, *httptest.Server) {
	t.Helper()
	fake := fakeoauth.New(time.Hour, "r0")
	srv := httptest.NewServer(fake.Handler())
	t.Cleanup(srv.Close)
	return fake, srv
}

const expiredToken = `{"access_token":"stale","token_type":"Bearer","refresh_token":"r0","expiry":"2000-01-01T00:00:00Z"}`

func TestRcloneRefreshUnderLockIsReported(t *testing.T) {
	fake, srv := fakeServer(t)
	l := &pluginLocker{fakeLocker{newest: expiredToken}}
	if err := openHiDrive(t, srv, "hd_lock", expiredToken, l); err != nil {
		t.Fatal(err)
	}
	st := fake.Stats()
	if st.Refreshes != 1 || st.InvalidGrant != 0 || st.RejectedAccess != 0 {
		t.Fatalf("stats %+v", st)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.locks != 1 || len(l.saved) != 1 || !strings.HasPrefix(l.saved[0], "hd=") || strings.Contains(l.saved[0], `"r0"`) {
		t.Fatalf("locks %d saved %v", l.locks, len(l.saved))
	}
}

// Another process already spent r0: the plugin hands out its fresh token and rclone
// must use it instead of refreshing with r0 (which the fake would reject).
func TestRcloneUsesTokenRefreshedElsewhere(t *testing.T) {
	fake, srv := fakeServer(t)
	resp, err := http.PostForm(srv.URL+"/token", url.Values{"grant_type": {"refresh_token"}, "refresh_token": {"r0"}})
	if err != nil {
		t.Fatal(err)
	}
	var issued struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&issued)
	_ = resp.Body.Close()
	fresh := `{"access_token":"` + issued.AccessToken + `","token_type":"Bearer","refresh_token":"` +
		issued.RefreshToken + `","expiry":"` + time.Now().Add(time.Hour).Format(time.RFC3339) + `"}`
	l := &pluginLocker{fakeLocker{newest: fresh}}
	if err := openHiDrive(t, srv, "hd_elsewhere", expiredToken, l); err != nil {
		t.Fatal(err)
	}
	if st := fake.Stats(); st.Refreshes != 1 || st.InvalidGrant != 0 {
		t.Fatalf("stats %+v", st)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.locks != 1 || l.unlocks != 1 || len(l.saved) != 0 {
		t.Fatalf("locks %d unlocks %d saved %d", l.locks, l.unlocks, len(l.saved))
	}
}
