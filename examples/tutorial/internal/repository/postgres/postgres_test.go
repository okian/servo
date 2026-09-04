package postgres

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
	"uuid"

	"example.com/servoorders/internal/domain"
)

// testStore skips the test unless TEST_POSTGRES_DSN is set — start
// deploy/docker-compose.yml's postgres service (or any Postgres) and export
// it to run these for real. CI sets it from a service container; see
// .github/workflows/tutorial.yml.
func testStore(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN not set; see docs/tutorial/05-repository-layer.md")
	}

	s, err := New(Config{DSN: dsn})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.Init(ctx); err != nil {
		t.Fatalf("Init (is Postgres running and reachable at %s?): %v", dsn, err)
	}
	t.Cleanup(func() { s.Stop(context.Background()) })
	return s
}

func TestCreateAndGetOrder(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	order := &domain.Order{
		ID:        uuid.New(),
		UserID:    uuid.MustParse("11111111-1111-1111-1111-111111111111"), // alice, seeded by 0002_seed_users.sql
		Item:      "widget",
		Quantity:  3,
		Status:    domain.OrderStatusPending,
		CreatedAt: time.Now().UTC().Truncate(time.Microsecond),
	}
	if err := s.Create(ctx, order); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := s.Get(ctx, order.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Item != order.Item || got.Quantity != order.Quantity || got.UserID != order.UserID {
		t.Errorf("Get returned %+v, want fields matching %+v", got, order)
	}
}

func TestGetMissingOrderReturnsErrNotFound(t *testing.T) {
	s := testStore(t)

	_, err := s.Get(context.Background(), uuid.New())
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Get on a nonexistent id = %v, want domain.ErrNotFound", err)
	}
}

func TestListByUserOrdersMostRecentFirst(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	userID := uuid.MustParse("22222222-2222-2222-2222-222222222222") // bob

	first := &domain.Order{ID: uuid.New(), UserID: userID, Item: "first", Quantity: 1, Status: domain.OrderStatusPending, CreatedAt: time.Now().UTC().Add(-time.Minute)}
	second := &domain.Order{ID: uuid.New(), UserID: userID, Item: "second", Quantity: 1, Status: domain.OrderStatusPending, CreatedAt: time.Now().UTC()}
	if err := s.Create(ctx, first); err != nil {
		t.Fatalf("Create first: %v", err)
	}
	if err := s.Create(ctx, second); err != nil {
		t.Fatalf("Create second: %v", err)
	}

	orders, err := s.ListByUser(ctx, userID, 10, 0)
	if err != nil {
		t.Fatalf("ListByUser: %v", err)
	}
	if len(orders) < 2 {
		t.Fatalf("got %d orders, want at least 2", len(orders))
	}
	if orders[0].Item != "second" {
		t.Errorf("orders[0].Item = %q, want %q (most recent first)", orders[0].Item, "second")
	}
}

func TestGetByUsernameFindsSeededUser(t *testing.T) {
	s := testStore(t)

	u, err := s.GetByUsername(context.Background(), "alice")
	if err != nil {
		t.Fatalf("GetByUsername: %v", err)
	}
	if u.Username != "alice" {
		t.Errorf("Username = %q, want %q", u.Username, "alice")
	}
	if u.PasswordHash == "" {
		t.Error("PasswordHash is empty, want the seeded bcrypt hash")
	}
}

func TestGetByUsernameUnknownReturnsErrNotFound(t *testing.T) {
	s := testStore(t)

	_, err := s.GetByUsername(context.Background(), "no-such-user")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("GetByUsername for an unknown user = %v, want domain.ErrNotFound", err)
	}
}
