package checker

import "sync"

// HostLimiter ensures that only one check per host is running at any given time.
type HostLimiter struct {
	mu    sync.Mutex
	hosts map[string]struct{}
}

// NewHostLimiter creates a new HostLimiter.
func NewHostLimiter() *HostLimiter {
	return &HostLimiter{
		hosts: make(map[string]struct{}),
	}
}

// Acquire attempts to acquire a lock for a given host.
// It returns true if the lock was acquired, and false otherwise.
func (hl *HostLimiter) Acquire(host string) bool {
	hl.mu.Lock()
	defer hl.mu.Unlock()

	if _, exists := hl.hosts[host]; exists {
		return false // Another check for this host is already in progress.
	}

	hl.hosts[host] = struct{}{}
	return true
}

// Release releases the lock for a given host.
func (hl *HostLimiter) Release(host string) {
	hl.mu.Lock()
	defer hl.mu.Unlock()
	delete(hl.hosts, host)
}
