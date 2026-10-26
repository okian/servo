package servoimpl

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/okian/servo/v3/servo"
)

// This file is the runtime half of the servers `servo generate` emits for
// a spec that declares servo.HTTP(). What varies with the user's types —
// the routing table, the request decoding, the handler calls, the
// middleware chain — stays generated; what does not — building and
// binding a listener, draining it, writing a response, turning an error
// into a status — lives here, where it is written and tested once.

// DefaultMaxBodyBytes is the request-body cap a listener gets when its
// MaxBodyBytes is zero or negative.
const DefaultMaxBodyBytes = 1 << 20

// statusClientClosedRequest is nginx's 499: no client ever reads it, but
// whatever wraps the writer — an access log, a metrics middleware —
// records a disconnect instead of a success or a server error.
const statusClientClosedRequest = 499

var (
	// errDrainExpired is the cause a server's request contexts are
	// cancelled with when its drain runs out of time, which is how the
	// error path tells a request the server cut short from one whose
	// client went away.
	errDrainExpired = errors.New("servo: the server's drain ran out of time, so it cancelled its in-flight requests")
	// errServerStopped is the cause once a drain has finished: by then no
	// request is left to see it.
	errServerStopped = errors.New("servo: the server has stopped")
)

// Listener returns the listener a group's server binds: the flat fields
// of cfg for the default group (""), cfg.Groups[group] for a named one. A
// declared group missing from Groups is the one configuration error only
// New can see — group names are generate-time facts, ports are runtime
// values — and fails it with an error naming the group.
func Listener(cfg *servo.HTTPConfig, group string) (servo.HTTPListener, error) {
	if cfg == nil {
		return servo.HTTPListener{}, errors.New("http: the *servo.HTTPConfig provider returned nil")
	}
	if group != "" {
		lc, ok := cfg.Groups[group]
		if !ok {
			return servo.HTTPListener{}, fmt.Errorf("http: group %q declared in the spec but missing from HTTPConfig.Groups", group)
		}
		return lc, nil
	}
	// Every flat field but Groups, by name. TestListenerCopiesEveryField
	// fails the day a field is added to HTTPConfig and not here — the
	// default group would otherwise ignore it without a word.
	return servo.HTTPListener{
		IP:                cfg.IP,
		Port:              cfg.Port,
		CertFile:          cfg.CertFile,
		KeyFile:           cfg.KeyFile,
		MaxBodyBytes:      cfg.MaxBodyBytes,
		MaxHeaderBytes:    cfg.MaxHeaderBytes,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
		ShutdownDelay:     cfg.ShutdownDelay,
	}, nil
}

// BodyLimit returns the request-body cap lc sets, or DefaultMaxBodyBytes.
func BodyLimit(lc servo.HTTPListener) int64 {
	if lc.MaxBodyBytes <= 0 {
		return DefaultMaxBodyBytes
	}
	return lc.MaxBodyBytes
}

// NewServer builds the *http.Server for one group's listener. It binds
// nothing; Serve does. name is the server's node name, "http" or
// "http:<group>", which every error and log line carries.
//
// A TLS listener's key pair is loaded here, so a missing, unreadable or
// mismatched file fails New, before any Init runs, with an error naming
// the server and both files — rather than surfacing in Run, after every
// Init, as the error of a server that never started. The files are then
// watched: see watchedKeyPair.
//
// Request contexts descend from a context of the server's own, not from
// the one Run is given, so the signal that ends Run does not cancel the
// requests a graceful drain is about to let finish. The returned cancel
// ends it, and only StopServer calls it: early when a drain runs out of
// time, otherwise once the server is down.
func NewServer(name string, lc servo.HTTPListener, handler http.Handler) (*http.Server, context.CancelCauseFunc, error) {
	tlsConfig, err := serverTLS(name, lc.CertFile, lc.KeyFile)
	if err != nil {
		return nil, nil, err
	}
	base, cancel := context.WithCancelCause(context.Background())
	return &http.Server{
		Addr:              net.JoinHostPort(lc.IP, strconv.FormatUint(uint64(lc.Port), 10)),
		Handler:           handler,
		TLSConfig:         tlsConfig,
		ReadHeaderTimeout: lc.ReadHeaderTimeout,
		ReadTimeout:       lc.ReadTimeout,
		WriteTimeout:      lc.WriteTimeout,
		IdleTimeout:       lc.IdleTimeout,
		MaxHeaderBytes:    lc.MaxHeaderBytes,
		BaseContext:       func(net.Listener) context.Context { return base },
	}, cancel, nil
}

// Serve is a server's Run half: bind explicitly, so ready flips at a
// precise moment, serve on a goroutine of its own, and return when ctx
// ends — the drain is StopServer's — or when serving fails. A bind failure
// is returned as net.Listen reports it ("listen tcp :8080: bind: address
// already in use"); Run names the server around it.
func Serve(ctx context.Context, name string, srv *http.Server, ready *atomic.Bool) error {
	// Decided before serving starts: net/http's Serve fills in TLSConfig
	// for its HTTP/2 setup even on a plain listener, so the field cannot
	// be asked afterwards. NewServer sets it exactly when the listener has
	// a key pair.
	useTLS := srv.TLSConfig != nil
	ln, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		return err
	}
	// net/http reads the headers under ReadHeaderTimeout, or under
	// ReadTimeout when that is zero, and under neither when the one it
	// picks is zero or negative.
	header := srv.ReadHeaderTimeout
	if header == 0 {
		header = srv.ReadTimeout
	}
	if header <= 0 {
		slog.WarnContext(ctx, "servo: HTTP server has no header timeout: a client that never finishes sending its headers holds its connection open indefinitely, and a few hundred such clients exhaust the server; set ReadHeaderTimeout on its listener", "server", name, "addr", ln.Addr().String())
	}
	ready.Store(true)

	served := make(chan error, 1)
	go func() {
		var err error
		if useTLS {
			err = srv.ServeTLS(ln, "", "")
		} else {
			err = srv.Serve(ln)
		}
		// However serving ended, the listener is gone: Ready must stop
		// saying it is bound, and StopServer must not hold a dead server
		// open for its ShutdownDelay.
		ready.Store(false)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		served <- err
	}()
	select {
	case <-ctx.Done():
		return nil
	case err := <-served:
		return err
	}
}

// StopServer takes one server out of service, each step bounded by ctx:
//
//  1. Readiness drops, so a probe landing during what follows is told the
//     truth.
//  2. The server goes on serving for delay — its listener's ShutdownDelay
//     — which is the time load balancers and Kubernetes endpoints need to
//     stop routing to it; a listener closed first would turn their last
//     requests into connection errors. A server that was never ready was
//     never in rotation, so nothing can still be routing to it and it
//     skips the delay. ctx ending cuts the wait short.
//  3. The drain: srv.Shutdown closes the listener and waits for in-flight
//     requests, for whatever is left of ctx's deadline — or, when ctx has
//     none, for servo.DefaultStopBudget.
//  4. If the drain does not finish, every request context is cancelled, so
//     r.Context() is done in every handler still running, and every
//     connection closed. Left running, those handlers would go on calling
//     dependencies the rest of Shutdown is about to stop.
//
// The result is StatusOK once the drain finishes. It is StatusAbandoned
// when it does not — the requests were cancelled and their connections
// closed, but a handler that ignores its context may still be running,
// which is what abandoned means everywhere else — and also when ctx was
// already done before the stop began: then nothing is attempted but the
// close. It is StatusFailed when srv.Shutdown fails for another reason.
func StopServer(ctx context.Context, name string, srv *http.Server, ready *atomic.Bool, delay time.Duration, cancel context.CancelCauseFunc) servo.NodeResult {
	if cancel == nil {
		cancel = func(error) {}
	}
	inRotation := ready.Swap(false)

	if err := ctx.Err(); err != nil {
		// Closing cannot block, so even a stop that has no time left
		// still stops the listener accepting, rather than leave it
		// serving while main exits around it.
		cancel(errDrainExpired)
		_ = srv.Close()
		return servo.NodeResult{Name: name, Status: servo.StatusAbandoned, Err: fmt.Errorf("servo: drain not attempted, its context was already done, so every connection was closed at once: %w", err)}
	}

	if inRotation && delay > 0 {
		if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) <= delay {
			slog.WarnContext(ctx, "servo: ShutdownDelay is not shorter than what is left of the shutdown deadline, so no time will be left to drain the requests in flight", "server", name, "shutdown_delay", delay, "left", time.Until(deadline).Round(time.Millisecond))
		}
		timer := time.NewTimer(delay)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
		}
	}

	drainCtx := ctx
	if _, ok := ctx.Deadline(); !ok {
		var stop context.CancelFunc
		drainCtx, stop = context.WithTimeout(ctx, servo.DefaultStopBudget)
		defer stop()
	}
	err := srv.Shutdown(drainCtx)
	if err != nil && drainCtx.Err() != nil {
		// Cancelled before closed, so a handler blocked on its context
		// returns, rather than meet a dead connection at its next write.
		cancel(errDrainExpired)
		_ = srv.Close()
		return servo.NodeResult{Name: name, Status: servo.StatusAbandoned, Err: fmt.Errorf("servo: drain did not finish: %w; every in-flight request's context was cancelled and its connection closed, but a handler that ignores its context may still be running", err)}
	}
	cancel(errServerStopped)
	if err != nil {
		// Shutdown returns anything but its context's error only once
		// every connection is idle: the drain finished, and closing a
		// listener is what failed.
		return servo.NodeResult{Name: name, Status: servo.StatusFailed, Err: err}
	}
	return servo.NodeResult{Name: name, Status: servo.StatusOK}
}

// Respond finishes one request with what its handler returned: the status
// contract, applied at run time so every adapter shares one copy. A nil
// error is a 200; a 2xx or 3xx HTTPStatus in the error position picks the
// success or redirect code instead; anything else goes to WriteFailure,
// and res is dropped. handler and route name the request in the log.
//
// Writing can fail after the status line is out — a Stream whose reader
// fails partway, a connection that breaks — and ending the response
// normally would then hand the client a cut-short body that reads as
// complete. So Respond panics with http.ErrAbortHandler, which net/http
// recovers without a stack trace, breaking the HTTP/1 connection or
// resetting the HTTP/2 stream so the client sees the truncation. It does
// not when the request's context is already done (the client went away,
// or the server cut the request), nor when no net/http server is serving
// the request — httptest's recorder, in a test, has no connection to
// break and nothing to recover the panic.
func Respond(w http.ResponseWriter, r *http.Request, handler, route string, res servo.Response, err error) {
	code := http.StatusOK
	if err != nil {
		var hs servo.HTTPStatus
		if !errors.As(err, &hs) || !validCode(hs.Code()) || hs.Code() >= 400 {
			WriteFailure(w, r, handler, route, err)
			return
		}
		code = hs.Code()
	}
	werr := servo.WriteResponse(w, code, res)
	if werr == nil {
		return
	}

	ctx := r.Context()
	var hs servo.HTTPStatus
	switch {
	case errors.As(werr, &hs):
		// WriteResponse failed before writing anything — the payload did
		// not encode — and has already answered with hs, a whole 500.
		slog.ErrorContext(ctx, "servo: response did not encode", "handler", handler, "route", route, "status", hs.Code(), "error", werr)
	case ctx.Err() != nil:
		// A write to a request that has already ended fails as a matter
		// of course; it is the ending, not the write, worth knowing.
		slog.Log(ctx, endedLevel(ctx), "servo: response cut short: the request ended first", "handler", handler, "route", route, "status", code, "error", werr, "cause", context.Cause(ctx))
	case errors.Is(werr, http.ErrBodyNotAllowed):
		// A body under a status that forbids one (204, 304): the status
		// line went out whole and only the body was refused, so there
		// is nothing to break — but the handler has a bug.
		slog.ErrorContext(ctx, "servo: response body refused by its status", "handler", handler, "route", route, "status", code, "error", werr)
	default:
		slog.ErrorContext(ctx, "servo: writing the response failed", "handler", handler, "route", route, "status", code, "error", werr)
		if ctx.Value(http.ServerContextKey) != nil {
			panic(http.ErrAbortHandler)
		}
	}
}

// WriteFailure maps a handler's or extractor's error onto the response.
// The order matters:
//
//   - A request whose context was cancelled is not a failure of the
//     handler: returning ctx.Err() once the client has gone is exactly
//     what a well-behaved handler does. It is logged at Debug and
//     recorded as a 499, with no body for a connection nobody reads. A
//     request the server itself cancelled, because Shutdown's drain ran
//     out of time, is logged at Warn and recorded as a 503.
//   - With no HTTPStatus in the chain, servo.ErrScopeFull and
//     servo.ErrScopeClosed are a scope shedding load — at its Max cap, or
//     shutting down — not a fault: 503 with Retry-After, logged at Debug.
//     Any other error is a 500, logged at Error.
//   - An HTTPStatus picks the code. Under 100 or over 999 it is no status
//     at all — the zero HTTPStatus, most likely — and answers 500. Under
//     400 it is written as the code alone: an extractor returns a value or
//     an error, never a body. From 500 the body carries only the
//     canonical text and the error is logged; a 4xx body carries the
//     error's message, written for the API's caller.
//
// Every log line takes the request's context, so a slog handler that
// reads request or trace ids from it can attach them, and carries the
// handler and route as attributes.
func WriteFailure(w http.ResponseWriter, r *http.Request, handler, route string, err error) {
	ctx := r.Context()
	if errors.Is(ctx.Err(), context.Canceled) {
		if errors.Is(context.Cause(ctx), errDrainExpired) {
			slog.WarnContext(ctx, "servo: request cancelled: the shutdown drain ran out of time", "handler", handler, "route", route, "status", http.StatusServiceUnavailable, "error", err)
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		slog.DebugContext(ctx, "servo: client went away before the handler answered", "handler", handler, "route", route, "status", statusClientClosedRequest, "error", err)
		w.WriteHeader(statusClientClosedRequest)
		return
	}

	var hs servo.HTTPStatus
	if !errors.As(err, &hs) {
		if errors.Is(err, servo.ErrScopeFull) || errors.Is(err, servo.ErrScopeClosed) {
			slog.DebugContext(ctx, "servo: scope refused the request", "handler", handler, "route", route, "status", http.StatusServiceUnavailable, "error", err)
			w.Header().Set("Retry-After", "1")
			WriteError(w, http.StatusServiceUnavailable, servo.Status.SERVICE_UNAVAILABLE.Error())
			return
		}
		slog.ErrorContext(ctx, "servo: handler failed", "handler", handler, "route", route, "status", http.StatusInternalServerError, "error", err)
		WriteError(w, http.StatusInternalServerError, servo.Status.INTERNAL_SERVER_ERROR.Error())
		return
	}
	code := hs.Code()
	switch {
	case !validCode(code):
		slog.ErrorContext(ctx, "servo: handler failed with an HTTPStatus that is no status code", "handler", handler, "route", route, "status", http.StatusInternalServerError, "invalid_code", code, "error", err)
		WriteError(w, http.StatusInternalServerError, servo.Status.INTERNAL_SERVER_ERROR.Error())
	case code < 400:
		w.WriteHeader(code)
	case code >= 500:
		slog.ErrorContext(ctx, "servo: handler failed", "handler", handler, "route", route, "status", code, "error", err)
		WriteError(w, code, hs.Error())
	default:
		WriteError(w, code, err.Error())
	}
}

// WriteError writes the error envelope every generated failure shares:
// code, and {"error": msg} as JSON.
func WriteError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(struct {
		Error string `json:"error"`
	}{Error: msg})
}

// BodyError maps a request-body decoding failure onto the response: a body
// over the listener's cap is a 413, anything else the 400 msg describes.
func BodyError(w http.ResponseWriter, err error, msg string) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		WriteError(w, http.StatusRequestEntityTooLarge, "request body too large")
		return
	}
	WriteError(w, http.StatusBadRequest, msg)
}

// validCode reports whether net/http accepts code in WriteHeader.
func validCode(code int) bool { return code >= 100 && code <= 999 }

// endedLevel is how loudly a request that ended before its response did
// is logged: quietly when its client left, louder when the server cut it.
func endedLevel(ctx context.Context) slog.Level {
	if errors.Is(context.Cause(ctx), errDrainExpired) {
		return slog.LevelWarn
	}
	return slog.LevelDebug
}

// serverTLS returns the TLS configuration for a listener with both files
// set, nil for plain HTTP.
func serverTLS(name, certFile, keyFile string) (*tls.Config, error) {
	if certFile == "" || keyFile == "" {
		if certFile != "" || keyFile != "" {
			slog.Warn("servo: only one of CertFile and KeyFile is set, so this server speaks plain HTTP", "server", name, "cert_file", certFile, "key_file", keyFile)
		}
		return nil, nil
	}
	kp := &watchedKeyPair{name: name, certFile: certFile, keyFile: keyFile}
	if err := kp.load(); err != nil {
		return nil, err
	}
	return &tls.Config{GetCertificate: kp.certificate}, nil
}

// keyPairCheckInterval is how often a TLS server looks at its key pair's
// files, at most. A variable so tests need not wait it out.
var keyPairCheckInterval = 10 * time.Second

// watchedKeyPair serves a certificate from two files and rereads them when
// they change, so a rotated certificate — cert-manager's, a Kubernetes
// secret's — is picked up without a restart. The files are looked at during
// a handshake, at most once per keyPairCheckInterval and by one handshake at
// a time; every other handshake reads the current certificate without
// waiting. A pair that fails to load — the certificate written but not yet
// its key — is logged once and retried, while the last good certificate
// goes on being served.
type watchedKeyPair struct {
	name, certFile, keyFile string

	current   atomic.Pointer[tls.Certificate]
	nextCheck atomic.Int64 // unix nanoseconds

	mu     sync.Mutex // serialises reloads; guards the stamps
	loaded [2]fileStamp
	failed [2]fileStamp
}

// fileStamp is what a reload compares: a rewritten file, or a symlink
// swapped to a new one, changes at least one of the two.
type fileStamp struct {
	modTime time.Time
	size    int64
}

func stampOf(path string) fileStamp {
	fi, err := os.Stat(path)
	if err != nil {
		return fileStamp{}
	}
	return fileStamp{modTime: fi.ModTime(), size: fi.Size()}
}

// load reads the pair for the first time; its error fails New.
func (k *watchedKeyPair) load() error {
	stamps := [2]fileStamp{stampOf(k.certFile), stampOf(k.keyFile)}
	cert, err := tls.LoadX509KeyPair(k.certFile, k.keyFile)
	if err != nil {
		return fmt.Errorf("%s: loading the TLS key pair (certificate %s, key %s): %w", k.name, k.certFile, k.keyFile, err)
	}
	k.current.Store(&cert)
	k.loaded = stamps
	k.nextCheck.Store(time.Now().Add(keyPairCheckInterval).UnixNano())
	return nil
}

// certificate is the tls.Config's GetCertificate.
func (k *watchedKeyPair) certificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	now := time.Now()
	next := k.nextCheck.Load()
	if now.UnixNano() >= next && k.nextCheck.CompareAndSwap(next, now.Add(keyPairCheckInterval).UnixNano()) {
		k.reloadIfChanged()
	}
	return k.current.Load(), nil
}

func (k *watchedKeyPair) reloadIfChanged() {
	k.mu.Lock()
	defer k.mu.Unlock()
	stamps := [2]fileStamp{stampOf(k.certFile), stampOf(k.keyFile)}
	if stamps == k.loaded || stamps == k.failed {
		return
	}
	cert, err := tls.LoadX509KeyPair(k.certFile, k.keyFile)
	if err != nil {
		k.failed = stamps
		slog.Warn("servo: reloading the TLS key pair failed; still serving the previous certificate", "server", k.name, "cert_file", k.certFile, "key_file", k.keyFile, "error", err)
		return
	}
	k.current.Store(&cert)
	k.loaded = stamps
	k.failed = [2]fileStamp{}
}
