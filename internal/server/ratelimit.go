package server

import (
	"math"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	codeRateLimited     = "rate_limited"
	defaultLimiterSize  = 10000
	limiterSweepEvery   = time.Minute
	maxForwardedEntries = 32
)

type bucket struct {
	tokens float64
	last   time.Time
}

type limiter struct {
	rate  float64
	burst float64
	max   int

	mu        sync.Mutex
	buckets   map[string]*bucket
	lastSweep time.Time
}

func newLimiter(perMinute, maxKeys int) *limiter {
	if perMinute <= 0 {
		return nil
	}
	if maxKeys <= 0 {
		maxKeys = defaultLimiterSize
	}
	return &limiter{rate: float64(perMinute) / 60, burst: float64(perMinute), max: maxKeys, buckets: map[string]*bucket{}}
}

func (l *limiter) idle() time.Duration {
	return time.Duration(l.burst / l.rate * float64(time.Second))
}

func (l *limiter) allow(key string, now time.Time) (bool, time.Duration) {
	if l == nil {
		return true, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Sub(l.lastSweep) >= limiterSweepEvery {
		l.sweep(now)
	}
	b := l.buckets[key]
	if b == nil {
		if len(l.buckets) >= l.max {
			l.sweep(now)
			if len(l.buckets) >= l.max {
				l.evictOldest()
			}
		}
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}
	if el := now.Sub(b.last).Seconds(); el > 0 {
		b.tokens = math.Min(l.burst, b.tokens+el*l.rate)
		b.last = now
	}
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	wait := time.Duration((1 - b.tokens) / l.rate * float64(time.Second))
	return false, wait
}

func (l *limiter) sweep(now time.Time) {
	l.lastSweep = now
	idle := l.idle()
	for k, b := range l.buckets {
		if now.Sub(b.last) >= idle {
			delete(l.buckets, k)
		}
	}
}

func (l *limiter) evictOldest() {
	var oldest string
	var at time.Time
	for k, b := range l.buckets {
		if oldest == "" || b.last.Before(at) {
			oldest, at = k, b.last
		}
	}
	delete(l.buckets, oldest)
}

func (l *limiter) size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buckets)
}

type limiters struct {
	login, write, read *limiter
}

func retryAfter(wait time.Duration) int {
	secs := int(math.Ceil(wait.Seconds()))
	if secs < 1 {
		secs = 1
	}
	return secs
}

func rateLimited(w http.ResponseWriter, wait time.Duration) {
	secs := retryAfter(wait)
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	writeError(w, http.StatusTooManyRequests, codeRateLimited, "too many requests, try again in "+strconv.Itoa(secs)+" s", nil)
}

func (s *Server) limitLogin(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if ok, wait := s.limits.login.allow(s.clientIP(r), s.now()); !ok {
			s.log.Warn("sign-in rate limit exceeded", "source_ip", s.clientIP(r), "path", r.URL.Path)
			rateLimited(w, wait)
			return
		}
		h(w, r)
	}
}

func (s *Server) limitUser(w http.ResponseWriter, r *http.Request, sess *session) bool {
	l := s.limits.read
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		l = s.limits.write
	}
	ok, wait := l.allow(sess.Subject, s.now())
	if !ok {
		rateLimited(w, wait)
	}
	return ok
}

func peerAddr(remote string) (netip.Addr, bool) {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, false
	}
	return a.Unmap().WithZone(""), true
}

func trusted(prefixes []netip.Prefix, a netip.Addr) bool {
	for _, p := range prefixes {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

func forwardedClient(remote string, xff []string, proxies []netip.Prefix) string {
	peer, ok := peerAddr(remote)
	if !ok {
		host, _, err := net.SplitHostPort(remote)
		if err != nil {
			return remote
		}
		return host
	}
	if len(proxies) == 0 || !trusted(proxies, peer) {
		return peer.String()
	}
	var hops []string
	for _, h := range xff {
		hops = append(hops, strings.Split(h, ",")...)
	}
	if len(hops) > maxForwardedEntries {
		hops = hops[len(hops)-maxForwardedEntries:]
	}
	client := peer
	for i := len(hops) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			break
		}
		a = a.Unmap().WithZone("")
		client = a
		if !trusted(proxies, a) {
			break
		}
	}
	return client.String()
}
