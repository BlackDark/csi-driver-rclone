// Package fakeoauth is a test-only OAuth2 token endpoint with single-use refresh
// tokens (like Box) plus the few HiDrive API calls rclone needs to mount a root.
// It counts invalid_grant answers: any is a lost token race.
package fakeoauth

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Stats are the counters served on /stats.
type Stats struct {
	Refreshes      int `json:"refreshes"`
	InvalidGrant   int `json:"invalidGrant"`
	RejectedAccess int `json:"rejectedAccess"`
	APICalls       int `json:"apiCalls"`
}

// Server is the fake. Zero TTL means one hour.
type Server struct {
	TTL time.Duration
	Now func() time.Time

	mu      sync.Mutex
	refresh map[string]bool      // unused refresh tokens
	access  map[string]time.Time // access token -> expiry
	stats   Stats
}

// New returns a Server that accepts the given initial refresh tokens once each.
func New(ttl time.Duration, initial ...string) *Server {
	s := &Server{TTL: ttl, Now: time.Now, refresh: map[string]bool{}, access: map[string]time.Time{}}
	for _, r := range initial {
		s.refresh[r] = true
	}
	return s
}

// Stats returns a copy of the counters.
func (s *Server) Stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stats
}

// Handler serves /token, /seed?refresh=, /stats and the HiDrive API under /2.1.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /token", s.token)
	mux.HandleFunc("GET /seed", func(w http.ResponseWriter, r *http.Request) {
		rt := r.URL.Query().Get("refresh")
		if rt == "" {
			writeJSON(w, 400, map[string]string{"error": "refresh missing"})
			return
		}
		s.mu.Lock()
		s.refresh[rt] = true
		s.mu.Unlock()
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /stats", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, s.Stats()) })
	mux.HandleFunc("GET /2.1/meta", s.api(func(r *http.Request) any {
		return map[string]any{"type": "dir", "name": "", "path": r.URL.Query().Get("path"), "id": "root"}
	}))
	mux.HandleFunc("GET /2.1/dir", s.api(func(*http.Request) any {
		return map[string]any{"nmembers": 0, "members": []any{}}
	}))
	return mux
}

func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil || r.PostForm.Get("grant_type") != "refresh_token" {
		writeJSON(w, 400, map[string]string{"error": "unsupported_grant_type"})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rt := r.PostForm.Get("refresh_token")
	if rt == "" || !s.refresh[rt] {
		s.stats.InvalidGrant++
		writeJSON(w, 400, map[string]string{"error": "invalid_grant", "error_description": "refresh token reused or unknown"})
		return
	}
	delete(s.refresh, rt)
	ttl := s.TTL
	if ttl == 0 {
		ttl = time.Hour
	}
	access, next := randID(), randID()
	s.access[access] = s.Now().Add(ttl)
	s.refresh[next] = true
	s.stats.Refreshes++
	writeJSON(w, 200, map[string]any{
		"access_token": access, "refresh_token": next, "token_type": "Bearer", "expires_in": int(ttl.Seconds()),
	})
}

func (s *Server) api(body func(*http.Request) any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		access := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		s.mu.Lock()
		exp, ok := s.access[access]
		valid := ok && s.Now().Before(exp)
		s.stats.APICalls++
		if !valid {
			s.stats.RejectedAccess++
		}
		s.mu.Unlock()
		if !valid {
			writeJSON(w, 401, map[string]string{"code": "401", "msg": "invalid access token"})
			return
		}
		writeJSON(w, 200, body(r))
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func randID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
