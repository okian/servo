// Package admin serves the endpoints that must not be public: the two
// probes, the per-dependency health report, and Prometheus metrics.
//
// They are on their own listener, on their own port, for a reason worth
// stating plainly. /healthz and /readyz enumerate every component in the
// graph by name along with its status, and /metrics exposes request rates,
// latencies and error counts for every route. Together they describe the
// shape and health of the system precisely enough to be worth hiding —
// so the deployment binds this listener to the cluster network and never
// to the internet, and no ingress rule points at it.
//
// Each endpoint answers a different question, and an orchestrator acts on
// the answer, so which is which matters:
//
//   - /livez is the liveness probe: "is this process wedged?" It checks
//     nothing beyond the process answering at all. A failed liveness
//     probe restarts the container, and a restart only fixes what is
//     wrong inside the process. A database failover is outside it: had
//     liveness pinged the database, a forty-second failover would have
//     failed it on every replica at once and restarted the whole fleet
//     into Init, which fails fast while the database is still down.
//   - /readyz is the readiness probe: App.Ready, "should this instance
//     get traffic?" A failure takes the instance out of the load balancer
//     until it passes again; nothing is restarted.
//   - /healthz is App.Health: every dependency's own check, for dashboards,
//     alerts and the person on call. No probe points at it. Its checks
//     include Redis, which this service degrades around (chapter 17), so
//     even as a readiness probe it would take every replica out of
//     rotation together whenever the cache went down.
//
// It is a package rather than a function in each main.go because every
// transport variant needs exactly the same thing, and several copies of a
// security boundary is several chances to get one wrong.
package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"

	"github.com/okian/servo/v3/servo"
)

// Checker is the part of a generated servo App the endpoints need: the two
// aggregate report methods. Taking an interface rather than *App is what
// lets one implementation serve every injector that mounts it, each with
// its own generated App type.
type Checker interface {
	Health(context.Context) servo.Report
	Ready(context.Context) servo.Report
}

// App is what Mount wraps: the reports the endpoints serve, and the Run
// and Shutdown that servo.Main drives.
type App interface {
	Checker
	Run(context.Context) error
	Shutdown(context.Context) servo.Report
}

// New builds the admin listener. metrics is the server's own registry
// handler rather than the global default one, so two Apps in one test
// binary never collide registering the same metric name twice.
func New(addr string, app Checker, metrics http.Handler) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /livez", live)
	mux.HandleFunc("GET /readyz", reportHandler(app.Ready))
	mux.HandleFunc("GET /healthz", reportHandler(app.Health))
	mux.Handle("GET /metrics", metrics)
	return &http.Server{Addr: addr, Handler: mux}
}

// Mount returns app with the admin listener mounted beside it, for
// servo.Main to run as one:
//
//	servo.Main(func(ctx context.Context) (*admin.Server, error) {
//		app, err := New(ctx)
//		if err != nil {
//			return nil, err
//		}
//		return admin.Mount(app, app.server.AdminAddr(), app.server.MetricsHandler()), nil
//	})
//
// The listener is wired here rather than in the graph because App.Health
// and App.Ready only exist once New has built the App: nothing inside the
// graph could serve them.
func Mount(app App, addr string, metrics http.Handler) *Server {
	return &Server{app: app, http: New(addr, app, metrics)}
}

// Server is an App with the admin listener mounted beside it.
type Server struct {
	app  App
	http *http.Server
}

// Run binds the admin listener, serves it, and runs the App. A listener
// that cannot bind fails Run before the App starts, so a port clash ends
// the process with the error rather than leaving it running with no probe
// to answer — which a liveness probe would turn into a restart loop with
// nothing in the log to say why.
func (s *Server) Run(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.http.Addr)
	if err != nil {
		return fmt.Errorf("admin: listen: %w", err)
	}
	go func() {
		// Serve returns once Shutdown closes it; anything else means the
		// probes have stopped answering, and the log should say so.
		if err := s.http.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
			slog.Error("admin: listener stopped", "error", err)
		}
	}()
	return s.app.Run(ctx)
}

// Shutdown stops the App first and the admin listener last, so the probes
// and /metrics answer for the whole drain: a readiness probe during it is
// told the instance is going, not refused a connection, and the last
// scrape sees the last requests. The listener's own outcome joins the
// App's report as one more entry, "admin".
func (s *Server) Shutdown(ctx context.Context) servo.Report {
	report := s.app.Shutdown(ctx)
	result := servo.NodeResult{Name: "admin", Status: servo.StatusOK}
	if err := s.http.Shutdown(ctx); err != nil {
		result.Status, result.Err = servo.StatusFailed, err
		if errors.Is(err, context.DeadlineExceeded) {
			result.Status = servo.StatusAbandoned
		}
	}
	report.Nodes = append(report.Nodes, result)
	return report
}

// live answers the liveness probe. It deliberately consults nothing: see
// the package comment for what a dependency check here costs.
func live(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	// A failed write means the prober hung up; the status is already sent.
	_, _ = io.WriteString(w, "ok\n")
}

// response is a report as /readyz and /healthz serve it: the verdict a
// person reading it wants first, then every node in servo's own encoding —
// name, status by name, and the error when there is one.
type response struct {
	Clean bool               `json:"clean"`
	Nodes []servo.NodeResult `json:"nodes"`
}

func reportHandler(check func(context.Context) servo.Report) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		report := check(r.Context())
		w.Header().Set("Content-Type", "application/json")
		if !report.Clean() {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		json.NewEncoder(w).Encode(response{Clean: report.Clean(), Nodes: report.Nodes})
	}
}
