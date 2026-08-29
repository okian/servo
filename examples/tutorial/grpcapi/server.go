// Package grpcapi is the gRPC transport, serving the same service layer
// as api and ginapi — and serving gRPC and REST on one port.
//
// The single-port arrangement is the advanced part. gRPC is HTTP/2 with a
// content type of application/grpc, so a handler that inspects those two
// facts can route each request to either the gRPC server or an ordinary
// net/http mux, and both live behind one listener on one port. That means
// one firewall rule, one TLS certificate, and one address for a client to
// know, at the cost of a dispatch function that has to be exactly right.
package grpcapi

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"

	"google.golang.org/grpc"

	"example.com/servoorders/auth"
	"example.com/servoorders/config"
	"example.com/servoorders/grpcapi/ordersv1"
	"example.com/servoorders/observability"
	"example.com/servoorders/openapi"
	"example.com/servoorders/resilience"
	"example.com/servoorders/service"
	"example.com/servoorders/session"
)

type Server struct {
	ordersv1.UnimplementedOrdersServer

	http    *http.Server
	grpc    *grpc.Server
	orders  *service.OrderService
	auth    *service.AuthService
	metrics *observability.Metrics
	// sessions is the scope accessor, not a session — see
	// docs/tutorial/14-scoped-instances.md.
	sessions session.Sessions
}

type Config struct {
	HTTPAddr  string `env:"HTTP_ADDR" envDefault:":8080"`
	AdminAddr string `env:"ADMIN_ADDR" envDefault:":8081"`
}

func NewConfig(src config.Source) (*Config, error) {
	return config.Parse[Config](src, "")
}

func New(
	cfg *Config,
	orders *service.OrderService,
	authSvc *service.AuthService,
	issuer *auth.Issuer,
	metrics *observability.Metrics,
	tracer *observability.Tracer,
	limiter *resilience.RateLimiter,
	sessions session.Sessions,
	log *observability.Logger,
) *Server {
	s := &Server{orders: orders, auth: authSvc, metrics: metrics, sessions: sessions}

	s.grpc = grpc.NewServer(grpc.UnaryInterceptor(authInterceptor(issuer)))
	ordersv1.RegisterOrdersServer(s.grpc, s)

	// The REST side of the same port: the contract and its UI. Health,
	// readiness and metrics are deliberately absent — they belong to the
	// admin listener, and that separation is the whole reason it exists.
	mux := http.NewServeMux()
	mux.Handle("/", openapi.Handler())

	// Only the REST half is wrapped. gRPC carries its own equivalents —
	// the interceptor above, and stats handlers for metrics and tracing —
	// and sending gRPC frames through HTTP middleware that reads paths
	// and statuses would produce numbers that look right and mean
	// nothing.
	//
	// The layering is also load-bearing, not just tidy. These middlewares
	// wrap the ResponseWriter to capture the status code, and gRPC needs
	// the original one — it writes trailers and flushes frames through
	// interfaces a wrapper does not carry. Putting any of them outside
	// the dispatch breaks every RPC with a protocol error and no
	// server-side log line, because the request never reaches a handler
	// that logs.
	var rest http.Handler = mux
	rest = metrics.Middleware(rest)
	rest = tracer.Middleware(rest)
	rest = limiter.Middleware(rest)

	// Unencrypted HTTP/2 is what makes one port work without TLS: gRPC is
	// HTTP/2, and a plaintext listener speaks HTTP/1.1 unless told
	// otherwise. Protocols says otherwise. Behind a TLS-terminating proxy
	// or with real certificates, ALPN negotiates h2 and none of this is
	// needed — but a tutorial that required certificates to run would be
	// a worse tutorial.
	//
	// This replaces golang.org/x/net/http2/h2c, which did the same job by
	// hijacking the connection and is deprecated now that the standard
	// library supports it directly.
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)

	s.http = &http.Server{
		Addr:      cfg.HTTPAddr,
		Handler:   dispatch(s.grpc, rest),
		Protocols: protocols,
	}
	return s
}

// dispatch sends a request to the gRPC server or the REST mux.
//
// Both conditions are load-bearing. ProtoMajor == 2 alone would capture
// any HTTP/2 request, including a browser's; the content type alone would
// be trivially spoofable over HTTP/1.1 and would hand a gRPC server a
// connection it cannot speak on. Requiring both is the check the gRPC
// project itself documents, and getting it wrong fails in a way that is
// miserable to debug: the client sees a protocol error with no server-side
// log line, because the request never reached a handler that logs.
func dispatch(grpcServer *grpc.Server, rest http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor == 2 && strings.HasPrefix(r.Header.Get("Content-Type"), "application/grpc") {
			grpcServer.ServeHTTP(w, r)
			return
		}
		rest.ServeHTTP(w, r)
	})
}

// Handler exposes the dispatching handler for httptest, so the routing
// decision above can be tested without binding a port.
func (s *Server) Handler() http.Handler {
	return s.http.Handler
}

// Serve runs on ln with this server's own configuration, rather than
// leaving a caller to rebuild an http.Server around Handler().
//
// That distinction is the point: the protocol settings that make one port
// carry both protocols live on the http.Server, not on the handler, so a
// test that wrapped Handler() in a fresh http.Server would be testing a
// plain HTTP/1.1 listener and would report a protocol error it could not
// explain.
func (s *Server) Serve(ln net.Listener) error {
	return s.http.Serve(ln)
}

func (s *Server) MetricsHandler() http.Handler {
	return s.metrics.Handler()
}

// Run serves both protocols from the one listener. Only the http.Server
// is started: the gRPC server never binds anything itself here, it is
// reached through ServeHTTP. That is why Stop below shuts down the HTTP
// server rather than calling grpc.Server.GracefulStop — there is no
// separate listener for it to close.
func (s *Server) Run(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		if err := s.http.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("grpcapi: %w", err)
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		return nil
	case err := <-errCh:
		return err
	}
}

func (s *Server) Stop(ctx context.Context) error {
	// Stop accepting new streams first, then drain HTTP. GracefulStop
	// would block until every in-flight RPC finished, which is the
	// http.Server.Shutdown call's job here — and doing both would mean
	// waiting twice for the same requests.
	s.grpc.Stop()
	return s.http.Shutdown(ctx)
}
