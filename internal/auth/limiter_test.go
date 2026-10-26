package auth

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestLoginLimiterBlocksAndResets(t *testing.T) {
	now := time.Unix(100, 0)
	limiter, err := NewLoginLimiter(2, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	limiter.now = func() time.Time { return now }
	for range 2 {
		attempt, ok := limiter.Reserve("source")
		if !ok {
			t.Fatal("source denied before its limit")
		}
		attempt.Complete(false)
	}
	if _, ok := limiter.Reserve("source"); ok || limiter.RetryAfter("source") <= 0 {
		t.Fatal("source should be blocked after the failure limit")
	}
	now = now.Add(time.Minute)
	failed, ok := limiter.Reserve("source")
	if !ok {
		t.Fatal("source denied after the window")
	}
	failed.Complete(false)
	successful, ok := limiter.Reserve("source")
	if !ok {
		t.Fatal("source denied before success")
	}
	successful.Complete(true)
	if next, ok := limiter.Reserve("source"); !ok {
		t.Fatal("success did not reset failures")
	} else {
		next.Complete(true)
	}
}

func TestLoginLimiterReservesConcurrentBurst(t *testing.T) {
	limiter, _ := NewLoginLimiter(5, time.Minute)
	ready := make(chan struct{}, 20)
	release := make(chan struct{})
	var allowed atomic.Int32
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			attempt, ok := limiter.Reserve("source")
			if ok {
				allowed.Add(1)
			}
			ready <- struct{}{}
			<-release
			if ok {
				attempt.Complete(false)
			}
		}()
	}
	for range 20 {
		<-ready
	}
	close(release)
	wg.Wait()
	if allowed.Load() != 5 {
		t.Fatalf("burst allowances = %d, want 5", allowed.Load())
	}
}

func TestLoginLimiterSuccessPreservesOtherReservations(t *testing.T) {
	limiter, _ := NewLoginLimiter(2, time.Minute)
	first, _ := limiter.Reserve("source")
	second, _ := limiter.Reserve("source")
	first.Complete(true)
	third, ok := limiter.Reserve("source")
	if !ok {
		t.Fatal("completed success did not release its allowance")
	}
	if _, ok := limiter.Reserve("source"); ok {
		t.Fatal("success forgot other pending work")
	}
	second.Complete(false)
	second.Complete(true)
	third.Complete(false)
	if _, ok := limiter.Reserve("source"); ok {
		t.Fatal("repeated completion changed accounting")
	}
}

func TestLoginLimiterOldSuccessCannotClearNewWindowFailures(t *testing.T) {
	now := time.Unix(100, 0)
	limiter, _ := NewLoginLimiter(2, time.Minute)
	limiter.now = func() time.Time { return now }
	old, _ := limiter.Reserve("source")
	now = now.Add(time.Minute)
	current, ok := limiter.Reserve("source")
	if !ok {
		t.Fatal("one old reservation consumed the entire new window")
	}
	current.Complete(false)
	old.Complete(true)
	last, ok := limiter.Reserve("source")
	if !ok {
		t.Fatal("expected one allowance after the new failure")
	}
	defer last.Complete(false)
	if _, ok := limiter.Reserve("source"); ok {
		t.Fatal("old success erased a new-window failure")
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
	limiter, _ := NewLoginLimiter(1, time.Hour)
	for index := 0; index <= maxTrackedLoginSources; index++ {
		attempt, ok := limiter.Reserve(fmt.Sprint(index))
		if !ok {
			t.Fatal("idle source could not be evicted")
		}
		attempt.Complete(false)
	}
	if len(limiter.entries) > maxTrackedLoginSources {
		t.Fatalf("tracked sources = %d", len(limiter.entries))
	}
}
