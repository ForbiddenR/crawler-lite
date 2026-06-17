// Package auth provides login, JWT issuance, and password hashing.
//
// Roles are enumerated here and embedded in JWT claims so every HTTP middleware
// can authorize off the JWT alone, without hitting the database on each request.
package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
)

// Role is the authorization granularity. Three levels suffice for a small team.
type Role string

const (
	RoleAdmin  Role = "admin"
	RoleEditor Role = "editor"
	RoleViewer Role = "viewer"
)

// Service is the user-facing auth API: login + token verification.
type Service struct {
	users  UserRepository
	hasher Hasher
	jwt    *JWTIssuer
	log    *slog.Logger
}

// UserRepository is the slice of repository.UserRepo this service depends on.
// Defining the interface here, where it's USED, is idiomatic Go (consumers
// declare interfaces; producers return concrete types).
type UserRepository interface {
	GetByEmail(ctx context.Context, email string) (*User, error)
	GetByID(ctx context.Context, id int64) (*User, error)
}

type User struct {
	ID           int64
	Email        string
	PasswordHash string
	Role         Role
	DisplayName  string
	IsActive     bool
}

func NewService(users UserRepository, hasher Hasher, jwt *JWTIssuer, log *slog.Logger) *Service {
	return &Service{users: users, hasher: hasher, jwt: jwt, log: log}
}

var (
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrInactive           = errors.New("user is inactive")
)

// Login verifies a password and issues a JWT. Returns ErrInvalidCredentials for
// both unknown users and bad passwords (no enumeration).
func (s *Service) Login(ctx context.Context, email, password string) (string, *User, error) {
	u, err := s.users.GetByEmail(ctx, email)
	if err != nil {
		s.log.Info("login: user not found", "email", email)
		return "", nil, ErrInvalidCredentials
	}
	if !u.IsActive {
		return "", nil, ErrInactive
	}
	if err := s.hasher.Compare(u.PasswordHash, password); err != nil {
		s.log.Info("login: bad password", "email", email)
		return "", nil, ErrInvalidCredentials
	}
	tok, err := s.jwt.Issue(Claims{
		UserID: u.ID,
		Email:  u.Email,
		Role:   u.Role,
	})
	if err != nil {
		return "", nil, fmt.Errorf("issue: %w", err)
	}
	return tok, u, nil
}

// VerifyToken returns the claims encoded in a token, or an error if invalid.
func (s *Service) VerifyToken(token string) (Claims, error) {
	return s.jwt.Verify(token)
}

// CurrentUser fetches the User behind a token. Used by the /me endpoint.
func (s *Service) CurrentUser(ctx context.Context, token string) (*User, error) {
	c, err := s.VerifyToken(token)
	if err != nil {
		return nil, err
	}
	return s.users.GetByID(ctx, c.UserID)
}
