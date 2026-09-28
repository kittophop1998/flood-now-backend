package auth_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	appauth "floodnow-api/internal/application/auth"
	"floodnow-api/internal/domain/apperr"
	"floodnow-api/internal/domain/user"
	"floodnow-api/internal/ports"
)

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }

type memUsers struct {
	users    map[uuid.UUID]user.User
	sessions map[string]struct {
		userID  uuid.UUID
		expires time.Time
	}
}

func newMemUsers() *memUsers {
	return &memUsers{users: map[uuid.UUID]user.User{}, sessions: map[string]struct {
		userID  uuid.UUID
		expires time.Time
	}{}}
}

func (m *memUsers) Create(_ context.Context, u *user.User) error {
	for _, existing := range m.users {
		if existing.Email == u.Email {
			return apperr.Conflict("taken")
		}
	}
	m.users[u.ID] = *u
	return nil
}
func (m *memUsers) GetByEmail(_ context.Context, email string) (*user.User, error) {
	for _, u := range m.users {
		if u.Email == email {
			return &u, nil
		}
	}
	return nil, nil
}
func (m *memUsers) CreateSession(_ context.Context, hash string, id uuid.UUID, _, expires time.Time) error {
	m.sessions[hash] = struct {
		userID  uuid.UUID
		expires time.Time
	}{id, expires}
	return nil
}
func (m *memUsers) UserBySession(_ context.Context, hash string, now time.Time) (*user.User, error) {
	s, ok := m.sessions[hash]
	if !ok || !s.expires.After(now) {
		return nil, nil
	}
	u := m.users[s.userID]
	return &u, nil
}
func (m *memUsers) DeleteSession(_ context.Context, hash string) error {
	delete(m.sessions, hash)
	return nil
}

// plainHasher keeps tests fast; the bcrypt adapter is a thin wrapper.
type plainHasher struct{}

func (plainHasher) Hash(p string) (string, error) { return "h:" + p, nil }
func (plainHasher) Compare(h, p string) bool      { return h == "h:"+p }

type claimRecorder struct {
	ports.FollowRepository
	claimed []string
}

func (c *claimRecorder) ClaimDevicePlaces(_ context.Context, deviceID string, _ uuid.UUID, _ int) error {
	c.claimed = append(c.claimed, deviceID)
	return nil
}

func assertCode(t *testing.T, err error, code apperr.Code) {
	t.Helper()
	var appErr *apperr.Error
	if !errors.As(err, &appErr) || appErr.Code != code {
		t.Fatalf("expected %s, got %v", code, err)
	}
}

func TestRegisterLoginAuthenticateLogout(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 9, 28, 8, 0, 0, 0, time.UTC)}
	claims := &claimRecorder{}
	svc := appauth.NewService(newMemUsers(), claims, plainHasher{}, clock)
	ctx := context.Background()
	const device = "12345678-aaaa-bbbb-cccc-dddddddddddd"

	s, err := svc.Register(ctx, user.RegisterInput{Email: " Somchai@Example.com ", Password: "longenough", DisplayName: " สมชาย "}, device)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if s.User.Email != "somchai@example.com" || s.User.DisplayName != "สมชาย" || s.Token == "" {
		t.Errorf("unexpected session: %+v", s.User)
	}
	if len(claims.claimed) != 1 || claims.claimed[0] != device {
		t.Errorf("device's saved places should be claimed on sign-up, got %v", claims.claimed)
	}

	_, err = svc.Register(ctx, user.RegisterInput{Email: "somchai@example.com", Password: "longenough", DisplayName: "x"}, "")
	assertCode(t, err, apperr.CodeConflict)
	_, err = svc.Register(ctx, user.RegisterInput{Email: "not-an-email", Password: "short", DisplayName: ""}, "")
	assertCode(t, err, apperr.CodeValidation)

	_, err = svc.Login(ctx, "somchai@example.com", "wrong-password", "")
	assertCode(t, err, apperr.CodeUnauthorized)
	_, err = svc.Login(ctx, "nobody@example.com", "longenough", "")
	assertCode(t, err, apperr.CodeUnauthorized)

	login, err := svc.Login(ctx, "SOMCHAI@example.com", "longenough", "")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	u, err := svc.Authenticate(ctx, login.Token)
	if err != nil || u == nil || u.ID != s.User.ID {
		t.Fatalf("authenticate: %v %v", u, err)
	}
	if u, _ := svc.Authenticate(ctx, "forged-token"); u != nil {
		t.Error("an unknown token must not authenticate")
	}

	if err := svc.Logout(ctx, login.Token); err != nil {
		t.Fatalf("logout: %v", err)
	}
	if u, _ := svc.Authenticate(ctx, login.Token); u != nil {
		t.Error("a logged-out token must not authenticate")
	}

	clock.now = clock.now.Add(user.SessionTTL + time.Minute)
	if u, _ := svc.Authenticate(ctx, s.Token); u != nil {
		t.Error("an expired session must not authenticate")
	}
}
