package server

import (
	"fmt"
	"net/netip"
	"testing"
	"time"
)

func TestLimiterBucket(t *testing.T) {
	l := newLimiter(3, 0)
	now := time.Unix(1000, 0)
	for i := 0; i < 3; i++ {
		if ok, _ := l.allow("a", now); !ok {
			t.Fatalf("request %d refused", i)
		}
	}
	ok, wait := l.allow("a", now)
	if ok || wait <= 0 || wait > 20*time.Second || retryAfter(wait) != 20 {
		t.Fatalf("4th request: ok=%v wait=%v", ok, wait)
	}
	if ok, _ := l.allow("b", now); !ok {
		t.Fatal("another key shares the bucket")
	}
	if ok, _ := l.allow("a", now.Add(10*time.Second)); ok {
		t.Fatal("refilled too early")
	}
	if ok, _ := l.allow("a", now.Add(21*time.Second)); !ok {
		t.Fatal("not refilled after 20 s")
	}
	if ok, _ := l.allow("a", now.Add(22*time.Second)); ok {
		t.Fatal("refill exceeded the rate")
	}
	if ok, _ := l.allow("a", now.Add(time.Hour)); !ok {
		t.Fatal("not refilled after an hour")
	}
	for i := 0; i < 2; i++ {
		if ok, _ := l.allow("a", now.Add(time.Hour)); !ok {
			t.Fatal("burst not restored to the full size")
		}
	}
	if ok, _ := l.allow("a", now.Add(time.Hour)); ok {
		t.Fatal("burst larger than per_minute")
	}

	off := newLimiter(0, 0)
	for i := 0; i < 1000; i++ {
		if ok, _ := off.allow("a", now); !ok {
			t.Fatal("a disabled limiter refused")
		}
	}
	if retryAfter(time.Millisecond) != 1 {
		t.Fatal("Retry-After must be at least 1")
	}
}

func TestLimiterBoundedMemory(t *testing.T) {
	l := newLimiter(10, 100)
	now := time.Unix(1000, 0)
	for i := 0; i < 1000; i++ {
		l.allow(fmt.Sprintf("10.0.%d.%d", i/256, i%256), now.Add(time.Duration(i)*time.Millisecond))
		if n := l.size(); n > 100 {
			t.Fatalf("%d buckets, max 100", n)
		}
	}
	l.allow("x", now.Add(2*time.Minute))
	if n := l.size(); n != 1 {
		t.Fatalf("idle buckets not evicted: %d left", n)
	}

	l = newLimiter(1, 2)
	l.allow("old", now)
	l.allow("new", now.Add(time.Second))
	l.allow("newest", now.Add(2*time.Second))
	l.mu.Lock()
	_, hasOld := l.buckets["old"]
	_, hasNew := l.buckets["new"]
	l.mu.Unlock()
	if hasOld || !hasNew {
		t.Fatal("the least recently used bucket must be evicted first")
	}
}

func TestForwardedClient(t *testing.T) {
	proxies := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("192.168.1.1/32"), netip.MustParsePrefix("fd00::/8")}
	cases := []struct {
		name    string
		remote  string
		xff     []string
		proxies []netip.Prefix
		want    string
	}{
		{"no proxies configured", "203.0.113.9:4000", []string{"198.51.100.1"}, nil, "203.0.113.9"},
		{"untrusted peer ignores XFF", "203.0.113.9:4000", []string{"198.51.100.1"}, proxies, "203.0.113.9"},
		{"trusted peer", "10.1.2.3:4000", []string{"198.51.100.1"}, proxies, "198.51.100.1"},
		{"spoofed left entries", "10.1.2.3:4000", []string{"6.6.6.6, 198.51.100.1"}, proxies, "198.51.100.1"},
		{"chain of trusted proxies", "10.1.2.3:4000", []string{"198.51.100.1, 192.168.1.1, 10.9.9.9"}, proxies, "198.51.100.1"},
		{"several headers", "10.1.2.3:4000", []string{"6.6.6.6", "198.51.100.1, 10.9.9.9"}, proxies, "198.51.100.1"},
		{"all trusted", "10.1.2.3:4000", []string{"10.5.5.5, 10.9.9.9"}, proxies, "10.5.5.5"},
		{"no header", "10.1.2.3:4000", nil, proxies, "10.1.2.3"},
		{"garbage stops the walk", "10.1.2.3:4000", []string{"198.51.100.1, garbage, 10.9.9.9"}, proxies, "10.9.9.9"},
		{"ipv6 peer", "[fd00::1]:4000", []string{"2001:db8::7"}, proxies, "2001:db8::7"},
		{"ipv4-mapped peer", "[::ffff:10.1.2.3]:4000", []string{"198.51.100.1"}, proxies, "198.51.100.1"},
		{"untrusted ipv6", "[2001:db8::1]:4000", []string{"198.51.100.1"}, proxies, "2001:db8::1"},
	}
	for _, c := range cases {
		if got := forwardedClient(c.remote, c.xff, c.proxies); got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
}
