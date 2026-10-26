package servoimpl

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/okian/servo/v3/servo"
)

// logRecord is one captured slog call, with the value the request context
// carried under markKey — which is how the tests see that a log call was
// made with the request's context rather than without one.
type logRecord struct {
	level slog.Level
	msg   string
	attrs map[string]string
	mark  any
}

type markKey struct{}

type capturedLogs struct {
	mu      sync.Mutex
	records []logRecord
}

func (c *capturedLogs) Enabled(context.Context, slog.Level) bool { return true }
func (c *capturedLogs) WithAttrs([]slog.Attr) slog.Handler       { return c }
func (c *capturedLogs) WithGroup(string) slog.Handler            { return c }
func (c *capturedLogs) Handle(ctx context.Context, r slog.Record) error {
	rec := logRecord{level: r.Level, msg: r.Message, attrs: map[string]string{}, mark: ctx.Value(markKey{})}
	r.Attrs(func(a slog.Attr) bool {
		rec.attrs[a.Key] = a.Value.String()
		return true
	})
	c.mu.Lock()
	defer c.mu.Unlock()
	c.records = append(c.records, rec)
	return nil
}

func (c *capturedLogs) all() []logRecord {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]logRecord(nil), c.records...)
}

// only returns the single record logged so far, failing when there is not
// exactly one.
func (c *capturedLogs) only(t *testing.T) logRecord {
	t.Helper()
	all := c.all()
	if len(all) != 1 {
		t.Fatalf("logged %d records, want 1: %+v", len(all), all)
	}
	return all[0]
}

// captureLogs points slog's default logger at a recorder for one test.
// slog.Default is process-wide, so no test using it may run in parallel.
func captureLogs(t *testing.T) *capturedLogs {
	t.Helper()
	c := &capturedLogs{}
	prev := slog.Default()
	slog.SetDefault(slog.New(c))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return c
}

// markedRequest is a request whose context carries a mark, so a log call
// made with it can be told from one made without it.
func markedRequest(method, target string) *http.Request {
	r := httptest.NewRequest(method, target, nil)
	return r.WithContext(context.WithValue(r.Context(), markKey{}, "req-1"))
}

func freeAddr(t *testing.T) (string, uint16) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("pick port: %v", err)
	}
	defer func() { _ = ln.Close() }()
	port := ln.Addr().(*net.TCPAddr).Port
	return "127.0.0.1", uint16(port)
}

// TestListenerCopiesEveryField: the default group's listener is made of
// HTTPConfig's flat fields, copied by name. A field added to HTTPConfig and
// forgotten here would be silently ignored by every default group.
func TestListenerCopiesEveryField(t *testing.T) {
	cfg := &servo.HTTPConfig{}
	v := reflect.ValueOf(cfg).Elem()
	for i := 0; i < v.NumField(); i++ {
		f := v.Field(i)
		switch f.Kind() {
		case reflect.String:
			f.SetString("v" + strconv.Itoa(i))
		case reflect.Int, reflect.Int64:
			f.SetInt(int64(i + 1))
		case reflect.Uint16:
			f.SetUint(uint64(i + 1))
		case reflect.Map:
			// Groups, the one field that is not the default group's.
		default:
			t.Fatalf("HTTPConfig.%s has kind %s, which this test does not know how to fill", v.Type().Field(i).Name, f.Kind())
		}
	}
	lc, err := Listener(cfg, "")
	if err != nil {
		t.Fatalf("Listener: %v", err)
	}
	lv := reflect.ValueOf(lc)
	for i := 0; i < v.NumField(); i++ {
		name := v.Type().Field(i).Name
		if name == "Groups" {
			continue
		}
		got := lv.FieldByName(name)
		if !got.IsValid() {
			t.Errorf("HTTPListener has no %s field, so a group cannot set what the default group can", name)
			continue
		}
		if !reflect.DeepEqual(got.Interface(), v.Field(i).Interface()) {
			t.Errorf("Listener copied %s as %v, want %v", name, got.Interface(), v.Field(i).Interface())
		}
	}
	if lv.NumField() != v.NumField()-1 {
		t.Errorf("HTTPListener has %d fields, HTTPConfig %d besides Groups", lv.NumField(), v.NumField()-1)
	}
}

func TestListenerGroups(t *testing.T) {
	cfg := &servo.HTTPConfig{Groups: map[string]servo.HTTPListener{"telemetry": {Port: 9001}}}
	lc, err := Listener(cfg, "telemetry")
	if err != nil || lc.Port != 9001 {
		t.Fatalf("Listener(telemetry) = %+v, %v", lc, err)
	}
	// The exact text docs/reference/diagnostics.md quotes.
	_, err = Listener(cfg, "internal")
	if err == nil || err.Error() != `http: group "internal" declared in the spec but missing from HTTPConfig.Groups` {
		t.Fatalf("missing group: %v", err)
	}
	if _, err := Listener(nil, ""); err == nil || !strings.Contains(err.Error(), "returned nil") {
		t.Fatalf("a nil config must fail New, not panic it: %v", err)
	}
}

func TestBodyLimit(t *testing.T) {
	if got := BodyLimit(servo.HTTPListener{}); got != DefaultMaxBodyBytes {
		t.Fatalf("zero = %d, want the 1 MiB default", got)
	}
	if got := BodyLimit(servo.HTTPListener{MaxBodyBytes: -1}); got != DefaultMaxBodyBytes {
		t.Fatalf("negative = %d, want the default", got)
	}
	if got := BodyLimit(servo.HTTPListener{MaxBodyBytes: 512}); got != 512 {
		t.Fatalf("512 = %d", got)
	}
}

func TestNewServerCarriesTheListener(t *testing.T) {
	h := http.NotFoundHandler()
	srv, cancel, err := NewServer("http", servo.HTTPListener{
		IP: "::1", Port: 8443,
		ReadHeaderTimeout: time.Second, ReadTimeout: 2 * time.Second,
		WriteTimeout: 3 * time.Second, IdleTimeout: 4 * time.Second,
		MaxHeaderBytes: 1 << 12,
	}, h)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	if srv.Addr != "[::1]:8443" {
		t.Errorf("Addr = %q", srv.Addr)
	}
	if srv.ReadHeaderTimeout != time.Second || srv.ReadTimeout != 2*time.Second || srv.WriteTimeout != 3*time.Second || srv.IdleTimeout != 4*time.Second || srv.MaxHeaderBytes != 1<<12 {
		t.Errorf("limits not carried: %+v", srv)
	}
	if srv.TLSConfig != nil {
		t.Errorf("no files, yet TLS is configured")
	}
	// Request contexts descend from the server's own base, which only
	// cancel ends — not the context Run is given.
	base := srv.BaseContext(nil)
	if base.Err() != nil {
		t.Fatalf("base context born done")
	}
	cancel(errDrainExpired)
	if !errors.Is(context.Cause(base), errDrainExpired) {
		t.Fatalf("cancel did not reach the base context: %v", context.Cause(base))
	}
}

// A missing or broken key pair fails New — the error naming the server and
// both files — instead of Run, after every Init.
func TestNewServerLoadsTheKeyPair(t *testing.T) {
	dir := t.TempDir()
	_, _, err := NewServer("http:internal", servo.HTTPListener{CertFile: filepath.Join(dir, "cert.pem"), KeyFile: filepath.Join(dir, "key.pem")}, http.NotFoundHandler())
	if err == nil {
		t.Fatal("NewServer accepted files that do not exist")
	}
	for _, want := range []string{"http:internal", "cert.pem", "key.pem", "no such file"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}

	certA, keyA := writeKeyPair(t, filepath.Join(dir, "a"), 1)
	_, keyB := writeKeyPair(t, filepath.Join(dir, "b"), 2)
	if _, _, err := NewServer("http", servo.HTTPListener{CertFile: certA, KeyFile: keyB}, http.NotFoundHandler()); err == nil || !strings.Contains(err.Error(), "private key does not match") {
		t.Fatalf("a mismatched pair must fail New: %v", err)
	}
	srv, _, err := NewServer("http", servo.HTTPListener{CertFile: certA, KeyFile: keyA}, http.NotFoundHandler())
	if err != nil || srv.TLSConfig == nil {
		t.Fatalf("a good pair: %v (TLS %v)", err, srv)
	}
}

// Half a key pair means plain HTTP, as it always has — but said out loud,
// since a missing KeyFile variable is how a server ends up serving
// plaintext by accident.
func TestNewServerWarnsAboutHalfAKeyPair(t *testing.T) {
	logs := captureLogs(t)
	srv, _, err := NewServer("http", servo.HTTPListener{CertFile: "cert.pem"}, http.NotFoundHandler())
	if err != nil || srv.TLSConfig != nil {
		t.Fatalf("half a pair: err=%v tls=%v, want plain HTTP", err, srv.TLSConfig)
	}
	rec := logs.only(t)
	if rec.level != slog.LevelWarn || !strings.Contains(rec.msg, "plain HTTP") || rec.attrs["cert_file"] != "cert.pem" {
		t.Fatalf("logged %+v", rec)
	}
}

// startServer builds and runs one server on a free port until the test
// ends, and returns it, its readiness flag, its cancel and its base URL.
func startServer(t *testing.T, lc servo.HTTPListener, h http.Handler) (*http.Server, *atomic.Bool, context.CancelCauseFunc, string) {
	t.Helper()
	lc.IP, lc.Port = freeAddr(t)
	srv, cancel, err := NewServer("http", lc, h)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	ready := &atomic.Bool{}
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, "http", srv, ready) }()
	t.Cleanup(func() {
		stop()
		if err := <-done; err != nil {
			t.Errorf("Serve: %v", err)
		}
		_ = srv.Close()
	})
	deadline := time.Now().Add(5 * time.Second)
	for !ready.Load() {
		if time.Now().After(deadline) {
			t.Fatal("server never became ready")
		}
		time.Sleep(time.Millisecond)
	}
	// From the listener, not srv.TLSConfig: net/http fills that in for a
	// plain server too once it is serving.
	scheme := "http"
	if lc.CertFile != "" && lc.KeyFile != "" {
		scheme = "https"
	}
	return srv, ready, cancel, fmt.Sprintf("%s://%s", scheme, srv.Addr)
}

func get(t *testing.T, url string) (int, string, error) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body), err
}

func TestServe(t *testing.T) {
	logs := captureLogs(t)
	_, ready, _, base := startServer(t, servo.HTTPListener{ReadHeaderTimeout: time.Second}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "pong")
	}))
	if code, body, err := get(t, base+"/"); err != nil || code != 200 || body != "pong" {
		t.Fatalf("GET = %d %q %v", code, body, err)
	}
	if !ready.Load() {
		t.Fatal("ready dropped while serving")
	}
	if all := logs.all(); len(all) != 0 {
		t.Fatalf("a server with a header timeout logged %+v", all)
	}
}

// The bind error is Run's error, as net.Listen words it; a server that
// never bound is never ready.
func TestServeReturnsTheBindError(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	srv := &http.Server{Addr: ln.Addr().String(), ReadHeaderTimeout: time.Second}
	var ready atomic.Bool
	err = Serve(context.Background(), "http", srv, &ready)
	if err == nil || !strings.Contains(err.Error(), "listen tcp "+ln.Addr().String()) || !strings.Contains(err.Error(), "address already in use") {
		t.Fatalf("Serve = %v, want net.Listen's bind error", err)
	}
	if ready.Load() {
		t.Fatal("ready after a failed bind")
	}
}

// One warning when nothing bounds the header read — and none when
// ReadTimeout does it instead, which net/http falls back to.
func TestServeWarnsWithoutAHeaderTimeout(t *testing.T) {
	cases := []struct {
		name string
		lc   servo.HTTPListener
		warn bool
	}{
		{"none", servo.HTTPListener{}, true},
		{"negative", servo.HTTPListener{ReadHeaderTimeout: -1, ReadTimeout: time.Second}, true},
		{"read timeout covers it", servo.HTTPListener{ReadTimeout: time.Second}, false},
		{"header timeout", servo.HTTPListener{ReadHeaderTimeout: time.Second}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			logs := captureLogs(t)
			startServer(t, c.lc, http.NotFoundHandler())
			all := logs.all()
			if !c.warn {
				if len(all) != 0 {
					t.Fatalf("logged %+v", all)
				}
				return
			}
			rec := logs.only(t)
			if rec.level != slog.LevelWarn || !strings.Contains(rec.msg, "no header timeout") || rec.attrs["server"] != "http" || rec.attrs["addr"] == "" {
				t.Fatalf("logged %+v", rec)
			}
		})
	}
}

// A server whose serving fails stops being ready and returns the error.
func TestServeFailureDropsReadiness(t *testing.T) {
	addr, port := freeAddr(t)
	srv, _, err := NewServer("http", servo.HTTPListener{IP: addr, Port: port, ReadHeaderTimeout: time.Second}, http.NotFoundHandler())
	if err != nil {
		t.Fatal(err)
	}
	var ready atomic.Bool
	done := make(chan error, 1)
	go func() { done <- Serve(context.Background(), "http", srv, &ready) }()
	for !ready.Load() {
		time.Sleep(time.Millisecond)
	}
	// Close ends Serve with ErrServerClosed, which Serve reports as a
	// clean return — the stop path's doing — but readiness must still
	// drop the moment the listener is gone.
	_ = srv.Close()
	if err := <-done; err != nil {
		t.Fatalf("Serve after Close = %v, want nil", err)
	}
	if ready.Load() {
		t.Fatal("still ready with the listener closed")
	}
}

// The delay keeps the listener serving after readiness drops, so traffic
// still routed here during it is answered rather than refused.
func TestStopServerServesThroughTheDelay(t *testing.T) {
	srv, ready, cancel, base := startServer(t, servo.HTTPListener{ReadHeaderTimeout: time.Second}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	const delay = 300 * time.Millisecond
	ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()

	result := make(chan servo.NodeResult, 1)
	began := time.Now()
	go func() { result <- StopServer(ctx, "http", srv, ready, delay, cancel) }()

	time.Sleep(delay / 3)
	if ready.Load() {
		t.Fatal("readiness must drop before the delay, not after it")
	}
	if code, body, err := get(t, base+"/"); err != nil || code != 200 || body != "ok" {
		t.Fatalf("request during the delay = %d %q %v, want it served", code, body, err)
	}
	r := <-result
	if r.Status != servo.StatusOK || r.Name != "http" {
		t.Fatalf("result = %+v", r)
	}
	if took := time.Since(began); took < delay {
		t.Fatalf("stopped after %v, before its %v delay", took, delay)
	}
	if _, _, err := get(t, base+"/"); err == nil {
		t.Fatal("listener still open after the stop")
	}
}

// A server that never bound was never in rotation: nothing to wait for.
func TestStopServerSkipsTheDelayWhenNeverReady(t *testing.T) {
	srv, cancel, err := NewServer("http", servo.HTTPListener{}, http.NotFoundHandler())
	if err != nil {
		t.Fatal(err)
	}
	var ready atomic.Bool
	began := time.Now()
	r := StopServer(context.Background(), "http", srv, &ready, time.Minute, cancel)
	if r.Status != servo.StatusOK || time.Since(began) > time.Second {
		t.Fatalf("result = %+v after %v, want an immediate clean stop", r, time.Since(began))
	}
}

// The delay is cut short by the deadline, and says why the drain that
// follows has no time left.
func TestStopServerDelayIsBoundedByTheDeadline(t *testing.T) {
	logs := captureLogs(t)
	srv, ready, cancel, _ := startServer(t, servo.HTTPListener{ReadHeaderTimeout: time.Second}, http.NotFoundHandler())
	ctx, stop := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer stop()
	began := time.Now()
	r := StopServer(ctx, "http", srv, ready, time.Minute, cancel)
	if took := time.Since(began); took > 2*time.Second {
		t.Fatalf("waited %v despite a 200ms deadline", took)
	}
	// Nothing was in flight, so the drain at the deadline still finds
	// every connection idle.
	if r.Status != servo.StatusOK {
		t.Fatalf("result = %+v", r)
	}
	var warned bool
	for _, rec := range logs.all() {
		warned = warned || (rec.level == slog.LevelWarn && strings.Contains(rec.msg, "ShutdownDelay"))
	}
	if !warned {
		t.Fatalf("no warning about a delay as long as the deadline: %+v", logs.all())
	}
}

// The drain gets what is left of the caller's deadline, not a fixed
// DefaultStopBudget: a request slower than the budget but inside the
// deadline finishes.
func TestStopServerDrainsForTheWholeDeadline(t *testing.T) {
	prev := servo.DefaultStopBudget
	servo.DefaultStopBudget = 50 * time.Millisecond
	t.Cleanup(func() { servo.DefaultStopBudget = prev })

	started := make(chan struct{})
	srv, ready, cancel, base := startServer(t, servo.HTTPListener{ReadHeaderTimeout: time.Second}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		time.Sleep(300 * time.Millisecond)
		_, _ = io.WriteString(w, "done")
	}))
	reply := make(chan string, 1)
	go func() {
		_, body, err := get(t, base+"/")
		reply <- fmt.Sprint(body, err)
	}()
	<-started

	ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	if r := StopServer(ctx, "http", srv, ready, 0, cancel); r.Status != servo.StatusOK {
		t.Fatalf("result = %+v, want the slow request drained", r)
	}
	if got := <-reply; got != "done<nil>" {
		t.Fatalf("in-flight request got %q", got)
	}
}

// With no deadline on ctx the drain falls back to DefaultStopBudget; when
// it runs out, every request context is cancelled — with a cause the error
// path recognises — and every connection closed, so a handler that honours
// its context stops instead of calling dependencies Shutdown is about to
// stop.
func TestStopServerCancelsRequestsWhenTheDrainRunsOut(t *testing.T) {
	prev := servo.DefaultStopBudget
	servo.DefaultStopBudget = 100 * time.Millisecond
	t.Cleanup(func() { servo.DefaultStopBudget = prev })

	started := make(chan struct{})
	sawCause := make(chan error, 1)
	srv, ready, cancel, base := startServer(t, servo.HTTPListener{ReadHeaderTimeout: time.Second}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
		sawCause <- context.Cause(r.Context())
	}))
	clientErr := make(chan error, 1)
	go func() {
		_, _, err := get(t, base+"/")
		clientErr <- err
	}()
	<-started

	began := time.Now()
	r := StopServer(context.Background(), "http", srv, ready, 0, cancel)
	if r.Status != servo.StatusAbandoned || !strings.Contains(r.Err.Error(), "drain did not finish") {
		t.Fatalf("result = %+v", r)
	}
	if took := time.Since(began); took > 2*time.Second {
		t.Fatalf("took %v, want about the 100ms budget", took)
	}
	select {
	case cause := <-sawCause:
		if !errors.Is(cause, errDrainExpired) {
			t.Fatalf("handler's context ended with %v", cause)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the handler's context was never cancelled")
	}
	if err := <-clientErr; err == nil {
		t.Fatal("the client got a response from a server that cut its connection")
	}
}

// A stop whose context is already done drains nothing, but still closes the
// listener: left open, it would go on accepting while main exits.
func TestStopServerWithNoTimeLeftStillCloses(t *testing.T) {
	srv, ready, cancel, base := startServer(t, servo.HTTPListener{ReadHeaderTimeout: time.Second}, http.NotFoundHandler())
	ctx, stop := context.WithCancel(context.Background())
	stop()
	r := StopServer(ctx, "http", srv, ready, time.Minute, cancel)
	if r.Status != servo.StatusAbandoned || !errors.Is(r.Err, context.Canceled) || !strings.Contains(r.Err.Error(), "not attempted") {
		t.Fatalf("result = %+v", r)
	}
	if _, _, err := get(t, base+"/"); err == nil {
		t.Fatal("listener still accepting")
	}
}

// The status contract, applied once at run time for every adapter.
func TestRespond(t *testing.T) {
	cases := []struct {
		name string
		res  servo.Response
		err  error
		code int
		body string
	}{
		{"nil error is a 200", servo.Text("hi"), nil, 200, "hi"},
		{"a 2xx status picks the code", servo.JSON(1), servo.Status.CREATED, 201, "1\n"},
		{"a 3xx status redirects", servo.Redirect("/x"), servo.Status.FOUND, 302, ""},
		{"nil response is the code alone", nil, servo.Status.NO_CONTENT, 204, ""},
		{"a 4xx status is a failure", servo.Text("dropped"), servo.Status.CONFLICT.New("taken"), 409, "{\"error\":\"taken\"}\n"},
		{"the zero status is a 500", servo.JSON(1), servo.HTTPStatus{}, 500, "{\"error\":\"Internal Server Error\"}\n"},
		{"a code past 999 is a 500", nil, servo.StatusFor(1000), 500, "{\"error\":\"Internal Server Error\"}\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			captureLogs(t)
			w := httptest.NewRecorder()
			Respond(w, markedRequest("GET", "/x"), "app.H", "GET /x", c.res, c.err)
			if w.Code != c.code || w.Body.String() != c.body {
				t.Fatalf("wrote %d %q, want %d %q", w.Code, w.Body.String(), c.code, c.body)
			}
		})
	}
}

// A payload that does not encode is a whole 500 (WriteResponse answered it)
// and one Error line — no abort, since nothing was cut short.
func TestRespondLogsAnEncodeFailure(t *testing.T) {
	logs := captureLogs(t)
	w := httptest.NewRecorder()
	r := markedRequest("GET", "/x")
	r = r.WithContext(context.WithValue(r.Context(), http.ServerContextKey, &http.Server{}))
	Respond(w, r, "app.H", "GET /x", servo.JSON(math.NaN()), servo.Status.CREATED)
	if w.Code != 500 {
		t.Fatalf("code = %d, want 500", w.Code)
	}
	rec := logs.only(t)
	if rec.level != slog.LevelError || rec.msg != "servo: response did not encode" || rec.attrs["handler"] != "app.H" || rec.attrs["route"] != "GET /x" || rec.mark != "req-1" {
		t.Fatalf("logged %+v", rec)
	}
}

// failingWriter is a ResponseWriter whose body writes fail, as a broken
// connection's do.
type failingWriter struct{ *httptest.ResponseRecorder }

func (f failingWriter) Write([]byte) (int, error)       { return 0, errors.New("broken pipe") }
func (f failingWriter) WriteString(string) (int, error) { return 0, errors.New("broken pipe") }

// A write that fails once the status line is out aborts the response —
// under a real server only, and not when the request has already ended.
func TestRespondAbortsACutShortResponse(t *testing.T) {
	served := func(ctx context.Context) context.Context {
		return context.WithValue(ctx, http.ServerContextKey, &http.Server{})
	}
	t.Run("under a server", func(t *testing.T) {
		logs := captureLogs(t)
		r := markedRequest("GET", "/x")
		r = r.WithContext(served(r.Context()))
		defer func() {
			if p := recover(); p != http.ErrAbortHandler {
				t.Fatalf("recovered %v, want http.ErrAbortHandler", p)
			}
			if rec := logs.only(t); rec.level != slog.LevelError || rec.msg != "servo: writing the response failed" || rec.attrs["status"] != "200" {
				t.Fatalf("logged %+v", rec)
			}
		}()
		Respond(failingWriter{httptest.NewRecorder()}, r, "app.H", "GET /x", servo.Text("x"), nil)
		t.Fatal("Respond returned instead of aborting")
	})
	t.Run("under a recorder", func(t *testing.T) {
		logs := captureLogs(t)
		Respond(failingWriter{httptest.NewRecorder()}, markedRequest("GET", "/x"), "app.H", "GET /x", servo.Text("x"), nil)
		if rec := logs.only(t); rec.level != slog.LevelError {
			t.Fatalf("logged %+v", rec)
		}
	})
	t.Run("after the client left", func(t *testing.T) {
		logs := captureLogs(t)
		ctx, cancel := context.WithCancel(served(context.WithValue(context.Background(), markKey{}, "req-1")))
		cancel()
		r := httptest.NewRequest("GET", "/x", nil).WithContext(ctx)
		Respond(failingWriter{httptest.NewRecorder()}, r, "app.H", "GET /x", servo.Text("x"), nil)
		if rec := logs.only(t); rec.level != slog.LevelDebug || !strings.Contains(rec.msg, "request ended first") {
			t.Fatalf("logged %+v", rec)
		}
	})
	t.Run("a body its status forbids", func(t *testing.T) {
		logs := captureLogs(t)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			Respond(w, req.WithContext(context.WithValue(req.Context(), markKey{}, "req-1")), "app.H", "GET /x", servo.Text("x"), servo.Status.NO_CONTENT)
		}))
		defer srv.Close()
		if code, _, err := get(t, srv.URL); err != nil || code != 204 {
			t.Fatalf("GET = %d %v, want the whole 204", code, err)
		}
		if rec := logs.only(t); rec.level != slog.LevelError || !strings.Contains(rec.msg, "refused by its status") {
			t.Fatalf("logged %+v", rec)
		}
	})
}

// endlessThenBroken streams more than net/http buffers, so the status
// line and a first chunk are on the wire, then fails.
type endlessThenBroken struct{ sent int }

func (e *endlessThenBroken) Read(p []byte) (int, error) {
	if e.sent >= 64<<10 {
		return 0, errors.New("upstream reset")
	}
	n := min(len(p), 64<<10-e.sent)
	for i := range p[:n] {
		p[i] = 'x'
	}
	e.sent += n
	return n, nil
}

// End to end: a Stream that fails partway reaches the client as a broken
// response, never as a complete one.
func TestRespondTruncationIsVisibleToTheClient(t *testing.T) {
	captureLogs(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		Respond(w, r, "app.H", "GET /x", servo.Stream("text/plain", &endlessThenBroken{}), nil)
	}))
	defer srv.Close()
	code, body, err := get(t, srv.URL)
	if err == nil {
		t.Fatalf("client read %d bytes under %d with no error: the truncation looked like success", len(body), code)
	}
}

// The error path: statuses, bodies, levels, and the request's context on
// every log call.
func TestWriteFailure(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		code   int
		body   string
		level  slog.Level
		logged bool
	}{
		{"no status is a 500", errors.New("db down"), 500, "{\"error\":\"Internal Server Error\"}\n", slog.LevelError, true},
		{"a 5xx keeps its detail for the log", servo.Status.BAD_GATEWAY.Wrap(errors.New("upstream at 10.0.0.7 refused")), 502, "{\"error\":\"Bad Gateway\"}\n", slog.LevelError, true},
		{"a 4xx carries its message", servo.Status.NOT_FOUND.New("no such order"), 404, "{\"error\":\"no such order\"}\n", 0, false},
		{"a sub-400 status is the code alone", servo.Status.ACCEPTED, 202, "", 0, false},
		{"the zero status is a 500", fmt.Errorf("lost: %w", servo.HTTPStatus{}), 500, "{\"error\":\"Internal Server Error\"}\n", slog.LevelError, true},
		{"a full scope is shed", fmt.Errorf("rooms: %w", servo.ErrScopeFull), 503, "{\"error\":\"Service Unavailable\"}\n", slog.LevelDebug, true},
		{"a closed scope is shed", servo.ErrScopeClosed, 503, "{\"error\":\"Service Unavailable\"}\n", slog.LevelDebug, true},
		{"a status the handler chose wins over shedding", servo.Status.INTERNAL_SERVER_ERROR.Wrap(servo.ErrScopeFull), 500, "{\"error\":\"Internal Server Error\"}\n", slog.LevelError, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			logs := captureLogs(t)
			w := httptest.NewRecorder()
			WriteFailure(w, markedRequest("GET", "/orders/7"), "app.GetOrder", "GET /orders/{id}", c.err)
			if w.Code != c.code || w.Body.String() != c.body {
				t.Fatalf("wrote %d %q, want %d %q", w.Code, w.Body.String(), c.code, c.body)
			}
			if c.code == 503 && w.Header().Get("Retry-After") != "1" {
				t.Fatalf("a shed request needs Retry-After, got %q", w.Header().Get("Retry-After"))
			}
			if !c.logged {
				if all := logs.all(); len(all) != 0 {
					t.Fatalf("logged %+v", all)
				}
				return
			}
			rec := logs.only(t)
			if rec.level != c.level || rec.mark != "req-1" || rec.attrs["handler"] != "app.GetOrder" || rec.attrs["route"] != "GET /orders/{id}" || rec.attrs["status"] != strconv.Itoa(c.code) {
				t.Fatalf("logged %+v", rec)
			}
			// Constant messages: the handler and route are attributes,
			// never part of the text, so a log query can match them.
			if strings.Contains(rec.msg, "GetOrder") || strings.Contains(rec.msg, "/orders") {
				t.Fatalf("message %q embeds the request", rec.msg)
			}
		})
	}
}

// A handler that returns ctx.Err() after its client left did its job: a
// 499 for whatever records the status, nothing for the dead connection,
// and a Debug line rather than an Error one. A request the server cut
// during a drain that ran out is a 503, logged at Warn.
func TestWriteFailureWhenTheRequestEnded(t *testing.T) {
	t.Run("client went away", func(t *testing.T) {
		logs := captureLogs(t)
		ctx, cancel := context.WithCancel(context.WithValue(context.Background(), markKey{}, "req-1"))
		cancel()
		w := httptest.NewRecorder()
		WriteFailure(w, httptest.NewRequest("GET", "/x", nil).WithContext(ctx), "app.H", "GET /x", ctx.Err())
		if w.Code != 499 || w.Body.Len() != 0 {
			t.Fatalf("wrote %d %q, want a bare 499", w.Code, w.Body.String())
		}
		if rec := logs.only(t); rec.level != slog.LevelDebug || rec.mark != "req-1" || rec.attrs["status"] != "499" {
			t.Fatalf("logged %+v", rec)
		}
	})
	t.Run("drain ran out", func(t *testing.T) {
		logs := captureLogs(t)
		ctx, cancel := context.WithCancelCause(context.WithValue(context.Background(), markKey{}, "req-1"))
		cancel(errDrainExpired)
		w := httptest.NewRecorder()
		WriteFailure(w, httptest.NewRequest("GET", "/x", nil).WithContext(ctx), "app.H", "GET /x", ctx.Err())
		if w.Code != 503 {
			t.Fatalf("wrote %d, want 503", w.Code)
		}
		if rec := logs.only(t); rec.level != slog.LevelWarn || rec.mark != "req-1" {
			t.Fatalf("logged %+v", rec)
		}
	})
	t.Run("a deadline is not a departure", func(t *testing.T) {
		captureLogs(t)
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer cancel()
		w := httptest.NewRecorder()
		WriteFailure(w, httptest.NewRequest("GET", "/x", nil).WithContext(ctx), "app.H", "GET /x", ctx.Err())
		if w.Code != 500 {
			t.Fatalf("wrote %d, want the 500 a timed-out handler has always earned", w.Code)
		}
	})
}

func TestBodyError(t *testing.T) {
	w := httptest.NewRecorder()
	BodyError(w, &http.MaxBytesError{Limit: 10}, "malformed request body")
	if w.Code != 413 || !strings.Contains(w.Body.String(), "request body too large") {
		t.Fatalf("over the cap: %d %q", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	BodyError(w, io.ErrUnexpectedEOF, "malformed request body")
	if w.Code != 400 || w.Body.String() != "{\"error\":\"malformed request body\"}\n" {
		t.Fatalf("malformed: %d %q", w.Code, w.Body.String())
	}
}

// writeKeyPair writes a fresh self-signed certificate and its key under
// dir, the certificate's serial number set to serial so a test can tell
// which one a handshake was served.
func writeKeyPair(t *testing.T, dir string, serial int64) (certFile, keyFile string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(serial),
		Subject:      pkix.Name{CommonName: "servo-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certFile, keyFile = filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certFile, keyFile
}

// servedSerial completes one handshake and returns the serial number of
// the certificate the server presented.
func servedSerial(t *testing.T, addr string) int64 {
	t.Helper()
	conn, err := tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true}) //nolint:gosec // a test against its own self-signed certificate
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	defer func() { _ = conn.Close() }()
	return conn.ConnectionState().PeerCertificates[0].SerialNumber.Int64()
}

// A rotated key pair is served without a restart; a half-rotated one —
// the certificate rewritten before its key — keeps the previous
// certificate in service and is reported once.
func TestKeyPairReloadsWhenItsFilesChange(t *testing.T) {
	prev := keyPairCheckInterval
	keyPairCheckInterval = 0
	t.Cleanup(func() { keyPairCheckInterval = prev })
	logs := captureLogs(t)

	dir := t.TempDir()
	certFile, keyFile := writeKeyPair(t, dir, 1)
	srv, _, _, base := startServer(t, servo.HTTPListener{CertFile: certFile, KeyFile: keyFile, ReadHeaderTimeout: time.Second}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "tls")
	}))
	if !strings.HasPrefix(base, "https://") {
		t.Fatalf("base = %s", base)
	}
	if got := servedSerial(t, srv.Addr); got != 1 {
		t.Fatalf("served serial %d, want 1", got)
	}

	// Half a rotation: a new certificate beside the old key.
	other := filepath.Join(dir, "next")
	nextCert, nextKey := writeKeyPair(t, other, 2)
	copyFile(t, nextCert, certFile, time.Now().Add(time.Minute))
	if got := servedSerial(t, srv.Addr); got != 1 {
		t.Fatalf("served serial %d mid-rotation, want the previous 1", got)
	}
	if got := servedSerial(t, srv.Addr); got != 1 {
		t.Fatalf("served serial %d mid-rotation, want the previous 1", got)
	}
	var warnings int
	for _, rec := range logs.all() {
		if rec.level == slog.LevelWarn && strings.Contains(rec.msg, "reloading the TLS key pair failed") {
			warnings++
		}
	}
	if warnings != 1 {
		t.Fatalf("%d warnings for one half-rotated pair, want 1: %+v", warnings, logs.all())
	}

	// The key follows; the new pair is served.
	copyFile(t, nextKey, keyFile, time.Now().Add(2*time.Minute))
	if got := servedSerial(t, srv.Addr); got != 2 {
		t.Fatalf("served serial %d after the rotation, want 2", got)
	}
}

// copyFile replaces dst with src's contents and gives it modTime, so the
// change is visible however coarse the filesystem's clock is.
func copyFile(t *testing.T, src, dst string, modTime time.Time) {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(dst, modTime, modTime); err != nil {
		t.Fatal(err)
	}
}
