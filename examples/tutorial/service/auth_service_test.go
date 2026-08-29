package service_test

import (
	"context"
	"errors"
	"testing"
	"time"
	"uuid"

	"example.com/servoorders/auth"
	"example.com/servoorders/domain"
	"example.com/servoorders/mocks"
	"example.com/servoorders/service"
	"go.uber.org/mock/gomock"
)

func TestLoginSucceedsWithCorrectPassword(t *testing.T) {
	ctrl := gomock.NewController(t)
	users := mocks.NewMockUserRepository(ctrl)
	issuer := auth.New(&auth.Config{Secret: "test-secret", Expiry: time.Hour})

	hash, err := auth.HashPassword("password123")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	userID := uuid.New()
	users.EXPECT().GetByUsername(gomock.Any(), "alice").Return(&domain.User{ID: userID, Username: "alice", PasswordHash: hash}, nil)

	authSvc := service.NewAuthService(users, issuer)
	token, err := authSvc.Login(context.Background(), "alice", "password123")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}

	claims, err := issuer.Verify(token)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if claims.UserID != userID {
		t.Errorf("token UserID = %s, want %s", claims.UserID, userID)
	}
}

func TestLoginFailsWithWrongPassword(t *testing.T) {
	ctrl := gomock.NewController(t)
	users := mocks.NewMockUserRepository(ctrl)
	issuer := auth.New(&auth.Config{Secret: "test-secret", Expiry: time.Hour})

	hash, _ := auth.HashPassword("password123")
	users.EXPECT().GetByUsername(gomock.Any(), "alice").Return(&domain.User{Username: "alice", PasswordHash: hash}, nil)

	authSvc := service.NewAuthService(users, issuer)
	if _, err := authSvc.Login(context.Background(), "alice", "wrong-password"); !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Errorf("Login with the wrong password: err = %v, want domain.ErrInvalidCredentials", err)
	}
}

// Both an unknown username and a wrong password must return the exact same
// error, so a client can't use the response to tell which usernames exist.
func TestLoginFailsWithUnknownUsernameTheSameWayAsWrongPassword(t *testing.T) {
	ctrl := gomock.NewController(t)
	users := mocks.NewMockUserRepository(ctrl)
	issuer := auth.New(&auth.Config{Secret: "test-secret", Expiry: time.Hour})

	users.EXPECT().GetByUsername(gomock.Any(), "nobody").Return(nil, domain.ErrNotFound)

	authSvc := service.NewAuthService(users, issuer)
	if _, err := authSvc.Login(context.Background(), "nobody", "password123"); !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Errorf("Login for an unknown username: err = %v, want domain.ErrInvalidCredentials", err)
	}
}
