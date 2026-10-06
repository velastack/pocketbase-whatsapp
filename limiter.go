package whatsapp

import (
	"sync"
	"time"
)

// Limits defines the abuse and cost protection limits.
//
// Every sent code is a paid WhatsApp authentication message so these
// limits are always enforced in addition to the app rate limit rules.
type Limits struct {
	// ResendCooldown is the min duration between 2 code requests for the same phone number
	// (default to 60s).
	ResendCooldown time.Duration

	// MaxRequestsPerPhone is the max number of code requests for the same phone number
	// within 24 hours (default to 10).
	MaxRequestsPerPhone int

	// MaxRequestsPerIP is the max number of code requests from the same IP
	// within IPWindow (default to 10).
	MaxRequestsPerIP int

	// IPWindow is the time window used for MaxRequestsPerIP (default to 10 minutes).
	IPWindow time.Duration

	// MaxAttempts is the max number of failed verification attempts
	// before the code is invalidated (default to 5).
	MaxAttempts int
}

func (l *Limits) setDefaults() {
	if l.ResendCooldown <= 0 {
		l.ResendCooldown = 60 * time.Second
	}
	if l.MaxRequestsPerPhone <= 0 {
		l.MaxRequestsPerPhone = 10
	}
	if l.MaxRequestsPerIP <= 0 {
		l.MaxRequestsPerIP = 10
	}
	if l.IPWindow <= 0 {
		l.IPWindow = 10 * time.Minute
	}
	if l.MaxAttempts <= 0 {
		l.MaxAttempts = 5
	}
}

// windowLimiter is a minimal in-memory sliding window limiter.
//
// The state is not persisted and resets on app restart.
type windowLimiter struct {
	mu   sync.Mutex
	hits map[string][]time.Time
	now  func() time.Time
}

func newWindowLimiter() *windowLimiter {
	return &windowLimiter{
		hits: map[string][]time.Time{},
		now:  time.Now,
	}
}

// allow registers a hit for key and reports whether it is within
// max hits for the provided window.
//
// Rejected hits are not registered.
func (l *windowLimiter) allow(key string, max int, window time.Duration) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	hits := pruneHits(l.hits[key], now.Add(-window))

	if len(hits) >= max {
		l.hits[key] = hits
		return false, hits[0].Add(window).Sub(now)
	}

	l.hits[key] = append(hits, now)

	return true, 0
}

// prune removes all hits older than maxWindow.
func (l *windowLimiter) prune(maxWindow time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	minTime := l.now().Add(-maxWindow)

	for key, hits := range l.hits {
		hits = pruneHits(hits, minTime)
		if len(hits) == 0 {
			delete(l.hits, key)
		} else {
			l.hits[key] = hits
		}
	}
}

func pruneHits(hits []time.Time, minTime time.Time) []time.Time {
	i := 0
	for i < len(hits) && !hits[i].After(minTime) {
		i++
	}
	return hits[i:]
}
