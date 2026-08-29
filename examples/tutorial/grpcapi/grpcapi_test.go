package grpcapi_test

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"
	"uuid"

	"github.com/okian/servo/v3/servo"
	"go.uber.org/mock/gomock"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"example.com/servoorders/auth"
	"example.com/servoorders/cache"
	"example.com/servoorders/domain"
	"example.com/servoorders/grpcapi"
	"example.com/servoorders/grpcapi/ordersv1"
	"example.com/servoorders/mocks"
	"example.com/servoorders/observability"
	"example.com/servoorders/resilience"
	"example.com/servoorders/service"
	"example.com/servoorders/session"
)

// TestOneListenerServesBothProtocols is the claim the single-port design
// makes, tested against a real listener rather than a handler: a gRPC
// client and an HTTP client both talk to the same address, and each gets
// its own protocol back.
func TestOneListenerServesBothProtocols(t *testing.T) {
	addr := serve(t, newTestServer(t))

	t.Run("http reaches the REST mux", func(t *testing.T) {
		resp, err := http.Get("http://" + addr + "/openapi.yaml")
		if err != nil {
			t.Fatalf("GET /openapi.yaml: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("status = %d, want 200", resp.StatusCode)
		}
	})

	t.Run("grpc reaches the service", func(t *testing.T) {
		conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		defer conn.Close()

		// Login is the unauthenticated method, so this exercises the
		// dispatch and the interceptor's exemption in one call. The
		// expected answer is Unauthenticated rather than NotFound:
		// AuthService deliberately reports a missing user and a wrong
		// password identically, so the API never reveals which usernames
		// exist. Reaching that answer at all proves the RPC arrived.
		_, err = ordersv1.NewOrdersClient(conn).Login(t.Context(),
			&ordersv1.LoginRequest{Username: "nobody", Password: "x"})
		if status.Code(err) != codes.Unauthenticated {
			t.Fatalf("Login err = %v (code %s), want Unauthenticated — the call did not reach the handler",
				err, status.Code(err))
		}
	})
}

// A gRPC call with no token must be refused before it reaches a handler.
func TestUnauthenticatedRPCIsRejected(t *testing.T) {
	conn, err := grpc.NewClient(serve(t, newTestServer(t)),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	client := ordersv1.NewOrdersClient(conn)

	if _, err := client.ListOrders(t.Context(), &ordersv1.ListOrdersRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Errorf("ListOrders with no token = %s, want Unauthenticated", status.Code(err))
	}

	ctx := metadata.AppendToOutgoingContext(t.Context(), "authorization", "Bearer not-a-token")
	if _, err := client.Recent(ctx, &ordersv1.RecentRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Errorf("Recent with a bad token = %s, want Unauthenticated", status.Code(err))
	}
}

// The admin endpoints must not be reachable on the public port, in this
// variant as much as the others.
func TestAdminEndpointsAreNotOnThePublicListener(t *testing.T) {
	addr := serve(t, newTestServer(t))
	for _, path := range []string{"/healthz", "/readyz", "/metrics"} {
		resp, err := http.Get("http://" + addr + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s = %d on the public listener, want 404", path, resp.StatusCode)
		}
	}
}

// serve runs the server on a loopback port, through its own Serve method.
//
// Not httptest: its EnableHTTP2 turns on TLS, and the point here is
// HTTP/2 without it. Not a hand-built http.Server around Handler()
// either — the settings that let one port carry both protocols live on
// the server, so that would test an HTTP/1.1 listener and report a
// protocol error with no obvious cause.
func serve(t *testing.T, s *grpcapi.Server) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = s.Serve(ln) }()
	t.Cleanup(func() { _ = s.Stop(context.Background()) })
	return ln.Addr().String()
}

func newTestServer(t *testing.T) *grpcapi.Server {
	t.Helper()
	ctrl := gomock.NewController(t)

	repo := mocks.NewMockOrderRepository(ctrl)
	users := mocks.NewMockUserRepository(ctrl)
	orderCache := mocks.NewMockOrderCache(ctrl)
	pub := mocks.NewMockEventPublisher(ctrl)

	orderCache.EXPECT().Get(gomock.Any(), gomock.Any()).Return(nil, cache.ErrMiss).AnyTimes()
	orderCache.EXPECT().Set(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	pub.EXPECT().PublishOrderPlaced(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	users.EXPECT().GetByUsername(gomock.Any(), gomock.Any()).Return(nil, domain.ErrNotFound).AnyTimes()

	issuer := auth.New(&auth.Config{Secret: "test-secret", Expiry: time.Hour})
	metrics := observability.NewMetrics()
	tracer, err := observability.NewTracer(&observability.Config{})
	if err != nil {
		t.Fatalf("NewTracer: %v", err)
	}

	return grpcapi.New(
		&grpcapi.Config{},
		service.New(repo, orderCache, pub, quietLogger()),
		service.NewAuthService(users, issuer),
		issuer,
		metrics,
		tracer,
		resilience.NewRateLimiter(&resilience.Config{RPS: 1000}, metrics),
		newFakeSessions(&session.Config{Recent: 10}),
		quietLogger(),
	)
}

func quietLogger() *observability.Logger {
	return &observability.Logger{Logger: slog.New(slog.DiscardHandler)}
}

type fakeSessions struct {
	cfg *session.Config
	mu  sync.Mutex
	by  map[session.UserID]*session.Session
}

func newFakeSessions(cfg *session.Config) *fakeSessions {
	return &fakeSessions{cfg: cfg, by: map[session.UserID]*session.Session{}}
}

func (f *fakeSessions) Acquire(ctx context.Context) (*session.Session, func(), error) {
	var zero *session.Session
	key, err := zero.ScopeKey(ctx)
	if err != nil {
		return nil, nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.by[key]
	if !ok {
		s = session.New(key, f.cfg, quietLogger())
		f.by[key] = s
	}
	return s, func() {}, nil
}

func (f *fakeSessions) Stats() servo.ScopeStats { return servo.ScopeStats{} }

var (
	_ = net.Listen
	_ = uuid.New
)
