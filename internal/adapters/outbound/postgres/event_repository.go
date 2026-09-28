package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"floodnow-api/internal/domain/event"
	"floodnow-api/internal/ports"
)

type EventRepository struct {
	db *sql.DB
}

func NewEventRepository(db *sql.DB) *EventRepository {
	return &EventRepository{db: db}
}

// The organizer's display name is joined in; no other user column is read.
const eventColumns = `e.id, e.owner_user_id, e.title, e.description, e.category, e.latitude, e.longitude,
	e.location_name, e.start_at, e.end_at, e.image_key, e.status, e.cancelled_at, e.created_at, e.updated_at,
	u.display_name`

const eventFrom = ` FROM community_events e JOIN users u ON u.id = e.owner_user_id`

func scanEvent(row rowScanner) (*event.Event, error) {
	var e event.Event
	err := row.Scan(&e.ID, &e.OwnerUserID, &e.Title, &e.Description, &e.Category, &e.Latitude, &e.Longitude,
		&e.LocationName, &e.StartAt, &e.EndAt, &e.ImageKey, &e.StoredStatus, &e.CancelledAt, &e.CreatedAt, &e.UpdatedAt,
		&e.OrganizerName)
	if err != nil {
		return nil, err
	}
	return &e, nil
}

func (repo *EventRepository) query(ctx context.Context, query string, args ...any) ([]event.Event, error) {
	rows, err := repo.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query events: %w", err)
	}
	defer rows.Close()
	out := []event.Event{}
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}

func (repo *EventRepository) List(ctx context.Context, f ports.EventFilter) ([]event.Event, error) {
	var b queryBuilder
	b.add("e.end_at > " + b.arg(f.Now))
	if f.BBox != nil {
		b.add("e.latitude BETWEEN " + b.arg(f.BBox.MinLat) + " AND " + b.arg(f.BBox.MaxLat))
		b.add("e.longitude BETWEEN " + b.arg(f.BBox.MinLng) + " AND " + b.arg(f.BBox.MaxLng))
	}
	return repo.query(ctx, `SELECT `+eventColumns+eventFrom+b.whereSQL()+
		` ORDER BY e.start_at, e.id LIMIT `+b.arg(f.Limit), b.args...)
}

func (repo *EventRepository) ListByOwner(ctx context.Context, ownerID uuid.UUID, limit int) ([]event.Event, error) {
	return repo.query(ctx, `SELECT `+eventColumns+eventFrom+` WHERE e.owner_user_id = $1 ORDER BY e.created_at DESC LIMIT $2`, ownerID, limit)
}

func (repo *EventRepository) CountUpcomingByOwner(ctx context.Context, ownerID uuid.UUID, now time.Time) (int, error) {
	var n int
	if err := repo.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM community_events WHERE owner_user_id = $1 AND status = 'active' AND end_at > $2`, ownerID, now,
	).Scan(&n); err != nil {
		return 0, fmt.Errorf("count events: %w", err)
	}
	return n, nil
}

func (repo *EventRepository) Get(ctx context.Context, id uuid.UUID) (*event.Event, error) {
	e, err := scanEvent(repo.db.QueryRowContext(ctx, `SELECT `+eventColumns+eventFrom+` WHERE e.id = $1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get event: %w", err)
	}
	return e, nil
}

func (repo *EventRepository) Create(ctx context.Context, e *event.Event) error {
	_, err := repo.db.ExecContext(ctx, `
		INSERT INTO community_events (id, owner_user_id, title, description, category, latitude, longitude,
			location_name, start_at, end_at, image_key, status, cancelled_at, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`,
		e.ID, e.OwnerUserID, e.Title, e.Description, e.Category, e.Latitude, e.Longitude,
		e.LocationName, e.StartAt, e.EndAt, e.ImageKey, e.StoredStatus, e.CancelledAt, e.CreatedAt, e.UpdatedAt)
	if err != nil {
		return fmt.Errorf("insert event: %w", err)
	}
	return nil
}

func (repo *EventRepository) Update(ctx context.Context, e *event.Event) error {
	_, err := repo.db.ExecContext(ctx, `
		UPDATE community_events SET title = $2, description = $3, category = $4, latitude = $5, longitude = $6,
			location_name = $7, start_at = $8, end_at = $9, image_key = $10, status = $11, cancelled_at = $12, updated_at = $13
		WHERE id = $1`,
		e.ID, e.Title, e.Description, e.Category, e.Latitude, e.Longitude,
		e.LocationName, e.StartAt, e.EndAt, e.ImageKey, e.StoredStatus, e.CancelledAt, e.UpdatedAt)
	if err != nil {
		return fmt.Errorf("update event: %w", err)
	}
	return nil
}

func (repo *EventRepository) Delete(ctx context.Context, id uuid.UUID) error {
	if _, err := repo.db.ExecContext(ctx, `DELETE FROM community_events WHERE id = $1`, id); err != nil {
		return fmt.Errorf("delete event: %w", err)
	}
	return nil
}
