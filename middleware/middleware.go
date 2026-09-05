// Package middleware is servo's batteries for the emitted HTTP servers:
// ordinary graph nodes with the standard
//
//	Middleware(next http.Handler) http.Handler
//
// method, attached in the spec with servo.Use[T](selector...) like any
// middleware you write yourself. The zero-config ones (Recover, AccessLog,
// RequestID, Gzip) need nothing beyond the Use line — their constructors
// take no arguments, so resolution finds them as they are. The configurable
// ones take a *XxxConfig node that **your own provider** supplies, the same
// pattern as *servo.HTTPConfig: the values are your module's business, and
// because the config is a real Go struct built in your code, fields like
// RateLimitConfig.KeyFunc are ordinary functions.
//
// Everything here is standard library only.
package middleware

import (
	"encoding/json"
	"net/http"
)

// writeJSONError mirrors the error body the generated adapters produce, so
// a client sees one shape whether a middleware or a handler refused it.
func writeJSONError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(struct {
		Error string `json:"error"`
	}{Error: msg})
}
