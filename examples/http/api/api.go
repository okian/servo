// Package api holds the //servo: handlers. A handler is an ordinary
// exported function — context first, an optional request struct second,
// then graph-resolved dependencies — returning (servo.Json[T], error).
// `servo generate` finds the directives, emits the router, the typed
// request decoding and the server lifecycle into the injector package.
package api

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"

	"github.com/okian/servo/v3/servo"

	"example.com/servohttp/mw"
	"example.com/servohttp/store"
)

// NewHTTPConfig is the config node servo.HTTP() requires: constructed by
// the user, resolved like any other provider — so where the values come
// from (env here) stays this module's own business. The flat fields are
// the default group's listener; each declared group gets an entry in
// Groups, keyed by its name.
func NewHTTPConfig() (*servo.HTTPConfig, error) {
	cfg := &servo.HTTPConfig{IP: "127.0.0.1", Port: 8080}
	if raw := os.Getenv("HTTP_PORT"); raw != "" {
		p, err := strconv.ParseUint(raw, 10, 16)
		if err != nil {
			return nil, fmt.Errorf("HTTP_PORT: %w", err)
		}
		cfg.Port = uint16(p)
	}
	if raw := os.Getenv("HTTP_MAX_BODY"); raw != "" {
		limit, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("HTTP_MAX_BODY: %w", err)
		}
		cfg.MaxBodyBytes = limit
	}
	cfg.Groups = map[string]servo.HTTPListener{
		"telemetry": {IP: "127.0.0.1", Port: 9001},
		"internal":  {IP: "127.0.0.1", Port: 9002},
	}
	for name, env := range map[string]string{"telemetry": "HTTP_TELEMETRY_PORT", "internal": "HTTP_INTERNAL_PORT"} {
		raw := os.Getenv(env)
		if raw == "" {
			continue
		}
		p, err := strconv.ParseUint(raw, 10, 16)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", env, err)
		}
		lc := cfg.Groups[name]
		lc.Port = uint16(p)
		cfg.Groups[name] = lc
	}
	return cfg, nil
}

type OrderReq struct {
	Category string `path:"category"`
	Priority int    `query:"priority"`
	Item     string `json:"item"`
	Quantity int    `json:"quantity"`
}

type OrderResp struct {
	Category string `json:"category"`
	Item     string `json:"item"`
	Quantity int    `json:"quantity"`
	Priority int    `json:"priority"`
}

// Order is the full status contract in one handler: a domain sentinel
// becomes 404 with the wrapped message, a policy refusal 403, an
// infrastructure failure 500 (canonical text only — the wrapped detail is
// for the log), and success picks 201 by returning the status in the error
// position.
//
//servo:post /order/{category}/
func Order(ctx context.Context, req *OrderReq, st *store.Store) (servo.Json[*OrderResp], error) {
	cat, err := st.Category(req.Category)
	if errors.Is(err, store.ErrNotFound) {
		return nil, servo.Status.NOT_FOUND.Wrapf("category %q: %w", req.Category, err)
	}
	if err != nil {
		return nil, servo.Status.INTERNAL_SERVER_ERROR.Wrap(err)
	}
	if !cat.Open {
		return nil, servo.Status.FORBIDDEN.New("category is closed")
	}
	if req.Quantity <= 0 {
		return nil, servo.Status.UNPROCESSABLE_CONTENT.Newf("quantity %d: order at least one", req.Quantity)
	}
	resp := &OrderResp{Category: cat.Name, Item: req.Item, Quantity: req.Quantity, Priority: req.Priority}
	return servo.JSON(resp), servo.Status.CREATED
}

type SearchReq struct {
	Q     string  `query:"q"`
	Limit uint16  `query:"limit"`
	Ratio float64 `query:"ratio"`
	Exact bool    `query:"exact"`
}

type SearchResp struct {
	Q     string  `json:"q"`
	Limit uint16  `json:"limit"`
	Ratio float64 `json:"ratio"`
	Exact bool    `json:"exact"`
}

// Search echoes its typed query parameters back, existing to show the
// generated strconv parsing for every scalar family — and the 400 a
// malformed value earns.
//
//servo:get /search
func Search(ctx context.Context, req *SearchReq) (servo.Json[*SearchResp], error) {
	return servo.JSON(&SearchResp{Q: req.Q, Limit: req.Limit, Ratio: req.Ratio, Exact: req.Exact}), nil
}

type WhoamiResp struct {
	User      string `json:"user"`
	RequestID string `json:"request_id"`
}

// Whoami takes an extracted parameter: mw.UserExtractor produces the *User
// from the request (401 when the header is missing, before this runs), and
// the request id proves middleware context changes reach handlers.
//
//servo:get /whoami
func Whoami(ctx context.Context, user *mw.User) (servo.Json[*WhoamiResp], error) {
	return servo.JSON(&WhoamiResp{User: user.Name, RequestID: mw.IDFromContext(ctx)}), nil
}

type HealthzResp struct {
	OK        bool   `json:"ok"`
	RequestID string `json:"request_id"`
}

// Healthz lives on the telemetry group — its own listener, its own port —
// while the server-level RequestID middleware still wraps it.
//
//servo:get /healthz telemetry
func Healthz(ctx context.Context) (servo.Json[*HealthzResp], error) {
	return servo.JSON(&HealthzResp{OK: true, RequestID: mw.IDFromContext(ctx)}), nil
}

type ReplicateReq struct {
	Shard string `path:"shard"`
}

type ReplicateResp struct {
	Shard string `json:"shard"`
	By    string `json:"by"`
}

// Replicate lives on the internal group, behind the group-level Auth
// middleware, and also takes the extracted user.
//
//servo:post /replicate/{shard} internal
func Replicate(ctx context.Context, req *ReplicateReq, user *mw.User) (servo.Json[*ReplicateResp], error) {
	return servo.JSON(&ReplicateResp{Shard: req.Shard, By: user.Name}), nil
}

type FeedbackReq struct {
	Subject string `form:"subject"`
	Stars   int    `form:"stars"`
}

type FeedbackResp struct {
	Subject string `json:"subject"`
	Stars   int    `json:"stars"`
}

// Feedback binds from a form body — urlencoded or multipart, the generated
// decoder handles both.
//
//servo:post /feedback
func Feedback(ctx context.Context, req *FeedbackReq) (servo.Json[*FeedbackResp], error) {
	return servo.JSON(&FeedbackResp{Subject: req.Subject, Stars: req.Stars}), nil
}
