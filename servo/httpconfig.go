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
	// empty the server speaks plain HTTP, and logs a warning when only
	// one of them is. The pair is loaded while New builds the server, so
	// a missing, unreadable or mismatched file fails New with an error
	// naming the server and both files, before any Init runs.
	CertFile string
	KeyFile  string

	// MaxBodyBytes bounds each request body via http.MaxBytesReader.
	// Zero or negative uses the generated default of 1 MiB.
	MaxBodyBytes int64

	// MaxHeaderBytes bounds the request line and headers, as
	// net/http.Server's field of the same name does; zero uses its
	// default, http.DefaultMaxHeaderBytes (1 MB).
	MaxHeaderBytes int

	// The timeouts go to net/http.Server unchanged, and zero means what
	// it means there: no timeout — except that a zero ReadHeaderTimeout
	// falls back to ReadTimeout, and a zero IdleTimeout to ReadTimeout
	// too. Bound the header read on anything reachable from the internet:
	// with no header deadline a client can hold a connection open
	// indefinitely by never finishing its headers (the slowloris shape),
	// and a few hundred such clients exhaust a server. The generated
	// server logs a warning when it starts listening with nothing
	// bounding the header read.
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration

	// ShutdownDelay is how long the server goes on serving once Shutdown
	// has dropped its readiness, before it closes the listener and
	// drains. Load balancers and Kubernetes endpoints take a few seconds
	// to stop routing to an instance that is going away, and a listener
	// that closes first turns their last requests into connection
	// errors. The delay is cut short when Shutdown's context ends and
	// counts against the same deadline the drain does, so give Shutdown
	// a deadline of at least ShutdownDelay plus the slowest request you
	// mean to let finish. A server whose listener never bound skips it:
	// nothing can be routing to it. Zero closes the listener at once.
	ShutdownDelay time.Duration

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

	// CertFile and KeyFile enable TLS when both are set; see
	// HTTPConfig.CertFile.
	CertFile string
	KeyFile  string

	// MaxBodyBytes bounds request bodies via http.MaxBytesReader;
	// <= 0 uses the generated default (1 MiB).
	MaxBodyBytes int64

	// MaxHeaderBytes bounds the request line and headers; zero uses
	// http.DefaultMaxHeaderBytes.
	MaxHeaderBytes int

	// Zero values mean what they mean on net/http.Server; see
	// HTTPConfig.ReadHeaderTimeout. A group does not inherit the flat
	// fields' values: set its timeouts here.
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration

	// ShutdownDelay keeps this group serving after its readiness drops;
	// see HTTPConfig.ShutdownDelay. Every group's delay starts at the
	// same moment, so two groups with five seconds each wait five, not
	// ten.
	ShutdownDelay time.Duration
}
