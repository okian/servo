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
}
