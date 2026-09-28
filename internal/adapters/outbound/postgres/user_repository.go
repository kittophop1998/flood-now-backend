package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"floodnow-api/internal/domain/apperr"
	"floodnow-api/internal/domain/user"
)

type UserRepository struct {
	db *sql.DB
}

func NewUserRepository(db *sql.DB) *UserRepository {
	return &UserRepository{db: db}
}

const userColumns = `u.id, u.email, u.password_hash, u.display_name, u.created_at, u.updated_at`

func scanUser(row rowScanner) (*user.User, error) {
	var u user.User
	err := row.Scan(&u.ID, &u.Email, &u.PasswordHash, &u.DisplayName, &u.CreatedAt, &u.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

func (repo *UserRepository) Create(ctx context.Context, u *user.User) error {
	_, err := repo.db.ExecContext(ctx, `
		INSERT INTO users (id, email, password_hash, display_name, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6)`,
		u.ID, u.Email, u.PasswordHash, u.DisplayName, u.CreatedAt, u.UpdatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return apperr.Conflict("an account with this email already exists")
		}
		return fmt.Errorf("insert user: %w", err)
	}
	return nil
}

func (repo *UserRepository) GetByEmail(ctx context.Context, email string) (*user.User, error) {
	u, err := scanUser(repo.db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users u WHERE lower(u.email) = lower($1)`, email))
	if err != nil {
		return nil, fmt.Errorf("get user by email: %w", err)
	}
	return u, nil
}

func (repo *UserRepository) CreateSession(ctx context.Context, tokenHash string, userID uuid.UUID, createdAt, expiresAt time.Time) error {
	// Opportunistically drop this user's expired sessions so the table
	// doesn't grow without bound.
	if _, err := repo.db.ExecContext(ctx, `DELETE FROM user_sessions WHERE user_id = $1 AND expires_at <= $2`, userID, createdAt); err != nil {
		return fmt.Errorf("prune sessions: %w", err)
	}
	if _, err := repo.db.ExecContext(ctx,
		`INSERT INTO user_sessions (token_hash, user_id, created_at, expires_at) VALUES ($1,$2,$3,$4)`,
		tokenHash, userID, createdAt, expiresAt); err != nil {
		return fmt.Errorf("insert session: %w", err)
	}
	return nil
}

func (repo *UserRepository) UserBySession(ctx context.Context, tokenHash string, now time.Time) (*user.User, error) {
	u, err := scanUser(repo.db.QueryRowContext(ctx, `
		SELECT `+userColumns+` FROM user_sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = $1 AND s.expires_at > $2`, tokenHash, now))
	if err != nil {
		return nil, fmt.Errorf("get session user: %w", err)
	}
	return u, nil
}

func (repo *UserRepository) DeleteSession(ctx context.Context, tokenHash string) error {
	if _, err := repo.db.ExecContext(ctx, `DELETE FROM user_sessions WHERE token_hash = $1`, tokenHash); err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}
