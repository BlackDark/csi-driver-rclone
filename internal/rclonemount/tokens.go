package rclonemount

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config"

	"github.com/BlackDark/csi-driver-rclone/internal/mountspec"
)

// tokenLocker is the Mount Pod's line to the node plugin (handoff.Session).
type tokenLocker interface {
	LockToken(ctx context.Context, section string) (string, error)
	UnlockToken(section string) error
	SaveToken(section, token string) error
}

// tokenHook sits between rclone's oauthutil and the in-memory config: rclone reads
// the token key right before it refreshes an expired token (oauthutil reReadToken)
// and writes it after. An expired read takes the node plugin's refresh lock and
// returns the newest token, so rclone refreshes only when nobody else has.
type tokenHook struct {
	get      func(section, key string) (string, bool)
	set      func(section, key, value string) error // rclone's write: stores and saves
	store    func(section, key, value string)       // memory only, for tokens from the plugin
	original map[string]string                      // isolated section -> user section
	isolated map[string]string                      // user section -> isolated section
	log      *slog.Logger
	now      func() time.Time

	lockTimeout time.Duration // bound on waiting for the lock
	holdTimeout time.Duration // lock released if rclone never saves

	lockMu sync.Mutex // serializes lock requests; never held by push
	mu     sync.Mutex
	locker tokenLocker
	held   map[string]*time.Timer // by isolated section
}

func newTokenHook(tokens []mountspec.TokenSection, log *slog.Logger) *tokenHook {
	h := &tokenHook{
		original: map[string]string{}, isolated: map[string]string{}, held: map[string]*time.Timer{},
		log: log, now: time.Now, lockTimeout: time.Minute, holdTimeout: time.Minute,
	}
	for _, ts := range tokens {
		h.original[ts.Isolated] = ts.Section
		h.isolated[ts.Section] = ts.Isolated
	}
	return h
}

// install wraps rclone's config accessors; one mount per process, so global is fine.
func (h *tokenHook) install() {
	h.get, h.set, h.store = fs.ConfigFileGet, fs.ConfigFileSet, config.FileSetValue
	fs.ConfigFileGet, fs.ConfigFileSet = h.getValue, h.setValue
}

func (h *tokenHook) attach(l tokenLocker) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.locker = l
}

func (h *tokenHook) getValue(section, key string) (string, bool) {
	v, ok := h.get(section, key)
	user, managed := h.original[section]
	if !managed || key != mountspec.KeyToken || h.valid(v) {
		return v, ok
	}
	h.lockMu.Lock()
	defer h.lockMu.Unlock()
	if v, ok = h.get(section, key); h.valid(v) {
		return v, ok // pushed while we waited
	}
	h.mu.Lock()
	locker, holding := h.locker, h.held[section] != nil
	h.mu.Unlock()
	if holding {
		return v, ok
	}
	if locker == nil {
		h.log.Warn("token write-back: no node plugin connection, refreshing without the lock", "section", user)
		return v, ok
	}
	ctx, cancel := context.WithTimeout(context.Background(), h.lockTimeout)
	defer cancel()
	newest, err := locker.LockToken(ctx, user)
	if err != nil {
		h.log.Warn("token write-back: no refresh lock, refreshing without it", "section", user, "err", err)
		return v, ok
	}
	if newest != "" && newest != v {
		h.store(section, key, newest)
		v, ok = newest, true
	}
	if h.valid(v) {
		_ = locker.UnlockToken(user) // refreshed elsewhere: nothing to do
		return v, ok
	}
	h.mu.Lock()
	h.held[section] = time.AfterFunc(h.holdTimeout, func() { h.release(section, locker) })
	h.mu.Unlock()
	return v, ok
}

func (h *tokenHook) release(section string, l tokenLocker) {
	if h.forget(section) {
		_ = l.UnlockToken(h.original[section])
	}
}

// forget drops the held lock of section; true if it was held.
func (h *tokenHook) forget(section string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	t := h.held[section]
	delete(h.held, section)
	if t != nil {
		t.Stop()
	}
	return t != nil
}

func (h *tokenHook) setValue(section, key, value string) error {
	err := h.set(section, key, value)
	user, managed := h.original[section]
	if !managed || key != mountspec.KeyToken {
		return err
	}
	h.forget(section)
	h.mu.Lock()
	locker := h.locker
	h.mu.Unlock()
	if locker == nil {
		h.log.Warn("token write-back: no node plugin connection, refreshed token kept in memory only", "section", user)
		return err
	}
	if serr := locker.SaveToken(user, value); serr != nil {
		h.log.Warn("token write-back: report refreshed token", "section", user, "err", serr)
	} else {
		h.log.Info("token write-back: reported refreshed token", "section", user)
	}
	return err
}

// push applies a token the node plugin pushed; rclone picks it up on its next read.
func (h *tokenHook) push(user, token string) {
	section, ok := h.isolated[user]
	if !ok || token == "" {
		return
	}
	if cur, _ := h.get(section, mountspec.KeyToken); cur == token {
		return
	}
	h.store(section, mountspec.KeyToken, token)
	h.log.Debug("token write-back: received updated token", "section", user)
}

// valid mirrors oauth2.Token.Valid with its 10s expiry delta; no expiry means valid.
func (h *tokenHook) valid(token string) bool {
	var t struct {
		AccessToken string    `json:"access_token"`
		Expiry      time.Time `json:"expiry"`
	}
	if token == "" || json.Unmarshal([]byte(token), &t) != nil || t.AccessToken == "" {
		return false
	}
	return t.Expiry.IsZero() || t.Expiry.Add(-10*time.Second).After(h.now())
}
