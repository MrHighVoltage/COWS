package notifications

import (
	"testing"
	"time"
)

func TestLeadIsProportionalAndBounded(t *testing.T) {
	policy := LeadPolicy{Divisor: 3, Max: 24 * time.Hour, Min: time.Hour}
	cases := []struct {
		name   string
		window time.Duration
		lead   time.Duration
		send   bool
	}{
		{name: "one hour stop window is below the floor", window: time.Hour, send: false},
		{name: "three hour window sits exactly on the floor", window: 3 * time.Hour, lead: time.Hour, send: true},
		{name: "just under the floor is suppressed", window: 3*time.Hour - 3*time.Second, send: false},
		{name: "six hour window", window: 6 * time.Hour, lead: 2 * time.Hour, send: true},
		{name: "two day window", window: 48 * time.Hour, lead: 16 * time.Hour, send: true},
		{name: "three day window is the crossover", window: 72 * time.Hour, lead: 24 * time.Hour, send: true},
		{name: "thirty day window is capped", window: 720 * time.Hour, lead: 24 * time.Hour, send: true},
		{name: "zero window sends nothing", window: 0, send: false},
		{name: "negative window sends nothing", window: -time.Hour, send: false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			lead, send := policy.Lead(testCase.window)
			if send != testCase.send {
				t.Fatalf("expected send=%v, got %v", testCase.send, send)
			}
			if send && lead != testCase.lead {
				t.Fatalf("expected lead %s, got %s", testCase.lead, lead)
			}
		})
	}
}
