package admin_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/okian/servo/v3/servo"

	"example.com/servoorders/internal/transport/admin"
)

// fakeApp is a generated App as package admin sees it, with every answer
// scripted.
type fakeApp struct {
	health, ready servo.Report
	run           func(context.Context) error
	shutdown      func(context.Context) servo.Report
}

func (a *fakeApp) Health(context.Context) servo.Report { return a.health }
func (a *fakeApp) Ready(context.Context) servo.Report  { return a.ready }

func (a *fakeApp) Run(ctx context.Context) error {
	if a.run == nil {
		<-ctx.Done()
		return ctx.Err()
	}
	return a.run(ctx)
}

func (a *fakeApp) Shutdown(ctx context.Context) servo.Report {
	if a.shutdown == nil {
		return servo.Report{}
	}
	return a.shutdown(ctx)
}

var (
	allOK     = servo.Report{Nodes: []servo.NodeResult{{Name: "*example.com/servoorders/internal/repository/postgres.Store", Status: servo.StatusOK}}}
	cacheDown = servo.Report{Nodes: []servo.NodeResult{
		{Name: "*example.com/servoorders/internal/repository/postgres.Store", Status: servo.StatusOK},
		{Name: "*example.com/servoorders/internal/cache/redis.Cache", Status: servo.StatusFailed, Err: errors.New("dial tcp [::1]:6379: connect: connection refused")},
	}}
	notListening = servo.Report{Nodes: []servo.NodeResult{{Name: "*example.com/servoorders/internal/transport/api.Server", Status: servo.StatusFailed, Err: errors.New("api: not accepting connections yet")}}}
)

func get(t *testing.T, h http.Handler, path string) (int, string) {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w.Code, w.Body.String()
}

// TestLivenessChecksNoDependency is the fix for a fleet-wide restart: a
// Redis outage, or a database failover, must not fail the liveness probe,
// since a restart cannot fix either and Init fails fast until they are
// back. /livez answers 200 whatever Health and Ready say.
func TestLivenessChecksNoDependency(t *testing.T) {
	srv := admin.New(":0", &fakeApp{health: cacheDown, ready: notListening}, http.NotFoundHandler())
	code, body := get(t, srv.Handler, "/livez")
	if code != http.StatusOK || body != "ok\n" {
		t.Fatalf("GET /livez = %d %q, want 200 \"ok\\n\" while every dependency is down", code, body)
	}
}

func TestReadinessIsReady(t *testing.T) {
	for _, tc := range []struct {
		name     string
		ready    servo.Report
		wantCode int
	}{
		{"listening", allOK, http.StatusOK},
		{"not listening yet", notListening, http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Health failing must not make the instance unready: the cache
			// is one this service degrades around.
			srv := admin.New(":0", &fakeApp{health: cacheDown, ready: tc.ready}, http.NotFoundHandler())
			if code, body := get(t, srv.Handler, "/readyz"); code != tc.wantCode {
				t.Fatalf("GET /readyz = %d %s, want %d", code, body, tc.wantCode)
			}
		})
	}
}

// TestHealthReportIsServosEncoding: /healthz is App.Health in servo's own
// JSON, statuses by name and errors by message, under the verdict.
func TestHealthReportIsServosEncoding(t *testing.T) {
	srv := admin.New(":0", &fakeApp{health: cacheDown, ready: allOK}, http.NotFoundHandler())
	code, body := get(t, srv.Handler, "/healthz")
	if code != http.StatusServiceUnavailable {
		t.Fatalf("GET /healthz = %d, want 503 with the cache down", code)
	}
	want := `{"clean":false,"nodes":[{"name":"*example.com/servoorders/internal/repository/postgres.Store","status":"ok"},{"name":"*example.com/servoorders/internal/cache/redis.Cache","status":"failed","err":"dial tcp [::1]:6379: connect: connection refused"}]}` + "\n"
	if body != want {
		t.Fatalf("GET /healthz body =\n%s\nwant\n%s", body, want)
	}
	var decoded struct {
		Clean bool
		Nodes []servo.NodeResult
	}
	if err := json.Unmarshal([]byte(body), &decoded); err != nil || decoded.Nodes[1].Status != servo.StatusFailed {
		t.Fatalf("the body does not decode back into servo's types: %v, %+v", err, decoded)
	}
}

// freeAddr is a loopback address nothing is listening on, as of now.
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

// TestMountFailsRunWhenTheListenerCannotBind: a port clash used to be a
// log line at INFO — invisible behind a WARN-level logger — and a process
// left running with no probes to answer.
func TestMountFailsRunWhenTheListenerCannotBind(t *testing.T) {
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer taken.Close()

	ran := false
	srv := admin.Mount(&fakeApp{run: func(context.Context) error { ran = true; return nil }}, taken.Addr().String(), http.NotFoundHandler())
	err = srv.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "admin: listen") {
		t.Fatalf("Run = %v, want the listen error", err)
	}
	if ran {
		t.Fatal("the App ran although the admin listener never bound")
	}
	if r := srv.Shutdown(context.Background()); !r.Clean() {
		t.Fatalf("Shutdown after a failed Run = %v, want clean", r)
	}
}

// TestMountServesTheProbesThroughTheDrain runs the mounted App the way
// servo.Main does, and checks the order Shutdown stops things in: the App
// first, while /readyz still answers, then the listener.
func TestMountServesTheProbesThroughTheDrain(t *testing.T) {
	addr := freeAddr(t)
	url := "http://" + addr

	var duringDrain int
	app := &fakeApp{health: allOK, ready: allOK}
	app.shutdown = func(context.Context) servo.Report {
		resp, err := http.Get(url + "/readyz")
		if err != nil {
			t.Errorf("GET /readyz during the App's Shutdown: %v — the listener closed first", err)
			return servo.Report{}
		}
		resp.Body.Close()
		duringDrain = resp.StatusCode
		return servo.Report{Nodes: []servo.NodeResult{{Name: "*api.Server", Status: servo.StatusOK}}}
	}
	srv := admin.Mount(app, addr, http.NotFoundHandler())

	ctx, cancel := context.WithCancel(context.Background())
	ran := make(chan error, 1)
	go func() { ran <- srv.Run(ctx) }()

	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, err := http.Get(url + "/livez")
		if err == nil {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK || string(body) != "ok\n" {
				t.Fatalf("GET /livez = %d %q, want 200 ok", resp.StatusCode, body)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the admin listener never came up: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	if err := <-ran; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v, want the App's own cancellation", err)
	}
	report := srv.Shutdown(context.Background())
	if duringDrain != http.StatusOK {
		t.Fatalf("GET /readyz during the App's Shutdown = %d, want an answer", duringDrain)
	}
	if !report.Clean() || len(report.Nodes) != 2 || report.Nodes[1].Name != "admin" {
		t.Fatalf("Shutdown = %+v, want the App's node then a clean \"admin\"", report.Nodes)
	}
	if _, err := http.Get(url + "/livez"); err == nil {
		t.Fatal("the admin listener still answers after Shutdown")
	}
}
