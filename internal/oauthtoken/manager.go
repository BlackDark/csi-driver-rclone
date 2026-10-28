package oauthtoken

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/BlackDark/csi-driver-rclone/internal/handoff"
	"github.com/BlackDark/csi-driver-rclone/internal/mountspec"
)

// Manager is the node plugin's token write-back: it seeds Token Secrets at publish,
// serves the Mount Pods' lock and save requests, and pushes changed tokens to them.
type Manager struct {
	Store  Store
	Locker Locker
	Node   string
	// LockWait bounds a lock request; a Mount Pod refreshes unlocked after it.
	LockWait time.Duration
	Log      *slog.Logger
	// ObserveSave and ObserveLockWait feed metrics; nil records nothing.
	ObserveSave     func(SaveResult)
	ObserveLockWait func(time.Duration)

	mu   sync.Mutex
	subs map[string]map[int]func(token string) // by seed hash
	next int
}

// Resolve substitutes the stored token for every write-back section of spec. A
// failure keeps the user's token: the mount then works as without write-back.
func (m *Manager) Resolve(ctx context.Context, spec mountspec.Spec) string {
	cfg := spec.Remote.ConfigData
	for _, ts := range spec.Tokens {
		seed := mountspec.TokenOf(cfg, ts.Isolated)
		cur, err := m.Store.Seed(ctx, ts.Section, ts.SeedHash, seed)
		if err != nil {
			m.Log.Warn("token write-back: using the Credential Secret token", "section", ts.Section, "err", err)
			continue
		}
		if cur != seed {
			cfg = mountspec.SetToken(cfg, ts.Isolated, cur)
		}
	}
	return cfg
}

// Updated pushes a changed Token Secret to the local Mount Pods using it.
func (m *Manager) Updated(sec *corev1.Secret) {
	seedHash, token := string(sec.Data[DataSeedHash]), string(sec.Data[DataToken])
	if seedHash == "" || token == "" {
		return
	}
	m.mu.Lock()
	pushes := make([]func(string), 0, len(m.subs[seedHash]))
	for _, p := range m.subs[seedHash] {
		pushes = append(pushes, p)
	}
	m.mu.Unlock()
	for _, p := range pushes {
		p(token)
	}
}

func (m *Manager) subscribe(seedHash string, push func(string)) func() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.subs == nil {
		m.subs = map[string]map[int]func(string){}
	}
	if m.subs[seedHash] == nil {
		m.subs[seedHash] = map[int]func(string){}
	}
	m.next++
	id := m.next
	m.subs[seedHash][id] = push
	return func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		delete(m.subs[seedHash], id)
		if len(m.subs[seedHash]) == 0 {
			delete(m.subs, seedHash)
		}
	}
}

func (m *Manager) observeSave(r SaveResult) {
	if m.ObserveSave != nil {
		m.ObserveSave(r)
	}
}

func (m *Manager) observeLockWait(d time.Duration) {
	if m.ObserveLockWait != nil {
		m.ObserveLockWait(d)
	}
}

// Handler serves the token requests of mount id; nil when spec has no write-back.
func (m *Manager) Handler(id string, tokens []mountspec.TokenSection) handoff.TokenHandler {
	if m == nil || len(tokens) == 0 {
		return nil
	}
	h := &mountHandler{m: m, id: id, sections: map[string]mountspec.TokenSection{}, held: map[string]func(){}}
	for _, ts := range tokens {
		if !validSeedHash(ts.SeedHash) {
			m.Log.Warn("token write-back: ignoring section with a malformed seed hash", "section", ts.Section, "id", id)
			continue
		}
		h.sections[ts.Section] = ts
	}
	return h
}

var errUnknownSection = errors.New("token write-back: unknown section")

const (
	readWait  = 10 * time.Second // one Token Secret read or write
	saveTries = 5
)

// mountHandler is the handoff.TokenHandler of one mount.
type mountHandler struct {
	m        *Manager
	id       string
	sections map[string]mountspec.TokenSection

	mu   sync.Mutex
	held map[string]func() // release by section
}

func (h *mountHandler) Lock(ctx context.Context, section string) (string, error) {
	ts, ok := h.sections[section]
	if !ok {
		return "", errUnknownSection
	}
	// LockWait plus readWait stays below the Mount Pod's own lock timeout.
	actx, cancel := context.WithTimeout(ctx, h.m.LockWait)
	defer cancel()
	start := h.m.Locker.Clock.Now()
	// The section is part of the holder: two sections on one grant are two refreshers.
	release, err := h.m.Locker.Acquire(actx, Name(ts.SeedHash), h.m.Node+"/"+h.id+"/"+section)
	h.m.observeLockWait(h.m.Locker.Clock.Since(start))
	if err != nil {
		h.m.Log.Warn("token write-back: no refresh lock", "section", section, "id", h.id, "err", err)
		return "", err
	}
	rctx, rcancel := context.WithTimeout(ctx, readWait)
	defer rcancel()
	token, err := h.m.Store.Get(rctx, ts.SeedHash)
	if err != nil {
		release()
		h.m.Log.Warn("token write-back: read token", "section", section, "err", err)
		return "", err
	}
	h.mu.Lock()
	if old := h.held[section]; old != nil {
		old()
	}
	h.held[section] = release
	h.mu.Unlock()
	return token, nil
}

func (h *mountHandler) Unlock(section string) {
	h.mu.Lock()
	release := h.held[section]
	delete(h.held, section)
	h.mu.Unlock()
	if release != nil {
		release()
	}
}

// Save writes the token, retrying API failures, then releases the lock: other
// refreshers must not start before the new token is stored.
func (h *mountHandler) Save(ctx context.Context, section, token string) error {
	defer h.Unlock(section)
	ts, ok := h.sections[section]
	if !ok {
		return errUnknownSection
	}
	res, err := h.saveRetrying(ctx, section, ts.SeedHash, token)
	h.m.observeSave(res)
	if err != nil {
		return err
	}
	h.m.Log.Info("token write-back", "section", section, "id", h.id, "result", res)
	return nil
}

func (h *mountHandler) saveRetrying(ctx context.Context, section, seedHash, token string) (SaveResult, error) {
	for try := 1; ; try++ {
		sctx, cancel := context.WithTimeout(ctx, readWait)
		res, err := h.m.Store.Save(sctx, section, seedHash, token)
		cancel()
		if err == nil || try == saveTries {
			return res, err
		}
		h.m.Log.Warn("token write-back failed, retrying", "section", section, "id", h.id, "try", try, "err", err)
		select {
		case <-ctx.Done():
			return res, err
		case <-h.m.Locker.Clock.After(time.Duration(try) * time.Second):
		}
	}
}

func (h *mountHandler) Subscribe(push func(section, token string)) func() {
	cancels := make([]func(), 0, len(h.sections))
	for section, ts := range h.sections {
		cancels = append(cancels, h.m.subscribe(ts.SeedHash, func(token string) { push(section, token) }))
	}
	return func() {
		for _, c := range cancels {
			c()
		}
	}
}
