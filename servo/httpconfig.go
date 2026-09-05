package servo

import "time"

// HTTPConfig configures the HTTP server that `servo generate` emits when a
// spec declares servo.HTTP(). servo never constructs one: the user writes
// an ordinary provider —
//
//	func NewHTTPConfig() (*servo.HTTPConfig, error)
//
// — and it resolves as a graph node like any other, so where the values
// come from (env, flags, a file) stays the user's business.
type HTTPConfig struct {
	// IP is the interface to bind; empty means all interfaces.
	IP string
	// Port is the TCP port to bind.
	Port uint16

	// CertFile and KeyFile enable TLS when both are set; with either
	// empty the server speaks plain HTTP.
	CertFile string
	KeyFile  string

	// MaxBodyBytes bounds each request body via http.MaxBytesReader.
	// Zero or negative uses the generated default of 1 MiB.
	MaxBodyBytes int64

	// Zero values mean no timeout, matching net/http.Server.
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
	IdleTimeout  time.Duration

	// Groups holds the listener for each group declared with
	// servo.Group("name") inside servo.HTTP(...) — one extra port per
	// entry, keyed by the group's name. The flat fields above are the
	// default group's listener. A declared group missing its entry fails
	// App construction with an error naming it. The zero map is a valid
	// config for an app that declares no groups.
	Groups map[string]HTTPListener
}

// HTTPListener is one named group's listener: the same knobs the flat
// HTTPConfig fields set for the default group, per group. It is a separate
// type rather than a nested HTTPConfig so a listener cannot recursively
// declare groups of its own.
type HTTPListener struct {
	// IP is the interface to bind; empty means all interfaces.
	IP   string
	Port uint16

	// CertFile and KeyFile enable TLS when both are set.
	CertFile string
	KeyFile  string

	// MaxBodyBytes bounds request bodies via http.MaxBytesReader;
	// <= 0 uses the generated default (1 MiB).
	MaxBodyBytes int64

	// Zero values mean no timeout, matching net/http.Server.
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
	IdleTimeout  time.Duration
}
