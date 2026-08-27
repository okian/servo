package service

import (
	"context"
	"errors"
	"fmt"

	"example.com/servoorders/auth"
	"example.com/servoorders/domain"
	"example.com/servoorders/repository"
)

type AuthService struct {
	users  repository.UserRepository
	issuer *auth.Issuer
}

func NewAuthService(users repository.UserRepository, issuer *auth.Issuer) *AuthService {
	return &AuthService{users: users, issuer: issuer}
}

// Login never distinguishes "no such user" from "wrong password" in what it
// returns — both come back as domain.ErrInvalidCredentials, so a client
// (or an attacker) can't use the login endpoint to enumerate which
// usernames exist.
func (s *AuthService) Login(ctx context.Context, username, password string) (string, error) {
	user, err := s.users.GetByUsername(ctx, username)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return "", domain.ErrInvalidCredentials
		}
		return "", fmt.Errorf("service: login: %w", err)
	}

	if err := auth.CheckPassword(user.PasswordHash, password); err != nil {
		return "", domain.ErrInvalidCredentials
	}

	token, err := s.issuer.Issue(user.ID, user.Username)
	if err != nil {
		return "", fmt.Errorf("service: login: %w", err)
	}
	return token, nil
}
