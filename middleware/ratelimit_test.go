package middleware

import (
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync"
	"testing"
	"time"
)

func rateLimited(t *testing.T, cfg *RateLimitConfig) (*RateLimit, http.Handler) {
	t.Helper()
	rl, err := NewRateLimit(cfg)
	if err != nil {
		t.Fatalf("NewRateLimit: %v", err)
	}
	return rl, rl.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
}

func hit(h http.Handler, remote string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.RemoteAddr = remote
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func TestRateLimitBurstThenRefill(t *testing.T) {
	rl, h := rateLimited(t, &RateLimitConfig{RPS: 1, Burst: 2})
	current := time.Now()
	rl.now = func() time.Time { return current }

	if w := hit(h, "10.0.0.1:1234"); w.Code != http.StatusOK {
		t.Fatalf("first request = %d", w.Code)
	}
	if w := hit(h, "10.0.0.1:1234"); w.Code != http.StatusOK {
		t.Fatalf("second request = %d (burst is 2)", w.Code)
	}
	w := hit(h, "10.0.0.1:1234")
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("third request = %d, want 429", w.Code)
	}
	if w.Header().Get("Retry-After") != "1" {
		t.Fatalf("429 must carry Retry-After: 1, got %q", w.Header().Get("Retry-After"))
	}

	// One second at 1 RPS refills one token.
	current = current.Add(time.Second)
	if w := hit(h, "10.0.0.1:1234"); w.Code != http.StatusOK {
		t.Fatalf("post-refill request = %d", w.Code)
	}
}

// Buckets are per key — by default the client IP, ports ignored.
func TestRateLimitKeysAreIndependent(t *testing.T) {
	rl, h := rateLimited(t, &RateLimitConfig{RPS: 1, Burst: 1})
	current := time.Now()
	rl.now = func() time.Time { return current }

	if w := hit(h, "10.0.0.1:1111"); w.Code != http.StatusOK {
		t.Fatalf("first client = %d", w.Code)
	}
	if w := hit(h, "10.0.0.1:2222"); w.Code != http.StatusTooManyRequests {
		t.Fatalf("same IP, different port = %d, want 429 (one bucket per IP)", w.Code)
	}
	if w := hit(h, "10.0.0.2:1111"); w.Code != http.StatusOK {
		t.Fatalf("different IP = %d, want its own bucket", w.Code)
	}
}

func TestRateLimitCustomKey(t *testing.T) {
	rl, h := rateLimited(t, &RateLimitConfig{
		RPS: 1, Burst: 1,
		KeyFunc: func(r *http.Request) string { return r.Header.Get("X-Api-Key") },
	})
	current := time.Now()
	rl.now = func() time.Time { return current }

	req := func(key string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/x", nil)
		r.Header.Set("X-Api-Key", key)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := req("a"); w.Code != http.StatusOK {
		t.Fatalf("key a = %d", w.Code)
	}
	if w := req("a"); w.Code != http.StatusTooManyRequests {
		t.Fatalf("key a again = %d", w.Code)
	}
	if w := req("b"); w.Code != http.StatusOK {
		t.Fatalf("key b = %d", w.Code)
	}
}

func TestRateLimitValidatesConfig(t *testing.T) {
	if _, err := NewRateLimit(&RateLimitConfig{RPS: 0, Burst: 1}); err == nil {
		t.Fatalf("RPS must be positive")
	}
	for _, rps := range []float64{math.NaN(), math.Inf(1)} {
		if _, err := NewRateLimit(&RateLimitConfig{RPS: rps, Burst: 1}); err == nil {
			t.Fatalf("RPS %v must be refused: it turns every bucket into NaN", rps)
		}
	}
	if _, err := NewRateLimit(&RateLimitConfig{RPS: 1, Burst: 0}); err == nil {
		t.Fatalf("Burst must be at least 1")
	}
	if _, err := NewRateLimit(&RateLimitConfig{RPS: 1, Burst: 1, MaxKeys: -1}); err == nil {
		t.Fatalf("MaxKeys must not be negative")
	}
	rl, err := NewRateLimit(&RateLimitConfig{RPS: 1, Burst: 1})
	if err != nil {
		t.Fatalf("NewRateLimit: %v", err)
	}
	if rl.maxKeys != defaultMaxKeys {
		t.Fatalf("zero MaxKeys = %d keys, want the default %d", rl.maxKeys, defaultMaxKeys)
	}
}

// A bucket is never forgotten before it would have refilled. At five an
// hour, six idle minutes earn half a token — the five-minute sweep this
// replaced dropped the bucket and handed the client a fresh burst of five.
func TestRateLimitKeepsUnrefilledBuckets(t *testing.T) {
	rl, h := rateLimited(t, &RateLimitConfig{RPS: 5.0 / 3600, Burst: 5})
	current := time.Now()
	rl.now = func() time.Time { return current }

	for i := range 5 {
		if w := hit(h, "10.0.0.1:1"); w.Code != http.StatusOK {
			t.Fatalf("request %d = %d, want the burst", i+1, w.Code)
		}
	}
	if w := hit(h, "10.0.0.1:1"); w.Header().Get("Retry-After") != "720" {
		t.Fatalf("Retry-After = %q, want 720 (one token every 12 minutes)", w.Header().Get("Retry-After"))
	}
	current = current.Add(6 * time.Minute)
	// Other clients' requests are what forget idle buckets.
	for i := range 10 {
		hit(h, fmt.Sprintf("10.1.0.%d:1", i))
	}
	if _, ok := rl.buckets["10.0.0.1"]; !ok {
		t.Fatalf("the bucket was forgotten after six minutes; it needs an hour to refill")
	}
	if w := hit(h, "10.0.0.1:1"); w.Code != http.StatusTooManyRequests {
		t.Fatalf("after six idle minutes = %d, want 429", w.Code)
	}

	// An hour after its last request it has refilled, and forgetting it
	// changes nothing. Eleven buckets are idle by then, two forgotten per
	// request.
	current = current.Add(time.Hour)
	for range 6 {
		hit(h, "10.2.0.1:1")
	}
	if _, ok := rl.buckets["10.0.0.1"]; ok {
		t.Fatalf("a bucket idle past its refill time should be forgotten")
	}
}

// Buckets idle past the five-minute floor are forgotten as traffic flows,
// at most two per request, so no request pays for a pass over the table.
func TestRateLimitForgetsIdleBucketsTwoAtATime(t *testing.T) {
	rl, h := rateLimited(t, &RateLimitConfig{RPS: 1, Burst: 1})
	current := time.Now()
	rl.now = func() time.Time { return current }

	for i := range 10 {
		hit(h, fmt.Sprintf("10.0.0.%d:1", i))
	}
	// One token refills in a second, but nothing goes before the floor.
	current = current.Add(5*time.Minute - time.Second)
	hit(h, "10.0.1.1:1")
	if got := len(rl.buckets); got != 11 {
		t.Fatalf("before the floor: %d buckets, want all 11", got)
	}
	current = current.Add(time.Second)
	hit(h, "10.0.1.1:1")
	if got := len(rl.buckets); got != 9 {
		t.Fatalf("one request past the floor: %d buckets, want 9 (two forgotten)", got)
	}
	for range 4 {
		hit(h, "10.0.1.1:1")
	}
	if got := len(rl.buckets); got != 1 {
		t.Fatalf("once the backlog drained: %d buckets, want only the active one", got)
	}
}

// At MaxKeys a new key evicts the least recently seen one, not the first
// inserted: a client that keeps coming back keeps its bucket, and its debt.
func TestRateLimitMaxKeysEvictsLeastRecentlySeen(t *testing.T) {
	rl, h := rateLimited(t, &RateLimitConfig{RPS: 1, Burst: 1, MaxKeys: 2})
	current := time.Now()
	rl.now = func() time.Time { return current }
	step := func() { current = current.Add(time.Millisecond) }

	hit(h, "10.0.0.1:1") // a spends its token
	step()
	hit(h, "10.0.0.2:1") // b
	step()
	hit(h, "10.0.0.1:1") // a again, now the most recently seen
	step()
	hit(h, "10.0.0.3:1") // c: the table is full, and b goes

	if got := len(rl.buckets); got != 2 {
		t.Fatalf("%d buckets, want MaxKeys (2)", got)
	}
	if _, ok := rl.buckets["10.0.0.2"]; ok {
		t.Fatalf("b, the least recently seen key, should have been evicted")
	}
	if w := hit(h, "10.0.0.1:1"); w.Code != http.StatusTooManyRequests {
		t.Fatalf("a = %d, want 429: its bucket must survive the eviction", w.Code)
	}
}

// A flood of distinct keys stays within MaxKeys: the table that used to
// grow to a million entries is bounded.
func TestRateLimitTableStaysBounded(t *testing.T) {
	rl, h := rateLimited(t, &RateLimitConfig{RPS: 1, Burst: 1, MaxKeys: 100})
	current := time.Now()
	rl.now = func() time.Time { return current }
	for i := range 1000 {
		if w := hit(h, fmt.Sprintf("10.0.%d.%d:1", i/256, i%256)); w.Code != http.StatusOK {
			t.Fatalf("new key %d = %d: a fresh key always has a full bucket", i, w.Code)
		}
	}
	if got := len(rl.buckets); got != 100 {
		t.Fatalf("%d buckets after 1000 keys, want MaxKeys (100)", got)
	}
	// The recency list holds exactly what the map does.
	n := 0
	for b := rl.recent.next; b != &rl.recent; b = b.next {
		if rl.buckets[b.key] != b {
			t.Fatalf("list entry %q is not the map's bucket", b.key)
		}
		n++
	}
	if n != len(rl.buckets) {
		t.Fatalf("list has %d buckets, map %d", n, len(rl.buckets))
	}
}

// Requests arrive concurrently; under the race detector this proves the
// table and its recency list are only touched under the lock, and that
// evictions racing with refills leave them in agreement.
func TestRateLimitConcurrent(t *testing.T) {
	rl, h := rateLimited(t, &RateLimitConfig{RPS: 1000, Burst: 5, MaxKeys: 50})
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 500 {
				hit(h, fmt.Sprintf("10.%d.0.%d:1", g, i%120)) // 960 keys for 50 slots
			}
		}()
	}
	wg.Wait()

	rl.mu.Lock()
	defer rl.mu.Unlock()
	if len(rl.buckets) > 50 {
		t.Fatalf("%d buckets, want at most MaxKeys (50)", len(rl.buckets))
	}
	n := 0
	for b := rl.recent.next; b != &rl.recent; b = b.next {
		if rl.buckets[b.key] != b || b.next.prev != b {
			t.Fatalf("list and map disagree at %q", b.key)
		}
		n++
	}
	if n != len(rl.buckets) {
		t.Fatalf("list has %d buckets, map %d", n, len(rl.buckets))
	}
}

// One host is routinely given a whole /64, so the default key is the /64;
// an IPv4-mapped address is the IPv4 client it maps.
func TestRateLimitKeysIPv6ByPrefix(t *testing.T) {
	rl, h := rateLimited(t, &RateLimitConfig{RPS: 1, Burst: 1})
	current := time.Now()
	rl.now = func() time.Time { return current }

	for _, step := range []struct {
		remote string
		want   int
		why    string
	}{
		{"[2001:db8:1:2::1]:1234", http.StatusOK, "first address in the /64"},
		{"[2001:db8:1:2:ffff:ffff:ffff:ffff]:1234", http.StatusTooManyRequests, "same /64, same bucket"},
		{"[2001:db8:1:3::1]:1234", http.StatusOK, "the next /64 is another client"},
		{"192.0.2.7:1", http.StatusOK, "an IPv4 client"},
		{"[::ffff:192.0.2.7]:1", http.StatusTooManyRequests, "the same client, IPv4-mapped"},
	} {
		if w := hit(h, step.remote); w.Code != step.want {
			t.Fatalf("%s (%s) = %d, want %d", step.remote, step.why, w.Code, step.want)
		}
	}
}

// Behind a proxy every request's peer is the proxy; ClientIPKey with the
// proxy's prefix gives each client behind it a bucket of its own.
func TestRateLimitBehindProxy(t *testing.T) {
	rl, h := rateLimited(t, &RateLimitConfig{
		RPS: 1, Burst: 1,
		KeyFunc: ClientIPKey(netip.MustParsePrefix("10.0.0.0/8")),
	})
	current := time.Now()
	rl.now = func() time.Time { return current }

	via := func(client string) int {
		r := httptest.NewRequest(http.MethodGet, "/x", nil)
		r.RemoteAddr = "10.0.0.5:4444" // the load balancer
		r.Header.Set("X-Forwarded-For", client)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	if got := via("203.0.113.1"); got != http.StatusOK {
		t.Fatalf("client A = %d", got)
	}
	if got := via("203.0.113.2"); got != http.StatusOK {
		t.Fatalf("client B = %d, want its own bucket", got)
	}
	if got := via("203.0.113.1"); got != http.StatusTooManyRequests {
		t.Fatalf("client A again = %d, want 429", got)
	}
}

func TestClientIPKey(t *testing.T) {
	prefixes := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("2001:db8:ffff::/48")}
	key := ClientIPKey(prefixes...)
	// The prefixes are copied: changing the caller's slice afterwards
	// changes nothing.
	prefixes[0] = netip.MustParsePrefix("203.0.113.0/24")

	for _, tc := range []struct {
		name   string
		remote string
		xff    []string
		want   string
	}{
		{"an untrusted peer is the client, whatever the header says", "203.0.113.9:1", []string{"198.51.100.1"}, "203.0.113.9"},
		{"a trusted peer: the entry it appended", "10.0.0.5:1", []string{"198.51.100.1"}, "198.51.100.1"},
		{"entries left of the client are never read", "10.0.0.5:1", []string{"192.0.2.66, 198.51.100.1"}, "198.51.100.1"},
		{"a chain of trusted hops", "10.0.0.5:1", []string{"198.51.100.1, 10.2.0.1, 10.3.0.1"}, "198.51.100.1"},
		{"repeated header lines are one list", "10.0.0.5:1", []string{"192.0.2.66, 198.51.100.1", "10.2.0.1"}, "198.51.100.1"},
		{"the last header line holds the nearest hop", "10.0.0.5:1", []string{"192.0.2.66", "198.51.100.1"}, "198.51.100.1"},
		{"an entry with a port", "10.0.0.5:1", []string{"198.51.100.1:4711"}, "198.51.100.1"},
		{"IPv6, keyed by its /64", "[2001:db8:ffff::1]:1", []string{"2001:db8:1:2:3:4:5:6"}, "2001:db8:1:2::/64"},
		{"bracketed IPv6 with a port", "10.0.0.5:1", []string{"[2001:db8:1:2::9]:4711"}, "2001:db8:1:2::/64"},
		{"bracketed IPv6 without one", "10.0.0.5:1", []string{"[2001:db8:1:2::9]"}, "2001:db8:1:2::/64"},
		{"no header: the trusted peer itself", "10.0.0.5:1", nil, "10.0.0.5"},
		{"every hop trusted: the leftmost", "10.0.0.5:1", []string{"10.9.9.9, 10.2.0.1"}, "10.9.9.9"},
		{"an entry that is no address stops at the hop that wrote it", "10.0.0.5:1", []string{"198.51.100.1, unknown, 10.2.0.1"}, "10.2.0.1"},
		{"an empty entry stops there too", "10.0.0.5:1", []string{"198.51.100.1, , 10.2.0.1"}, "10.2.0.1"},
		{"an IPv4-mapped peer matches IPv4 prefixes", "[::ffff:10.0.0.5]:1", []string{"198.51.100.1"}, "198.51.100.1"},
		{"a bare peer address", "10.0.0.5", []string{"198.51.100.1"}, "198.51.100.1"},
		{"a peer that is no IP address", "@", nil, "@"},
	} {
		r := httptest.NewRequest(http.MethodGet, "/x", nil)
		r.RemoteAddr = tc.remote
		for _, v := range tc.xff {
			r.Header.Add("X-Forwarded-For", v)
		}
		if got := key(r); got != tc.want {
			t.Errorf("%s: key = %q, want %q", tc.name, got, tc.want)
		}
	}

	// With no prefixes — the default — the header is never read.
	r := httptest.NewRequest(http.MethodGet, "/x", nil)
	r.RemoteAddr = "10.0.0.5:1"
	r.Header.Set("X-Forwarded-For", "198.51.100.1")
	if got := ClientIPKey()(r); got != "10.0.0.5" {
		t.Errorf("default key = %q, want the peer", got)
	}
}
