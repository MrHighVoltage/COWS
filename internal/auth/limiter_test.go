package auth

import (
	"testing"
	"time"
)

func TestLoginLimiterBlocksAndResets(t *testing.T) {
	now := time.Unix(100, 0)
	limiter, err := NewLoginLimiter(2, time.Minute)
	if err != nil {
		t.Fatalf("create limiter: %v", err)
	}
	limiter.now = func() time.Time { return now }

	if !limiter.Allow("198.51.100.5") {
		t.Fatal("new source should be allowed")
	}
	limiter.Failure("198.51.100.5")
	limiter.Failure("198.51.100.5")
	if limiter.Allow("198.51.100.5") {
		t.Fatal("source should be blocked after the failure limit")
	}
	if limiter.RetryAfter("198.51.100.5") <= 0 {
		t.Fatal("blocked source should have a retry duration")
	}

	now = now.Add(time.Minute)
	if !limiter.Allow("198.51.100.5") {
		t.Fatal("source should be allowed after the window")
	}
	limiter.Failure("198.51.100.5")
	limiter.Success("198.51.100.5")
	if !limiter.Allow("198.51.100.5") {
		t.Fatal("successful authentication should reset failures")
	}
}

func TestLoginLimiterRejectsInvalidConfiguration(t *testing.T) {
	if _, err := NewLoginLimiter(0, time.Minute); err == nil {
		t.Fatal("expected invalid failure limit error")
	}
	if _, err := NewLoginLimiter(1, 0); err == nil {
		t.Fatal("expected invalid failure window error")
	}
}

func TestLoginLimiterCapsTrackedSources(t *testing.T) {
	limiter, err := NewLoginLimiter(1, time.Hour)
	if err != nil {
		t.Fatalf("create limiter: %v", err)
	}
	for index := 0; index <= maxTrackedLoginSources; index++ {
		limiter.Failure(string(rune(index + 1)))
	}
	if len(limiter.entries) > maxTrackedLoginSources {
		t.Fatalf("tracked sources = %d, want at most %d", len(limiter.entries), maxTrackedLoginSources)
	}
}
