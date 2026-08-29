package ginapi

import (
	"net/http"
	"strconv"
	"uuid"

	"github.com/gin-gonic/gin"
)

func (s *Server) handleLogin(c *gin.Context) {
	var req loginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		abort(c, http.StatusBadRequest, "malformed request body")
		return
	}

	token, err := s.auth.Login(c.Request.Context(), req.Username, req.Password)
	if err != nil {
		abortDomainError(c, err)
		return
	}
	c.JSON(http.StatusOK, loginResponse{Token: token})
}

func (s *Server) handleCreateOrder(c *gin.Context) {
	claims := claimsFrom(c)

	var req createOrderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		abort(c, http.StatusBadRequest, "malformed request body")
		return
	}

	order, err := s.orders.CreateOrder(c.Request.Context(), claims.UserID, req.Item, req.Quantity)
	if err != nil {
		abortDomainError(c, err)
		return
	}
	c.JSON(http.StatusCreated, newOrderResponse(order))
}

func (s *Server) handleGetOrder(c *gin.Context) {
	claims := claimsFrom(c)

	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		abort(c, http.StatusBadRequest, "invalid order id")
		return
	}

	order, err := s.orders.GetOrder(c.Request.Context(), claims.UserID, id)
	if err != nil {
		abortDomainError(c, err)
		return
	}

	// Acquire, defer the release, use it. The reference lasts for this
	// call, not for the process — which is the whole difference between a
	// scope and a singleton.
	if sess, release, err := s.sessions.Acquire(c.Request.Context()); err == nil {
		defer release()
		sess.RecordView(order.ID)
	}

	c.JSON(http.StatusOK, newOrderResponse(order))
}

// handleRecent reads back the per-user list the handler above builds. It
// is the only endpoint whose answer differs per user without touching
// Postgres, Redis or NATS at all — the session *is* the storage.
func (s *Server) handleRecent(c *gin.Context) {
	sess, release, err := s.sessions.Acquire(c.Request.Context())
	if err != nil {
		abort(c, http.StatusServiceUnavailable, "session unavailable")
		return
	}
	defer release()

	c.JSON(http.StatusOK, recentResponse{Recent: sess.Recent()})
}

const (
	defaultListLimit = 20
	maxListLimit     = 100
)

func (s *Server) handleListOrders(c *gin.Context) {
	claims := claimsFrom(c)

	limit := parseIntQuery(c, "limit", defaultListLimit)
	if limit <= 0 || limit > maxListLimit {
		limit = defaultListLimit
	}
	offset := max(parseIntQuery(c, "offset", 0), 0)

	orders, err := s.orders.ListOrders(c.Request.Context(), claims.UserID, limit, offset)
	if err != nil {
		abortDomainError(c, err)
		return
	}

	resp := listOrdersResponse{Orders: make([]orderResponse, len(orders))}
	for i, o := range orders {
		resp.Orders[i] = newOrderResponse(o)
	}
	c.JSON(http.StatusOK, resp)
}

func parseIntQuery(c *gin.Context, name string, fallback int) int {
	raw := c.Query(name)
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return n
}
