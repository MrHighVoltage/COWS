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
	entries     map[string]loginAttempt
	maxFailures int
	window      time.Duration
	now         func() time.Time
}

type loginAttempt struct {
	startedAt time.Time
	failures  int
}

func NewLoginLimiter(maxFailures int, window time.Duration) (*LoginLimiter, error) {
	if maxFailures <= 0 {
		return nil, errors.New("login failure limit must be positive")
	}
	if window <= 0 {
		return nil, errors.New("login failure window must be positive")
	}
	return &LoginLimiter{entries: make(map[string]loginAttempt), maxFailures: maxFailures, window: window, now: time.Now}, nil
}

func (l *LoginLimiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	entry, ok := l.currentEntry(key)
	return !ok || entry.failures < l.maxFailures
}

func (l *LoginLimiter) Failure(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	entry, ok := l.currentEntry(key)
	if !ok {
		if len(l.entries) >= maxTrackedLoginSources {
			l.removeOldest()
		}
		entry = loginAttempt{startedAt: now}
	}
	entry.failures++
	l.entries[key] = entry
}

func (l *LoginLimiter) Success(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, key)
}

func (l *LoginLimiter) RetryAfter(key string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	entry, ok := l.currentEntry(key)
	if !ok || entry.failures < l.maxFailures {
		return 0
	}
	remaining := l.window - l.now().Sub(entry.startedAt)
	if remaining < 0 {
		return 0
	}
	return remaining
}

func (l *LoginLimiter) currentEntry(key string) (loginAttempt, bool) {
	entry, ok := l.entries[key]
	if !ok {
		return loginAttempt{}, false
	}
	if l.now().Sub(entry.startedAt) >= l.window {
		delete(l.entries, key)
		return loginAttempt{}, false
	}
	return entry, true
}

func (l *LoginLimiter) removeOldest() {
	var oldestKey string
	var oldest time.Time
	for key, entry := range l.entries {
		if oldestKey == "" || entry.startedAt.Before(oldest) {
			oldestKey = key
			oldest = entry.startedAt
		}
	}
	if oldestKey != "" {
		delete(l.entries, oldestKey)
	}
}
