package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"example.com/servoorders/api"
	"example.com/servoorders/config"
	"example.com/servoorders/observability"
	"github.com/okian/servo/v3/servo"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Logging is configured before anything else has a chance to log, and
	// the admin listener's address is read here rather than taken from the
	// graph — see newAdminServer's comment for why that one piece of
	// wiring cannot go through servo. Both parse from the same Source the
	// graph will use; it is an in-memory map, so reading it twice is free.
	src := config.NewEnv()

	obsCfg, err := observability.NewConfig(src)
	if err != nil {
		log.Fatal(err)
	}
	observability.ConfigureLogging(obsCfg)

	apiCfg, err := api.NewConfig(src)
	if err != nil {
		log.Fatal(err)
	}

	app, err := New(ctx)
	if err != nil {
		log.Fatal(err)
	}

	admin := newAdminServer(apiCfg.AdminAddr, app, app.server.MetricsHandler())
	go func() {
		if err := admin.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Print(err)
		}
	}()

	if err := app.Run(ctx); err != nil {
		log.Print(err)
	}

	admin.Shutdown(context.Background())
	if r := app.Shutdown(context.Background()); !r.Clean() {
		log.Print(r)
	}
}

// newAdminServer exists because app.Health/app.Ready are only reachable
// once App is fully constructed — no component inside the graph can call
// back into the aggregate view of everything else, since that view doesn't
// exist yet at the time any single component is built. So this one small
// piece of wiring happens here instead of through servo, on a separate
// port from api.Server's own — see docs/tutorial/10-api-layer.md.
func newAdminServer(addr string, app interface {
	Health(context.Context) servo.Report
	Ready(context.Context) servo.Report
}, metricsHandler http.Handler,
) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", reportHandler(app.Health))
	mux.HandleFunc("GET /readyz", reportHandler(app.Ready))
	mux.Handle("GET /metrics", metricsHandler)
	return &http.Server{Addr: addr, Handler: mux}
}

// healthResponse re-renders a servo.Report with a readable status string
// instead of NodeStatus's raw int value — NodeStatus.String() exists, but
// encoding/json only calls MarshalJSON/MarshalText, neither of which
// NodeStatus implements, so encoding a servo.Report directly would print
// "Status":0 instead of "Status":"ok".
type healthResponse struct {
	Clean bool         `json:"clean"`
	Nodes []healthNode `json:"nodes"`
}

type healthNode struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

func reportHandler(check func(context.Context) servo.Report) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		report := check(r.Context())

		resp := healthResponse{Clean: report.Clean()}
		for _, n := range report.Nodes {
			node := healthNode{Name: n.Name, Status: n.Status.String()}
			if n.Err != nil {
				node.Error = n.Err.Error()
			}
			resp.Nodes = append(resp.Nodes, node)
		}

		w.Header().Set("Content-Type", "application/json")
		if !report.Clean() {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		json.NewEncoder(w).Encode(resp)
	}
}
