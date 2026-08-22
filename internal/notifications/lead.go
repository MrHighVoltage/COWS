package notifications

import "time"

// Lead returns how far ahead of the deadline a warning for a window of this
// length should be sent, and whether to send one at all. The lead is a fixed
// fraction of the window, capped so a very long retention window does not warn
// weeks in advance, and floored so a very short stop window does not produce a
// notice too late to act on. At Max*Divisor the two bounds meet, so the
// function is continuous: with the defaults, a three-day window yields exactly
// the 24-hour cap from either direction.
func (p LeadPolicy) Lead(window time.Duration) (time.Duration, bool) {
	if window <= 0 || p.Divisor < 1 {
		return 0, false
	}
	lead := window / time.Duration(p.Divisor)
	if lead > p.Max {
		lead = p.Max
	}
	if lead < p.Min {
		return 0, false
	}
	return lead, true
}
