package auth

import (
	"sync"
	"time"
)

// Rate limiting is an in-memory token bucket. Horizontal scaling is a non-goal
// (D00), so putting it in the database buys nothing and costs a query on the
// hottest path. Restarting resets every bucket, and that is accepted: for a
// rate limit, deciding fast matters more than surviving a restart (D15 4.3-2).

// Limit is one threshold from D15 4.3-2: Burst attempts, refilled to full over
// Window. Both values come from that table; nothing here hardcodes them, so the
// document stays the single source and a change is a change of configuration.
type Limit struct {
	Burst  int
	Window time.Duration
}

// Limiter hands out allowances per key. A key is whatever the threshold is
// counted by — an IP, an account, an order number — and callers keep those
// namespaces apart by prefixing ("login:ip:1.2.3.4"), because two thresholds
// sharing a key would silently halve both.
type Limiter struct {
	// now is injectable so the tests can move time without sleeping. A rate
	// limiter tested with real sleeps is a test that is slow when it passes and
	// flaky when the machine is busy.
	now func() time.Time

	mu      sync.Mutex
	buckets map[string]*bucket
}

type bucket struct {
	tokens float64
	last   time.Time
}

// NewLimiter returns a limiter reading the clock. Pass a fake clock with
// NewLimiterAt in tests.
func NewLimiter() *Limiter { return NewLimiterAt(time.Now) }

// NewLimiterAt lets the caller supply the clock.
func NewLimiterAt(now func() time.Time) *Limiter {
	return &Limiter{now: now, buckets: make(map[string]*bucket)}
}

// Allow consumes one token for key under lim, reporting whether the request may
// proceed. A zero or negative Burst denies everything: an unconfigured limit
// must not read as "unlimited", which is the direction that fails open.
func (l *Limiter) Allow(key string, lim Limit) bool {
	if lim.Burst <= 0 || lim.Window <= 0 {
		return false
	}
	now := l.now()

	l.mu.Lock()
	defer l.mu.Unlock()

	b, ok := l.buckets[key]
	if !ok {
		// A new key starts full, then immediately spends one.
		l.buckets[key] = &bucket{tokens: float64(lim.Burst) - 1, last: now}
		return true
	}

	// Refill continuously rather than in window-sized steps. Stepped windows
	// let a caller spend a full burst at the end of one window and another at
	// the start of the next — twice the intended rate across the boundary.
	elapsed := now.Sub(b.last)
	if elapsed > 0 {
		perToken := lim.Window / time.Duration(lim.Burst)
		if perToken > 0 {
			b.tokens += float64(elapsed) / float64(perToken)
			if b.tokens > float64(lim.Burst) {
				b.tokens = float64(lim.Burst)
			}
		}
		b.last = now
	}

	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// Forget drops a key's bucket. Used after a successful login so that a person
// who mistyped their password four times is not still throttled afterwards.
func (l *Limiter) Forget(key string) {
	l.mu.Lock()
	delete(l.buckets, key)
	l.mu.Unlock()
}

// Sweep removes buckets untouched for longer than maxIdle. Without it the map
// grows once per distinct IP forever, which is a slow leak on a public site.
// Callers run it from the same goroutine that already ticks; there is no
// background worker (NFR-103).
func (l *Limiter) Sweep(maxIdle time.Duration) int {
	cutoff := l.now().Add(-maxIdle)
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for k, b := range l.buckets {
		if b.last.Before(cutoff) {
			delete(l.buckets, k)
			n++
		}
	}
	return n
}

// Limits holds the thresholds of D15 4.3-2. They are values, not constants, so
// an installation can loosen them without a rebuild and so the tests can use
// small numbers.
type Limits struct {
	LoginPerIP        Limit
	LoginPerAccount   Limit
	PasswordResetIP   Limit
	VerifyMailAccount Limit
	SignupPerIP       Limit
	GuestOrderIP      Limit
	GuestOrderNo      Limit
	AdminTreeIP       Limit
	ReauthAccount     Limit
	MailTestAccount   Limit
}

// DefaultLimits is D15 4.3-2 transcribed. The payment webhook (P-905) is
// deliberately absent: answering 429 makes the PG retry, and that retry storm
// is the thing D50 warns about. Its defence is a body-size cap and signature
// verification, not throttling.
func DefaultLimits() Limits {
	return Limits{
		LoginPerIP:        Limit{Burst: 10, Window: time.Minute},
		LoginPerAccount:   Limit{Burst: 5, Window: time.Minute},
		PasswordResetIP:   Limit{Burst: 3, Window: time.Hour},
		VerifyMailAccount: Limit{Burst: 3, Window: time.Hour},
		SignupPerIP:       Limit{Burst: 5, Window: time.Hour},
		GuestOrderIP:      Limit{Burst: 5, Window: time.Minute},
		GuestOrderNo:      Limit{Burst: 3, Window: time.Hour},
		AdminTreeIP:       Limit{Burst: 60, Window: time.Minute},
		ReauthAccount:     Limit{Burst: 5, Window: time.Minute},
		MailTestAccount:   Limit{Burst: 5, Window: time.Hour},
	}
}
