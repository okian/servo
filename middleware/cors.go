package middleware

import (
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

// CORSConfig is provided by your own provider — the origins are deployment
// facts, so where they come from stays your module's business.
type CORSConfig struct {
	// AllowedOrigins are exact origins ("https://app.example.com"), or the
	// single element "*" for any. Required.
	AllowedOrigins []string
	// AllowedMethods defaults to every method servo routes:
	// GET, POST, PUT, PATCH, DELETE, HEAD, OPTIONS.
	AllowedMethods []string
	// AllowedHeaders defaults to echoing whatever the preflight asked for.
	AllowedHeaders []string
	// ExposedHeaders are stamped on actual responses.
	ExposedHeaders   []string
	AllowCredentials bool
	// MaxAge caps how long a preflight may be cached; zero omits the header.
	MaxAge time.Duration
}

// CORS answers preflights itself (a 204 that never reaches the handler)
// and stamps actual responses. A disallowed origin gets no CORS headers at
// all — the browser enforces the block. With credentials allowed the
// concrete origin is echoed, never "*", because the combination is invalid.
type CORS struct {
	origins       []string
	wildcard      bool
	methods       string
	headers       string
	exposed       string
	credentials   bool
	maxAgeSeconds string
}

func NewCORS(cfg *CORSConfig) (*CORS, error) {
	if len(cfg.AllowedOrigins) == 0 {
		return nil, errors.New("middleware: CORSConfig.AllowedOrigins must not be empty")
	}
	c := &CORS{
		origins:     cfg.AllowedOrigins,
		wildcard:    slices.Contains(cfg.AllowedOrigins, "*"),
		credentials: cfg.AllowCredentials,
		methods:     strings.Join(cfg.AllowedMethods, ", "),
		headers:     strings.Join(cfg.AllowedHeaders, ", "),
		exposed:     strings.Join(cfg.ExposedHeaders, ", "),
	}
	if c.methods == "" {
		c.methods = "GET, POST, PUT, PATCH, DELETE, HEAD, OPTIONS"
	}
	if cfg.MaxAge > 0 {
		c.maxAgeSeconds = strconv.Itoa(int(cfg.MaxAge / time.Second))
	}
	return c, nil
}

func (c *CORS) allowOrigin(origin string) string {
	if c.wildcard {
		if c.credentials {
			return origin // "*" is invalid alongside credentials
		}
		return "*"
	}
	if slices.Contains(c.origins, origin) {
		return origin
	}
	return ""
}

func (c *CORS) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == "" {
			next.ServeHTTP(w, r)
			return
		}
		// The response differs by origin whether or not this one is
		// allowed, so caches must know either way.
		w.Header().Add("Vary", "Origin")

		allow := c.allowOrigin(origin)
		if allow == "" {
			next.ServeHTTP(w, r)
			return
		}

		if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", allow)
			h.Set("Access-Control-Allow-Methods", c.methods)
			if c.headers != "" {
				h.Set("Access-Control-Allow-Headers", c.headers)
			} else if req := r.Header.Get("Access-Control-Request-Headers"); req != "" {
				h.Set("Access-Control-Allow-Headers", req)
			}
			if c.credentials {
				h.Set("Access-Control-Allow-Credentials", "true")
			}
			if c.maxAgeSeconds != "" {
				h.Set("Access-Control-Max-Age", c.maxAgeSeconds)
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}

		h := w.Header()
		h.Set("Access-Control-Allow-Origin", allow)
		if c.credentials {
			h.Set("Access-Control-Allow-Credentials", "true")
		}
		if c.exposed != "" {
			h.Set("Access-Control-Expose-Headers", c.exposed)
		}
		next.ServeHTTP(w, r)
	})
}
