package auth

import (
	"errors"
	"sync"
	"time"
)

const (
	DefaultLoginFailureLimit  = 5
	DefaultLoginFailureWindow = 15 * time.Minute
	maxTrackedLoginSources    = 10000
)

// LoginLimiter is intentionally process-local. COWS currently has one active
// control-plane process; a future multi-instance deployment needs a shared
// limiter or an upstream control.
type LoginLimiter struct {
	mu          sync.Mutex
	entries     map[string]*loginAttempt
	maxFailures int
	window      time.Duration
	now         func() time.Time
}

type loginAttempt struct {
	startedAt time.Time
	failures  int
	active    int
}

// LoginReservation owns one allowance until Complete is called. In-flight work
// counts toward the limit, so a concurrent burst cannot exceed it.
type LoginReservation struct {
	once      sync.Once
	limiter   *LoginLimiter
	key       string
	entry     *loginAttempt
	startedAt time.Time
}

func NewLoginLimiter(maxFailures int, window time.Duration) (*LoginLimiter, error) {
	if maxFailures <= 0 {
		return nil, errors.New("login failure limit must be positive")
	}
	if window <= 0 {
		return nil, errors.New("login failure window must be positive")
	}
	return &LoginLimiter{entries: make(map[string]*loginAttempt), maxFailures: maxFailures, window: window, now: time.Now}, nil
}

func (l *LoginLimiter) Reserve(key string) (*LoginReservation, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	entry := l.currentEntry(key)
	if entry == nil {
		if len(l.entries) >= maxTrackedLoginSources && !l.removeOldest() {
			return nil, false
		}
		entry = &loginAttempt{startedAt: l.now()}
		l.entries[key] = entry
	}
	if entry.failures+entry.active >= l.maxFailures {
		return nil, false
	}
	entry.active++
	return &LoginReservation{limiter: l, key: key, entry: entry, startedAt: entry.startedAt}, true
}

// Complete records one result. Success clears earlier failures but keeps other
// reservations charged; failure retains this allowance until its window ends.
// Call Complete(false) for operations whose successes also consume a budget.
func (r *LoginReservation) Complete(success bool) {
	r.once.Do(func() {
		l := r.limiter
		l.mu.Lock()
		defer l.mu.Unlock()
		entry := l.currentEntry(r.key)
		if entry != r.entry {
			return
		}
		entry.active--
		// A request from an older window cannot erase or add to new failures.
		if entry.startedAt.Equal(r.startedAt) {
			if success {
				entry.failures = 0
			} else {
				entry.failures++
			}
		}
		if entry.active == 0 && entry.failures == 0 {
			delete(l.entries, r.key)
		}
	})
}

func (l *LoginLimiter) RetryAfter(key string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	entry := l.currentEntry(key)
	if entry == nil || entry.failures+entry.active < l.maxFailures {
		return 0
	}
	remaining := l.window - l.now().Sub(entry.startedAt)
	if remaining < 0 {
		return 0
	}
	return remaining
}

func (l *LoginLimiter) currentEntry(key string) *loginAttempt {
	entry := l.entries[key]
	if entry != nil && l.now().Sub(entry.startedAt) >= l.window {
		if entry.active == 0 {
			delete(l.entries, key)
			return nil
		}
		entry.startedAt = l.now()
		entry.failures = 0
	}
	return entry
}

func (l *LoginLimiter) removeOldest() bool {
	var oldestKey string
	var oldest time.Time
	found := false
	for key, entry := range l.entries {
		if entry.active == 0 && (!found || entry.startedAt.Before(oldest)) {
			oldestKey, oldest, found = key, entry.startedAt, true
		}
	}
	if found {
		delete(l.entries, oldestKey)
	}
	return found
}
