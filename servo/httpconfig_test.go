package servo

import (
	"testing"
	"time"
)

// The emitted server reads these fields by name — the flat fields for the
// default group, Groups["name"] for the rest — so renaming any of them is a
// contract change this test is meant to catch.
func TestHTTPConfigGroupListeners(t *testing.T) {
	cfg := HTTPConfig{
		IP: "0.0.0.0", Port: 9000,
		Groups: map[string]HTTPListener{
			"telemetry": {IP: "127.0.0.1", Port: 9001, MaxBodyBytes: 1 << 16, ReadTimeout: time.Second},
			"internal":  {Port: 9002, CertFile: "c.pem", KeyFile: "k.pem"},
		},
	}
	if cfg.Groups["telemetry"].Port != 9001 || cfg.Groups["internal"].CertFile != "c.pem" {
		t.Fatalf("group listeners = %+v", cfg.Groups)
	}
	if cfg.Groups["telemetry"].ReadTimeout != time.Second {
		t.Fatalf("timeouts must ride along per listener: %+v", cfg.Groups["telemetry"])
	}
}
