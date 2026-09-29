package api

import (
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// limiter counts failed attempts per client. The first few failures are
// free; from then on every failure locks the client out for exponentially
// longer, up to a cap. While locked out, even a correct attempt is refused,
// or the lockout would not slow guessing at all.
type limiter struct {
	mu     sync.Mutex
	free   int           // failures before the first lockout
	base   time.Duration // first lockout
	max    time.Duration // longest lockout
	forget time.Duration // quiet time after which a client starts over
	now    func() time.Time
	m      map[string]*limitState
}

type limitState struct {
	fails int
	last  time.Time // most recent failure
	until time.Time // locked out until
}

// maxTracked bounds the table; stale entries are pruned past it.
const maxTracked = 4096

// newLimiter gives 5 free failures, then locks out for 15 s, 30 s, 1 min …
// up to 15 min per further failure.
func newLimiter(now func() time.Time) *limiter {
	return &limiter{
		free:   5,
		base:   15 * time.Second,
		max:    15 * time.Minute,
		forget: time.Hour,
		now:    now,
		m:      map[string]*limitState{},
	}
}

// check reports whether key may attempt now, and if not, how long to wait.
func (l *limiter) check(key string) (time.Duration, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	st := l.m[key]
	if st == nil {
		return 0, true
	}
	if now := l.now(); now.Before(st.until) {
		return st.until.Sub(now), false
	}
	return 0, true
}

// fail records a failed attempt by key.
func (l *limiter) fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	st := l.m[key]
	if st == nil || l.stale(st, now) {
		st = &limitState{}
		l.m[key] = st
	}
	st.fails++
	st.last = now
	if st.fails >= l.free {
		d := l.max
		if shift := st.fails - l.free; shift < 30 {
			if b := l.base << shift; b > 0 && b < l.max {
				d = b
			}
		}
		st.until = now.Add(d)
	}
	if len(l.m) > maxTracked {
		for k, s := range l.m {
			if l.stale(s, now) {
				delete(l.m, k)
			}
		}
	}
}

// reset forgets key's failures after a success.
func (l *limiter) reset(key string) {
	l.mu.Lock()
	delete(l.m, key)
	l.mu.Unlock()
}

func (l *limiter) stale(st *limitState, now time.Time) bool {
	return !now.Before(st.until) && now.Sub(st.last) > l.forget
}

// limitKey identifies a client for rate limiting: its IPv4 address, or its
// IPv6 /64, since one host can trivially rotate through its own /64.
func limitKey(r *http.Request) string {
	ip, ok := remoteIP(r)
	if !ok {
		return r.RemoteAddr
	}
	ip = ip.WithZone("")
	if ip.Is6() {
		if p, err := ip.Prefix(64); err == nil {
			return p.String()
		}
	}
	return ip.String()
}

// tooMany writes 429 with Retry-After in whole seconds.
func tooMany(w http.ResponseWriter, retry time.Duration) {
	secs := int(math.Ceil(retry.Seconds()))
	if secs < 1 {
		secs = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	Error(w, http.StatusTooManyRequests, "too many failed attempts; try again in %s", (time.Duration(secs) * time.Second).String())
}
