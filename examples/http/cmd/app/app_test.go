package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

// startApp builds the generated App on three free ports — one per group —
// and runs it until the test ends, failing fast if any listener never
// becomes ready. It returns the default, telemetry and internal base URLs.
func startApp(t *testing.T) (string, string, string) {
	t.Helper()

	freePort := func() int {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("pick port: %v", err)
		}
		port := ln.Addr().(*net.TCPAddr).Port
		_ = ln.Close()
		return port
	}
	port := freePort()
	telPort := freePort()
	intPort := freePort()
	t.Setenv("HTTP_PORT", strconv.Itoa(port))
	t.Setenv("HTTP_TELEMETRY_PORT", strconv.Itoa(telPort))
	t.Setenv("HTTP_INTERNAL_PORT", strconv.Itoa(intPort))
	t.Setenv("HTTP_MAX_BODY", "1024")

	ctx, cancel := context.WithCancel(context.Background())
	app, err := New(ctx)
	if err != nil {
		cancel()
		t.Fatalf("New: %v", err)
	}

	runErr := make(chan error, 1)
	go func() { runErr <- app.Run(ctx) }()

	t.Cleanup(func() {
		cancel()
		if err := <-runErr; err != nil {
			t.Errorf("Run returned %v", err)
		}
		sctx, scancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer scancel()
		if r := app.Shutdown(sctx); !r.Clean() {
			t.Errorf("Shutdown not clean: %v", r)
		}
	})

	deadline := time.Now().Add(5 * time.Second)
	for !app.Ready(ctx).Clean() {
		if time.Now().After(deadline) {
			t.Fatalf("servers never became ready: %v", app.Ready(ctx))
		}
		time.Sleep(10 * time.Millisecond)
	}
	base := func(p int) string { return fmt.Sprintf("http://127.0.0.1:%d", p) }
	return base(port), base(telPort), base(intPort)
}

// call issues one request and returns the status code, the decoded JSON
// body (nil when there was none) and the raw body text.
func call(t *testing.T, req *http.Request) (int, map[string]any, string) {
	t.Helper()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", req.Method, req.URL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	var decoded map[string]any
	// The mux's own responses (405, 404 for unknown paths) are plain text;
	// only the generated handlers promise JSON.
	if len(raw) > 0 && strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatalf("body is not JSON: %v\n%s", err, raw)
		}
	}
	return resp.StatusCode, decoded, string(raw)
}

func post(t *testing.T, url, contentType, body string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", contentType)
	return req
}

func get(t *testing.T, url string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	return req
}

func TestHTTPEndToEnd(t *testing.T) {
	base, telemetry, internal := startApp(t)

	t.Run("created with echoed body", func(t *testing.T) {
		code, body, _ := call(t, post(t, base+"/order/espresso/?priority=2", "application/json", `{"item":"latte","quantity":3}`))
		if code != http.StatusCreated {
			t.Fatalf("status = %d, want 201", code)
		}
		if body["category"] != "espresso" || body["item"] != "latte" || body["quantity"] != float64(3) || body["priority"] != float64(2) {
			t.Fatalf("body = %v", body)
		}
	})

	t.Run("not found carries the wrapped message", func(t *testing.T) {
		code, body, _ := call(t, post(t, base+"/order/tea/", "application/json", `{"item":"x","quantity":1}`))
		if code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", code)
		}
		if body["error"] != `category "tea": no such category` {
			t.Fatalf("error = %q", body["error"])
		}
	})

	t.Run("forbidden carries the handler's message", func(t *testing.T) {
		code, body, _ := call(t, post(t, base+"/order/seasonal/", "application/json", `{"item":"x","quantity":1}`))
		if code != http.StatusForbidden || body["error"] != "category is closed" {
			t.Fatalf("status=%d body=%v", code, body)
		}
	})

	t.Run("unprocessable via Newf", func(t *testing.T) {
		code, body, _ := call(t, post(t, base+"/order/espresso/", "application/json", `{"item":"x","quantity":0}`))
		if code != http.StatusUnprocessableEntity || body["error"] != "quantity 0: order at least one" {
			t.Fatalf("status=%d body=%v", code, body)
		}
	})

	t.Run("internal error hides the wrapped detail", func(t *testing.T) {
		code, body, raw := call(t, post(t, base+"/order/broken/", "application/json", `{"item":"x","quantity":1}`))
		if code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500", code)
		}
		if body["error"] != "Internal Server Error" {
			t.Fatalf("error = %q, want the canonical text only", body["error"])
		}
		if strings.Contains(raw, "db down") {
			t.Fatalf("response leaked the wrapped detail: %s", raw)
		}
	})

	t.Run("malformed query int is a 400", func(t *testing.T) {
		code, body, _ := call(t, post(t, base+"/order/espresso/?priority=abc", "application/json", `{"item":"x","quantity":1}`))
		if code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", code)
		}
		if body["error"] != `query parameter "priority" is not a valid int` {
			t.Fatalf("error = %q", body["error"])
		}
	})

	t.Run("malformed body is a 400", func(t *testing.T) {
		code, body, _ := call(t, post(t, base+"/order/espresso/", "application/json", `{`))
		if code != http.StatusBadRequest || body["error"] != "malformed request body" {
			t.Fatalf("status=%d body=%v", code, body)
		}
	})

	t.Run("body over the configured limit is a 400", func(t *testing.T) {
		big := `{"item":"` + strings.Repeat("x", 2048) + `","quantity":1}`
		code, _, _ := call(t, post(t, base+"/order/espresso/", "application/json", big))
		if code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", code)
		}
	})

	t.Run("typed query scalars round-trip", func(t *testing.T) {
		code, body, _ := call(t, get(t, base+"/search?q=beans&limit=7&ratio=1.5&exact=true"))
		if code != http.StatusOK {
			t.Fatalf("status = %d", code)
		}
		if body["q"] != "beans" || body["limit"] != float64(7) || body["ratio"] != 1.5 || body["exact"] != true {
			t.Fatalf("body = %v", body)
		}
	})

	t.Run("absent optional query values stay zero", func(t *testing.T) {
		code, body, _ := call(t, get(t, base+"/search"))
		if code != http.StatusOK || body["limit"] != float64(0) || body["exact"] != false {
			t.Fatalf("status=%d body=%v", code, body)
		}
	})

	t.Run("extracted parameter reaches the handler", func(t *testing.T) {
		req := get(t, base+"/whoami")
		req.Header.Set("X-User", "kian")
		code, body, _ := call(t, req)
		if code != http.StatusOK || body["user"] != "kian" {
			t.Fatalf("status=%d body=%v", code, body)
		}
	})

	t.Run("failed extraction is a 401 before the handler", func(t *testing.T) {
		code, body, _ := call(t, get(t, base+"/whoami"))
		if code != http.StatusUnauthorized || body["error"] != "missing X-User header" {
			t.Fatalf("status=%d body=%v", code, body)
		}
	})

	t.Run("middleware sets the response header and the context value", func(t *testing.T) {
		req := get(t, base+"/whoami")
		req.Header.Set("X-User", "kian")
		req.Header.Set("X-Request-Id", "req-42")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		if got := resp.Header.Get("X-Request-Id"); got != "req-42" {
			t.Fatalf("X-Request-Id header = %q", got)
		}
		var body map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		// The context value the middleware planted made it into the
		// handler's ctx — that is the "context change" half of the story.
		if body["request_id"] != "req-42" {
			t.Fatalf("request_id = %v", body["request_id"])
		}
	})

	t.Run("telemetry group serves on its own port only", func(t *testing.T) {
		code, body, _ := call(t, get(t, telemetry+"/healthz"))
		if code != http.StatusOK || body["ok"] != true {
			t.Fatalf("status=%d body=%v", code, body)
		}
		// The server-level middleware wraps this group too — the shipped
		// RequestID generated an id and the handler saw it in its context.
		if id, _ := body["request_id"].(string); id == "" {
			t.Fatalf("request_id = %v, want a generated id", body["request_id"])
		}
		code, _, _ = call(t, get(t, base+"/healthz"))
		if code != http.StatusNotFound {
			t.Fatalf("healthz leaked onto the default port: %d", code)
		}
		code, _, _ = call(t, get(t, telemetry+"/whoami"))
		if code != http.StatusNotFound {
			t.Fatalf("default route leaked onto the telemetry port: %d", code)
		}
	})

	t.Run("internal group sits behind its auth middleware", func(t *testing.T) {
		req := post(t, internal+"/replicate/shard-7", "application/json", "")
		code, _, _ := call(t, req)
		if code != http.StatusUnauthorized {
			t.Fatalf("unauthenticated internal call = %d, want 401", code)
		}

		req = post(t, internal+"/replicate/shard-7", "application/json", "")
		req.Header.Set("X-Token", "letmein")
		req.Header.Set("X-User", "replicator")
		code, body, _ := call(t, req)
		if code != http.StatusOK || body["shard"] != "shard-7" || body["by"] != "replicator" {
			t.Fatalf("status=%d body=%v", code, body)
		}

		// The group middleware guards only its group.
		pub := get(t, base+"/search")
		code, _, _ = call(t, pub)
		if code != http.StatusOK {
			t.Fatalf("default group must not require the internal token: %d", code)
		}
	})

	t.Run("urlencoded form", func(t *testing.T) {
		form := url.Values{"subject": {"great coffee"}, "stars": {"5"}}
		code, body, _ := call(t, post(t, base+"/feedback", "application/x-www-form-urlencoded", form.Encode()))
		if code != http.StatusOK || body["subject"] != "great coffee" || body["stars"] != float64(5) {
			t.Fatalf("status=%d body=%v", code, body)
		}
	})

	t.Run("multipart form", func(t *testing.T) {
		var buf strings.Builder
		w := multipart.NewWriter(&buf)
		if err := w.WriteField("subject", "grinder jammed"); err != nil {
			t.Fatal(err)
		}
		if err := w.WriteField("stars", "2"); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		code, body, _ := call(t, post(t, base+"/feedback", w.FormDataContentType(), buf.String()))
		if code != http.StatusOK || body["subject"] != "grinder jammed" || body["stars"] != float64(2) {
			t.Fatalf("status=%d body=%v", code, body)
		}
	})

	t.Run("malformed form int is a 400", func(t *testing.T) {
		form := url.Values{"subject": {"x"}, "stars": {"many"}}
		code, body, _ := call(t, post(t, base+"/feedback", "application/x-www-form-urlencoded", form.Encode()))
		if code != http.StatusBadRequest || body["error"] != `form value "stars" is not a valid int` {
			t.Fatalf("status=%d body=%v", code, body)
		}
	})

	t.Run("plain text response", func(t *testing.T) {
		req := get(t, base+"/version")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		raw, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK || string(raw) != "servohttp 1.0" {
			t.Fatalf("status=%d body=%q", resp.StatusCode, raw)
		}
		if ct := resp.Header.Get("Content-Type"); ct != "text/plain; charset=utf-8" {
			t.Fatalf("Content-Type = %q", ct)
		}
	})

	t.Run("redirect flows through the success path", func(t *testing.T) {
		noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}}
		resp, err := noFollow.Get(base + "/old-orders")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/search" {
			t.Fatalf("status=%d location=%q", resp.StatusCode, resp.Header.Get("Location"))
		}
	})

	t.Run("shipped CORS answers preflights on the public group", func(t *testing.T) {
		req, err := http.NewRequest(http.MethodOptions, base+"/search", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Origin", "https://app.example.com")
		req.Header.Set("Access-Control-Request-Method", "GET")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("preflight status = %d, want 204", resp.StatusCode)
		}
		if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
			t.Fatalf("Allow-Origin = %q", got)
		}
		// The telemetry group has no CORS attachment: same preflight there
		// falls through to the mux.
		req2, err := http.NewRequest(http.MethodOptions, telemetry+"/healthz", nil)
		if err != nil {
			t.Fatal(err)
		}
		req2.Header.Set("Origin", "https://app.example.com")
		req2.Header.Set("Access-Control-Request-Method", "GET")
		resp2, err := http.DefaultClient.Do(req2)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp2.Body.Close() }()
		if resp2.Header.Get("Access-Control-Allow-Origin") != "" {
			t.Fatalf("telemetry group must not carry the default group's CORS")
		}
	})

	t.Run("wrong method is the mux's 405", func(t *testing.T) {
		code, _, _ := call(t, get(t, base+"/order/espresso/"))
		if code != http.StatusMethodNotAllowed {
			t.Fatalf("status = %d, want 405", code)
		}
	})
}
