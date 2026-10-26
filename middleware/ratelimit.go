package middleware

import (
	"errors"
	"math"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// RateLimitConfig is provided by your own provider. KeyFunc decides what a
// "client" is — nil means the address the connection came from, which
// behind a proxy is the proxy's (see ClientIPKey). Being a real Go struct
// built in your code, the func field is ordinary Go: key by API token,
// tenant, whatever the request carries.
type RateLimitConfig struct {
	// RPS is the sustained refill rate per key. Must be positive and
	// finite.
	RPS float64
	// Burst is the bucket size — how many requests a cold key may spend at
	// once. Must be at least 1.
	Burst int
	// KeyFunc extracts the limit key. Nil means ClientIPKey() with no
	// trusted proxies: the peer address in r.RemoteAddr, an IPv6 one by its
	// /64. Behind a load balancer, ingress or CDN that address is the
	// proxy's, so every client would share one bucket; set KeyFunc to
	// ClientIPKey with the proxies' prefixes there instead.
	KeyFunc func(*http.Request) string
	// MaxKeys caps how many keys are tracked at once; zero means 100,000
	// (about 12 MiB of buckets with the default key), negative is an
	// error. At the cap a new key evicts the least recently seen one, which
	// starts over with a full bucket if it returns.
	MaxKeys int
}

// RateLimit is a hand-rolled token bucket per key: the runtime stays
// stdlib-only, and the state is one small struct per active client.
// Exhausted keys get 429 with a Retry-After. The buckets are per process —
// two replicas mean two buckets per key.
//
// The table is bounded twice over. A bucket idle long enough to have
// refilled to full is forgotten, since keeping it would change nothing —
// never sooner, so a slow limit like five an hour is not reset by an idle
// spell, and never under five minutes. Each request forgets at most two
// such buckets, from the cold end of a recency list, so no request pays for
// a pass over the table. And at MaxKeys a new key evicts the least
// recently seen one, refilled or not: under a flood of distinct keys the
// limiter forgets a client rather than grow without bound or turn away
// clients it has no room to track.
type RateLimit struct {
	rps     float64
	burst   float64
	key     func(*http.Request) string
	maxKeys int
	// idle is how long a bucket goes unused before forgetting it is
	// indistinguishable from keeping it.
	idle time.Duration

	mu      sync.Mutex
	buckets map[string]*bucket
	// recent is the sentinel of a circular list of every bucket, most
	// recently seen first, so the idle ones gather at its tail.
	recent bucket
	// now is time.Now, replaceable in tests — a limiter that can only be
	// tested by sleeping is a limiter that never gets tested.
	now func() time.Time
}

type bucket struct {
	key        string
	tokens     float64
	last       time.Time
	prev, next *bucket
}

const (
	// defaultMaxKeys is MaxKeys when the config leaves it zero: far more
	// clients than one process sees within an idle window, unless someone
	// is cycling through addresses to exhaust it.
	defaultMaxKeys = 100_000
	// minIdle is the floor on how long an unused bucket is kept.
	minIdle = 5 * time.Minute
)

func NewRateLimit(cfg *RateLimitConfig) (*RateLimit, error) {
	// NaN and +Inf pass a plain sign check, and either turns every bucket's
	// arithmetic into NaN.
	if !(cfg.RPS > 0) || math.IsInf(cfg.RPS, 1) {
		return nil, errors.New("middleware: RateLimitConfig.RPS must be positive and finite")
	}
	if cfg.Burst < 1 {
		return nil, errors.New("middleware: RateLimitConfig.Burst must be at least 1")
	}
	if cfg.MaxKeys < 0 {
		return nil, errors.New("middleware: RateLimitConfig.MaxKeys must not be negative")
	}
	key := cfg.KeyFunc
	if key == nil {
		key = ClientIPKey()
	}
	maxKeys := cfg.MaxKeys
	if maxKeys == 0 {
		maxKeys = defaultMaxKeys
	}
	m := &RateLimit{
		rps:     cfg.RPS,
		burst:   float64(cfg.Burst),
		key:     key,
		maxKeys: maxKeys,
		idle:    idleAfter(cfg.Burst, cfg.RPS),
		buckets: map[string]*bucket{},
		now:     time.Now,
	}
	m.recent.prev, m.recent.next = &m.recent, &m.recent
	return m, nil
}

// idleAfter is how long a bucket must go unused before it can be forgotten:
// burst/rps seconds refills it from empty to full, so only after that is a
// fresh bucket the same thing — forgetting it sooner would hand the key
// back tokens it has not earned. minIdle is the floor.
func idleAfter(burst int, rps float64) time.Duration {
	refill := math.Ceil(float64(burst) / rps * float64(time.Second))
	if refill >= math.MaxInt64 {
		return math.MaxInt64
	}
	return max(minIdle, time.Duration(refill))
}

func (m *RateLimit) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if retryAfter, ok := m.take(m.key(r)); !ok {
			w.Header().Set("Retry-After", retryAfter)
			writeJSONError(w, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// take spends one token from key's bucket, reporting the Retry-After value
// when none is available.
func (m *RateLimit) take(key string) (retryAfter string, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := m.now()
	b := m.buckets[key]
	if b == nil {
		if len(m.buckets) >= m.maxKeys {
			m.forget(m.recent.prev)
		}
		b = &bucket{key: key, tokens: m.burst}
		m.buckets[key] = b
	} else {
		if elapsed := now.Sub(b.last); elapsed > 0 {
			b.tokens = min(m.burst, b.tokens+elapsed.Seconds()*m.rps)
		}
		b.unlink()
	}
	b.last = now
	m.pushFront(b)
	m.expire(now)

	if b.tokens >= 1 {
		b.tokens--
		return "", true
	}
	// Formatted as a float so that an absurdly slow rate cannot overflow
	// an int into a negative delay.
	return strconv.FormatFloat(math.Ceil((1-b.tokens)/m.rps), 'f', 0, 64), false
}

// expire forgets up to two buckets from the cold end of the recency list,
// each only if it has been idle for m.idle. Two, because a request adds at
// most one bucket: the idle backlog shrinks while traffic flows, and no
// request ever pays for more — a sweep of the whole table holds the lock
// for tens of milliseconds at a million keys.
func (m *RateLimit) expire(now time.Time) {
	for range 2 {
		b := m.recent.prev
		if b == &m.recent || now.Sub(b.last) < m.idle {
			return
		}
		m.forget(b)
	}
}

func (m *RateLimit) forget(b *bucket) {
	b.unlink()
	delete(m.buckets, b.key)
}

// pushFront makes b the most recently seen bucket.
func (m *RateLimit) pushFront(b *bucket) {
	b.prev, b.next = &m.recent, m.recent.next
	m.recent.next.prev = b
	m.recent.next = b
}

func (b *bucket) unlink() {
	b.prev.next = b.next
	b.next.prev = b.prev
	b.prev, b.next = nil, nil
}

// ClientIPKey returns a RateLimitConfig.KeyFunc keying each request by the
// client that sent it, as seen through the trusted proxies — your load
// balancers, ingress, CDN — given as prefixes:
//
//	KeyFunc: middleware.ClientIPKey(netip.MustParsePrefix("10.0.0.0/8"))
//
// The walk starts at the peer in r.RemoteAddr. While that address is inside
// a trusted prefix, it continues into X-Forwarded-For from the right: each
// entry is the peer the trusted hop after it saw, so the first entry no
// trusted prefix covers is the client. Everything left of that is whatever
// the client chose to send and is never read, so the header cannot be used
// to pick a key; an entry that is not an address ends the walk at the
// trusted hop that wrote it, and a request whose hops are all trusted is
// keyed by the leftmost. A client inside a trusted prefix can name its own
// key, so trust exactly your proxies and nothing broader.
//
// An IPv6 address is keyed by its /64, the block one host is routinely
// given — per address, a single client could mint a fresh bucket per
// request. An IPv4-mapped IPv6 address counts as the IPv4 one, and is
// matched against IPv4 prefixes. A peer that is not an IP address (a Unix
// socket) is keyed by r.RemoteAddr as it is.
//
// With no prefixes this is the default key: the peer alone, which behind a
// proxy is the proxy.
func ClientIPKey(trusted ...netip.Prefix) func(*http.Request) string {
	trusted = slices.Clone(trusted)
	isTrusted := func(a netip.Addr) bool {
		for _, p := range trusted {
			if p.Contains(a) {
				return true
			}
		}
		return false
	}
	return func(r *http.Request) string {
		addr, ok := parseHop(r.RemoteAddr)
		if !ok {
			return r.RemoteAddr
		}
		if isTrusted(addr) {
			addr = forwardedClient(r.Header.Values("X-Forwarded-For"), addr, isTrusted)
		}
		if addr.Is6() {
			p, _ := addr.Prefix(64)
			return p.String()
		}
		return addr.String()
	}
}

// forwardedClient walks X-Forwarded-For from the right, starting at the
// trusted peer addr, and returns the first address no trusted prefix
// covers. Repeated header lines are one list in order, so the last line's
// last entry comes first. Running out of entries leaves the leftmost one,
// trusted though it is; an entry that does not parse — empty, "unknown", a
// name — leaves the trusted hop that wrote it.
func forwardedClient(lines []string, addr netip.Addr, trusted func(netip.Addr) bool) netip.Addr {
	for i := len(lines) - 1; i >= 0; i-- {
		rest := lines[i]
		for rest != "" {
			entry := rest
			if j := strings.LastIndexByte(rest, ','); j >= 0 {
				rest, entry = rest[:j], rest[j+1:]
			} else {
				rest = ""
			}
			hop, ok := parseHop(strings.TrimSpace(entry))
			if !ok {
				return addr
			}
			if addr = hop; !trusted(addr) {
				return addr
			}
		}
	}
	return addr
}

// parseHop reads an address the way RemoteAddr and X-Forwarded-For write
// one: bare, bracketed, or with a port ("192.0.2.1:443",
// "[2001:db8::1]:443"). The result is unmapped and zoneless, the form
// prefixes match against.
func parseHop(s string) (netip.Addr, bool) {
	if len(s) > 2 && s[0] == '[' && s[len(s)-1] == ']' {
		s = s[1 : len(s)-1]
	}
	addr, err := netip.ParseAddr(s)
	if err != nil {
		ap, perr := netip.ParseAddrPort(s)
		if perr != nil {
			return netip.Addr{}, false
		}
		addr = ap.Addr()
	}
	return addr.Unmap().WithZone(""), true
}
