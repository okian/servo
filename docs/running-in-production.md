# Running in production

**Who this is for:** anyone deploying a servo-built service into containers. This page is a
checklist of the few settings worth getting right, short on purpose: each item points at the
reference that owns the detail.

## `main` and the container

`func main() { servo.Main(New) }` is the whole of `main`. It turns SIGINT, SIGTERM and SIGHUP into
a graceful shutdown under a deadline of its own, logs failures at ERROR level, and sets the exit
status a supervisor's restart policy reads: 1 when `New` fails, `Run` fails or the shutdown report
is not clean, and 128 plus the signal number (130, 143) when a second signal cuts the shutdown short.
[Your main](reference/lifecycle.md#your-main) documents each of these.

In a container, start the binary in the Dockerfile's exec form, `ENTRYPOINT ["/app"]`, so it is
process 1 and receives the signals itself. The shell form starts a shell that does not pass SIGTERM
on, and the orchestrator's grace period then runs out on a process that never heard it.
[In a container](reference/lifecycle.md#in-a-container) has the rest.

## CPU and memory limits

**`GOMAXPROCS` needs nothing.** Since Go 1.25, a module whose `go` line is 1.25 or later — yours is,
since servo's is — sizes `GOMAXPROCS` from the container's CPU limit and follows it if it changes;
the `automaxprocs` package is no longer needed. It reads the *limit*, though, not the request: a pod
with a CPU request and no CPU limit gets a `GOMAXPROCS` equal to the core count of whatever node it
lands on. Set a limit, or `GOMAXPROCS` itself.

**`GOMEMLIMIT` is yours to set.** No Go release reads the container's memory limit. Without
`GOMEMLIMIT`, the garbage collector paces itself by `GOGC` alone and knows nothing of the limit, so
a heap that grows towards it meets the kernel's OOM killer rather than a collector working harder.
Set it a little below the container's limit, leaving room for what the Go runtime does not count:

```yaml
resources:
  limits:
    cpu: "2"
    memory: 512Mi
env:
  - name: GOMEMLIMIT
    value: 460MiB
```

It is a soft limit — a heap that really needs more than it still gets it, at the cost of more
frequent collections — so it tunes the collector; it does not replace sizing the container.

## Probes

Liveness asks whether the process is broken and gets it restarted; readiness asks whether to send
it traffic. Keep dependency checks out of liveness. `App.Health` collects each component's own check
— the database answered, the cache answered — and a liveness probe wired to it restarts every
replica at once when that dependency fails over, each then failing to start until it is back.
[Probes](reference/lifecycle.md#probes) has the guidance and the Kubernetes settings, and the
tutorial's admin listener ([chapter 10](tutorial/10-api-layer.md#health-and-readiness-live-outside-the-graph))
is a worked example: `/livez` for liveness, `/readyz` for readiness, `/healthz` for people.

## Shutdown and the grace period

Three durations have to fit inside one another: how long a generated server keeps serving after its
readiness drops (`ShutdownDelay`, so load balancers stop routing to it before its listener closes),
`servo.Main`'s shutdown deadline, and the orchestrator's grace period before it sends SIGKILL.
[Sizing the grace period](reference/lifecycle.md#sizing-the-grace-period) gives the sequence and
the arithmetic, and the [HTTP reference](reference/http.md#the-config) documents `ShutdownDelay` and
the drain that follows it.

## HTTP server timeouts

A generated server takes its timeouts from `servo.HTTPConfig` and its per-group listeners, and zero
means no timeout, as it does for `net/http.Server`. A listener with no `ReadHeaderTimeout` logs one
warning when it starts serving: without it, a client that opens connections and never finishes
sending headers can hold every one of them for as long as it likes. Set `ReadHeaderTimeout`, and
`ReadTimeout`, `WriteTimeout` and `IdleTimeout` to what your routes need — a long `WriteTimeout`, or
none, on a listener that streams. [The config](reference/http.md#the-config) lists every field.

## Profiling

A generated server serves `//servo:` routes and nothing else; it cannot mount a raw `http.Handler`,
such as `net/http/pprof`'s, beside them. Give the profiler a listener of its own, as a component of
the graph, so it starts and stops with everything else:

```go
// debug/debug.go
package debug

import (
	"context"
	"net"
	"net/http"
	"net/http/pprof"
	"time"
)

//servo:config prefix=DEBUG
type config struct {
	addr string `config:"addr,default=localhost:6060"`
}

type Server struct{ srv *http.Server }

func New(cfg config) *Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	return &Server{srv: &http.Server{Addr: cfg.addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}}
}

func (s *Server) Run(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.srv.Addr)
	if err != nil {
		return err
	}
	go s.srv.Serve(ln)
	<-ctx.Done()
	return nil
}

func (s *Server) Stop(ctx context.Context) error { return s.srv.Shutdown(ctx) }
```

and `servo.Root[*debug.Server]()` in the spec. Keep the address off public interfaces: a profile
shows where the program spends its time and memory, and `/debug/pprof/cmdline` prints its command
line. `localhost` plus `kubectl port-forward` reaches it when you need it. A hand-written admin
listener, like the tutorial's, can mount the same handlers next to its probes instead.
