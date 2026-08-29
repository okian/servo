// Package admin serves the endpoints that must not be public: liveness,
// readiness and Prometheus metrics.
//
// They are on their own listener, on their own port, for a reason worth
// stating plainly. /healthz and /readyz enumerate every component in the
// graph by name along with its status, and /metrics exposes request rates,
// latencies and error counts for every route. Together they describe the
// shape and health of the system precisely enough to be worth hiding —
// so the deployment binds this listener to the cluster network and never
// to the internet, and no ingress rule points at it.
//
// It is a package rather than a function in each main.go because all three
// transport variants need exactly the same thing, and three copies of a
// security boundary is three chances to get one wrong.
package admin

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/okian/servo/v3/servo"
)

// Checker is the part of a generated servo App this package needs: the two
// aggregate report methods. Taking an interface rather than *App is what
// lets one implementation serve three injectors, each with its own
// generated App type.
type Checker interface {
	Health(context.Context) servo.Report
	Ready(context.Context) servo.Report
}

// New builds the admin listener. metrics is the server's own registry
// handler rather than the global default one, so two Apps in one test
// binary never collide registering the same metric name twice.
func New(addr string, app Checker, metrics http.Handler) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", reportHandler(app.Health))
	mux.HandleFunc("GET /readyz", reportHandler(app.Ready))
	mux.Handle("GET /metrics", metrics)
	return &http.Server{Addr: addr, Handler: mux}
}

// response re-renders a servo.Report with a readable status string instead
// of NodeStatus's raw int value — NodeStatus.String() exists, but
// encoding/json only calls MarshalJSON/MarshalText, neither of which
// NodeStatus implements, so encoding a servo.Report directly would print
// "status":0 instead of "status":"ok".
type response struct {
	Clean bool   `json:"clean"`
	Nodes []node `json:"nodes"`
}

type node struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

func reportHandler(check func(context.Context) servo.Report) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		report := check(r.Context())

		resp := response{Clean: report.Clean()}
		for _, n := range report.Nodes {
			entry := node{Name: n.Name, Status: n.Status.String()}
			if n.Err != nil {
				entry.Error = n.Err.Error()
			}
			resp.Nodes = append(resp.Nodes, entry)
		}

		w.Header().Set("Content-Type", "application/json")
		if !report.Clean() {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		json.NewEncoder(w).Encode(resp)
	}
}
