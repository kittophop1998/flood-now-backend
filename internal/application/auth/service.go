// Package auth contains the sign-up / sign-in use cases and resolves a
// session token to its user. Sessions are opaque random tokens (256 bits)
// the HTTP adapter keeps in an HttpOnly cookie; only their SHA-256 is
// stored, so a database leak doesn't leak live sessions.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"time"

	"github.com/google/uuid"

	"floodnow-api/internal/domain/apperr"
	domainfollow "floodnow-api/internal/domain/follow"
	"floodnow-api/internal/domain/user"
	"floodnow-api/internal/ports"
)

type Service struct {
	users   ports.UserRepository
	follows ports.FollowRepository
	hasher  ports.PasswordHasher
	clock   ports.Clock
}

func NewService(users ports.UserRepository, follows ports.FollowRepository, hasher ports.PasswordHasher, clock ports.Clock) *Service {
	return &Service{users: users, follows: follows, hasher: hasher, clock: clock}
}

// Session is a signed-in user plus the raw session token (it only ever goes
// into the session cookie) and when it expires.
type Session struct {
	User      user.User
	Token     string
	ExpiresAt time.Time
}

// Register creates an account and signs it in. deviceID (optional) is the
// anonymous device the person used so far: saved places it made before
// accounts existed move to the new account. previousToken (optional) is the
// session the browser held before; it is revoked, so signing in always
// yields a fresh session and never keeps using one fixed beforehand.
func (s *Service) Register(ctx context.Context, in user.RegisterInput, deviceID, previousToken string) (*Session, error) {
	if err := in.Validate(); err != nil {
		return nil, err
	}
	hash, err := s.hasher.Hash(in.Password)
	if err != nil {
		return nil, err
	}
	now := s.clock.Now()
	u := &user.User{
		ID:           uuid.New(),
		Email:        user.NormalizeEmail(in.Email),
		PasswordHash: hash,
		DisplayName:  strings.TrimSpace(in.DisplayName),
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := s.users.Create(ctx, u); err != nil {
		return nil, err // CONFLICT when the email is taken
	}
	return s.startSession(ctx, u, deviceID, previousToken)
}

// Login checks the password and starts a new session (revoking
// previousToken, as for Register). Wrong email and wrong password get the
// same answer.
func (s *Service) Login(ctx context.Context, email, password, deviceID, previousToken string) (*Session, error) {
	u, err := s.users.GetByEmail(ctx, user.NormalizeEmail(email))
	if err != nil {
		return nil, err
	}
	if u == nil || len(password) > user.MaxPasswordLen || !s.hasher.Compare(u.PasswordHash, password) {
		return nil, apperr.Unauthorized("email or password is incorrect")
	}
	return s.startSession(ctx, u, deviceID, previousToken)
}

// Logout ends the session behind token (a no-op if it's already gone).
func (s *Service) Logout(ctx context.Context, token string) error {
	return s.users.DeleteSession(ctx, hashToken(token))
}

// Authenticate resolves a session token to its user; nil when the token is
// unknown, revoked or expired.
func (s *Service) Authenticate(ctx context.Context, token string) (*user.User, error) {
	if token == "" {
		return nil, nil
	}
	return s.users.UserBySession(ctx, hashToken(token), s.clock.Now())
}

func (s *Service) startSession(ctx context.Context, u *user.User, deviceID, previousToken string) (*Session, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	now := s.clock.Now()
	expiresAt := now.Add(user.SessionTTL)
	if err := s.users.CreateSession(ctx, hashToken(token), u.ID, now, expiresAt); err != nil {
		return nil, err
	}
	if previousToken != "" {
		if err := s.users.DeleteSession(ctx, hashToken(previousToken)); err != nil {
			return nil, err
		}
	}
	if domainfollow.ValidateDeviceID(deviceID) == nil {
		if err := s.follows.ClaimDevicePlaces(ctx, deviceID, u.ID, domainfollow.MaxPlacesPerUser); err != nil {
			return nil, err
		}
	}
	return &Session{User: *u, Token: token, ExpiresAt: expiresAt}, nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
