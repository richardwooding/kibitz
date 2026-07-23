package relay

import (
	"net"
	"sync"

	"golang.org/x/time/rate"
)

// ipLimiter rate-limits connection attempts per client IP. Behind PAKE the
// phrase space (~2^27) is only attackable online, so throttling joins is the
// second half of the wrong-phrase defense.
type ipLimiter struct {
	mu    sync.Mutex
	m     map[string]*rate.Limiter
	limit rate.Limit
	burst int
}

// ipLimiterCap bounds the tracking map; when full it resets, which
// briefly forgives old offenders rather than growing without bound.
const ipLimiterCap = 16384

func newIPLimiter(limit rate.Limit, burst int) *ipLimiter {
	return &ipLimiter{m: map[string]*rate.Limiter{}, limit: limit, burst: burst}
}

func (l *ipLimiter) allow(remoteAddr string) bool {
	ip, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		ip = remoteAddr
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.m) >= ipLimiterCap {
		l.m = map[string]*rate.Limiter{}
	}
	lim, ok := l.m[ip]
	if !ok {
		lim = rate.NewLimiter(l.limit, l.burst)
		l.m[ip] = lim
	}
	return lim.Allow()
}
