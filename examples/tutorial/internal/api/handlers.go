package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"uuid"

	"example.com/servoorders/internal/domain"
)

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "malformed request body")
		return
	}

	token, err := s.auth.Login(r.Context(), req.Username, req.Password)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, loginResponse{Token: token})
}

func (s *Server) handleCreateOrder(w http.ResponseWriter, r *http.Request) {
	claims, _ := claimsFromContext(r.Context()) // requireAuth guarantees this is present

	var req createOrderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "malformed request body")
		return
	}

	order, err := s.orders.CreateOrder(r.Context(), claims.UserID, req.Item, req.Quantity)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, newOrderResponse(order))
}

func (s *Server) handleGetOrder(w http.ResponseWriter, r *http.Request) {
	claims, _ := claimsFromContext(r.Context())

	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid order id")
		return
	}

	order, err := s.orders.GetOrder(r.Context(), claims.UserID, id)
	if err != nil {
		writeDomainError(w, err)
		return
	}

	// Acquire, defer the release, use it. The reference lasts for this
	// call, not for the process — which is the whole difference between a
	// scope and a singleton.
	if sess, release, err := s.sessions.Acquire(r.Context()); err == nil {
		defer release()
		sess.RecordView(order.ID)
	}

	writeJSON(w, http.StatusOK, newOrderResponse(order))
}

// handleRecent reads back the per-user list the handler above builds. It
// is the only endpoint whose answer differs per user without touching
// Postgres, Redis or NATS at all — the session *is* the storage.
func (s *Server) handleRecent(w http.ResponseWriter, r *http.Request) {
	sess, release, err := s.sessions.Acquire(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "session unavailable")
		return
	}
	defer release()

	writeJSON(w, http.StatusOK, recentResponse{Recent: sess.Recent()})
}

const (
	defaultListLimit = 20
	maxListLimit     = 100
)

func (s *Server) handleListOrders(w http.ResponseWriter, r *http.Request) {
	claims, _ := claimsFromContext(r.Context())

	limit := parseIntParam(r, "limit", defaultListLimit)
	if limit <= 0 || limit > maxListLimit {
		limit = defaultListLimit
	}
	offset := max(parseIntParam(r, "offset", 0), 0)

	orders, err := s.orders.ListOrders(r.Context(), claims.UserID, limit, offset)
	if err != nil {
		writeDomainError(w, err)
		return
	}

	resp := listOrdersResponse{Orders: make([]orderResponse, len(orders))}
	for i, o := range orders {
		resp.Orders[i] = newOrderResponse(o)
	}
	writeJSON(w, http.StatusOK, resp)
}

func parseIntParam(r *http.Request, name string, fallback int) int {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return n
}

// writeDomainError is the one place a domain sentinel error becomes an HTTP
// status code — everything below the API layer only ever deals in domain
// errors, never status codes (see docs/tutorial/04-domain-layer.md).
func writeDomainError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrNotFound):
		writeError(w, http.StatusNotFound, "not found")
	case errors.Is(err, domain.ErrForbidden):
		writeError(w, http.StatusForbidden, "forbidden")
	case errors.Is(err, domain.ErrInvalidCredentials):
		writeError(w, http.StatusUnauthorized, "invalid credentials")
	case errors.Is(err, domain.ErrValidation):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "internal server error")
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, errorResponse{Error: message})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(body)
}
