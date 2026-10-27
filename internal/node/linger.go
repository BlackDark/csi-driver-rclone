package node

import (
	"context"
)

// startLinger arms the Linger for a mount that just lost its last user. Callers hold
// the <id> lock. A non-positive Linger releases at once.
func (s *Server) startLinger(ctx context.Context, m *fuseMount, unlock func()) error {
	if s.cfg.Linger <= 0 {
		return s.release(ctx, m, unlock)
	}
	s.cancelLinger(m)
	gen := m.lingerGen
	s.bg.Add(1)
	m.linger = s.deps.Clock.AfterFunc(s.cfg.Linger, func() { s.lingerExpired(m, gen) })
	s.deps.Log.Info("mount unused, lingering", "id", m.id, "linger", s.cfg.Linger.String())
	unlock()
	return nil
}

// cancelLinger stops a pending Linger; a publish within it reuses the Mount Pod.
// Callers hold the <id> lock. A timer that already fired sees the bumped generation.
func (s *Server) cancelLinger(m *fuseMount) {
	m.lingerGen++
	if m.linger == nil {
		return
	}
	if m.linger.Stop() {
		s.bg.Done()
	}
	m.linger = nil
}

func (s *Server) lingerExpired(m *fuseMount, gen uint64) {
	defer s.bg.Done()
	unlock := s.locks.lock(m.id)
	if s.getMount(m.id) != m || m.lingerGen != gen || s.deps.Users.Count(m.id) > 0 {
		unlock()
		return
	}
	m.linger = nil
	s.deps.Log.Info("linger expired, stopping mount pod", "id", m.id)
	ctx, cancel := context.WithTimeout(context.Background(), kubeCallTimeout)
	defer cancel()
	if err := s.release(ctx, m, unlock); err != nil {
		s.deps.Log.Warn("release after linger, retrying after next linger", "id", m.id, "err", err)
		relock := s.locks.lock(m.id)
		if s.getMount(m.id) == m && s.deps.Users.Count(m.id) == 0 {
			_ = s.startLinger(ctx, m, relock)
			return
		}
		relock()
	}
}
