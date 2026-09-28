// Package user holds FloodNow's minimal account model: an email/password
// login with a public display name. Accounts are optional — the app is
// guest-first; signing in only unlocks interactions (reactions, saved
// places, SOS, community events) and ownership of that content.
package user

import (
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"floodnow-api/internal/domain/apperr"
)

// Password length bounds. 72 bytes is bcrypt's input limit.
const (
	MinPasswordLen = 8
	MaxPasswordLen = 72
)

// SessionTTL is how long a sign-in stays valid.
const SessionTTL = 30 * 24 * time.Hour

type User struct {
	ID           uuid.UUID
	Email        string
	PasswordHash string
	DisplayName  string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// NormalizeEmail is the canonical form emails are stored and compared in.
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

type RegisterInput struct {
	Email       string
	Password    string
	DisplayName string
}

func (in RegisterInput) Validate() error {
	fields := map[string]string{}
	email := NormalizeEmail(in.Email)
	if addr, err := mail.ParseAddress(email); err != nil || addr.Address != email || len(email) > 254 {
		fields["email"] = "must be a valid email address"
	}
	if len(in.Password) < MinPasswordLen || len(in.Password) > MaxPasswordLen {
		fields["password"] = "must be between 8 and 72 characters"
	}
	if n := utf8.RuneCountInString(strings.TrimSpace(in.DisplayName)); n < 1 || n > 60 {
		fields["display_name"] = "must be between 1 and 60 characters"
	}
	if len(fields) > 0 {
		return apperr.Validation("registration is invalid", fields)
	}
	return nil
}
