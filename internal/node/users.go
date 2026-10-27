package node

import "sync"

// Users tracks which target paths use which mount <id>. In memory for now; after a
// plugin restart it is to be rebuilt from the mount table (ticket 06).
type Users interface {
	Add(id, target string)
	// Lookup returns the id target uses.
	Lookup(target string) (id string, ok bool)
	// Remove forgets target and reports how many targets still use its id.
	Remove(target string) (remaining int)
}

type memUsers struct {
	mu       sync.Mutex
	byTarget map[string]string
	count    map[string]int
}

// NewUsers returns an in-memory Users.
func NewUsers() Users {
	return &memUsers{byTarget: map[string]string{}, count: map[string]int{}}
}

func (u *memUsers) Add(id, target string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if old, ok := u.byTarget[target]; ok {
		if old == id {
			return
		}
		u.count[old]--
	}
	u.byTarget[target] = id
	u.count[id]++
}

func (u *memUsers) Remove(target string) int {
	u.mu.Lock()
	defer u.mu.Unlock()
	id, ok := u.byTarget[target]
	if !ok {
		return 0
	}
	delete(u.byTarget, target)
	u.count[id]--
	n := u.count[id]
	if n <= 0 {
		delete(u.count, id)
	}
	return n
}

func (u *memUsers) Lookup(target string) (string, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	id, ok := u.byTarget[target]
	return id, ok
}

// keyedLock serializes work per mount <id>.
type keyedLock struct {
	mu    sync.Mutex
	locks map[string]*refMutex
}

type refMutex struct {
	sync.Mutex
	refs int
}

// lock returns the unlock func; it may be called from another goroutine.
func (k *keyedLock) lock(key string) func() {
	k.mu.Lock()
	if k.locks == nil {
		k.locks = map[string]*refMutex{}
	}
	m := k.locks[key]
	if m == nil {
		m = &refMutex{}
		k.locks[key] = m
	}
	m.refs++
	k.mu.Unlock()
	m.Lock()
	return func() {
		m.Unlock()
		k.mu.Lock()
		m.refs--
		if m.refs == 0 {
			delete(k.locks, key)
		}
		k.mu.Unlock()
	}
}
