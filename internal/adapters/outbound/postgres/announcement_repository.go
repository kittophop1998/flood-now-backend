package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"floodnow-api/internal/domain/announcement"
	"floodnow-api/internal/ports"
)

type AnnouncementRepository struct {
	db *sql.DB
}

func NewAnnouncementRepository(db *sql.DB) *AnnouncementRepository {
	return &AnnouncementRepository{db: db}
}

const announcementColumns = `a.id, a.title, a.body, a.type, a.severity, a.source_name, a.source_url,
	a.latitude, a.longitude, a.radius_m, a.starts_at, a.ends_at, a.published_at, a.created_at, a.updated_at`

func scanAnnouncement(row rowScanner) (*announcement.Announcement, error) {
	var a announcement.Announcement
	if err := row.Scan(&a.ID, &a.Title, &a.Body, &a.Type, &a.Severity, &a.SourceName, &a.SourceURL,
		&a.Latitude, &a.Longitude, &a.RadiusM, &a.StartsAt, &a.EndsAt, &a.PublishedAt, &a.CreatedAt, &a.UpdatedAt); err != nil {
		return nil, err
	}
	return &a, nil
}

func (repo *AnnouncementRepository) List(ctx context.Context, f ports.AnnouncementFilter) ([]announcement.Announcement, error) {
	var b queryBuilder
	if !f.IncludeDrafts {
		now := b.arg(f.Now)
		b.add("a.published_at IS NOT NULL AND a.starts_at <= " + now)
		if f.IncludeExpired {
			b.add("(a.ends_at IS NULL OR a.ends_at > " + b.arg(f.ExpiredSince) + ")")
		} else {
			b.add("(a.ends_at IS NULL OR a.ends_at > " + now + ")")
		}
	}
	if f.BBox != nil {
		// Announcements without a location apply everywhere. With a location,
		// the point padded by its radius (≤ 200 km, ~1.8°) must touch the box;
		// the constant pad keeps the predicate index-friendly, the exact
		// radius check trims it.
		const padDeg = 1.8
		box := *f.BBox
		wide := ports.BBox{MinLat: box.MinLat - padDeg, MaxLat: box.MaxLat + padDeg, MinLng: box.MinLng - padDeg, MaxLng: box.MaxLng + padDeg}
		b.add("(a.latitude IS NULL OR (" + b.bboxSQL("a.latitude", "a.longitude", wide) + ` AND
			a.latitude + COALESCE(a.radius_m, 0) / 111320.0 >= ` + b.arg(box.MinLat) + ` AND
			a.latitude - COALESCE(a.radius_m, 0) / 111320.0 <= ` + b.arg(box.MaxLat) + ` AND
			a.longitude + COALESCE(a.radius_m, 0) / (111320.0 * GREATEST(cos(radians(a.latitude)), 0.01)) >= ` + b.arg(box.MinLng) + ` AND
			a.longitude - COALESCE(a.radius_m, 0) / (111320.0 * GREATEST(cos(radians(a.latitude)), 0.01)) <= ` + b.arg(box.MaxLng) + `))`)
	}
	query := `SELECT ` + announcementColumns + ` FROM announcements a` + b.whereSQL() + `
		ORDER BY CASE a.severity WHEN 'critical' THEN 4 WHEN 'high' THEN 3 WHEN 'moderate' THEN 2 ELSE 1 END DESC,
			a.starts_at DESC
		LIMIT ` + b.arg(f.Limit)
	rows, err := repo.db.QueryContext(ctx, query, b.args...)
	if err != nil {
		return nil, fmt.Errorf("list announcements: %w", err)
	}
	defer rows.Close()
	out := []announcement.Announcement{}
	for rows.Next() {
		a, err := scanAnnouncement(rows)
		if err != nil {
			return nil, fmt.Errorf("scan announcement: %w", err)
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

func (repo *AnnouncementRepository) Get(ctx context.Context, id uuid.UUID) (*announcement.Announcement, error) {
	a, err := scanAnnouncement(repo.db.QueryRowContext(ctx, `SELECT `+announcementColumns+` FROM announcements a WHERE a.id = $1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get announcement: %w", err)
	}
	return a, nil
}

func (repo *AnnouncementRepository) Create(ctx context.Context, a *announcement.Announcement) error {
	_, err := repo.db.ExecContext(ctx, `
		INSERT INTO announcements (id, title, body, type, severity, source_name, source_url, latitude, longitude, radius_m,
			starts_at, ends_at, published_at, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`,
		a.ID, a.Title, a.Body, a.Type, a.Severity, a.SourceName, a.SourceURL, a.Latitude, a.Longitude, a.RadiusM,
		a.StartsAt, a.EndsAt, a.PublishedAt, a.CreatedAt, a.UpdatedAt)
	if err != nil {
		return fmt.Errorf("insert announcement: %w", err)
	}
	return nil
}

func (repo *AnnouncementRepository) Update(ctx context.Context, a *announcement.Announcement) error {
	_, err := repo.db.ExecContext(ctx, `
		UPDATE announcements SET title = $1, body = $2, type = $3, severity = $4, source_name = $5, source_url = $6,
			latitude = $7, longitude = $8, radius_m = $9, starts_at = $10, ends_at = $11, published_at = $12, updated_at = $13
		WHERE id = $14`,
		a.Title, a.Body, a.Type, a.Severity, a.SourceName, a.SourceURL, a.Latitude, a.Longitude, a.RadiusM,
		a.StartsAt, a.EndsAt, a.PublishedAt, a.UpdatedAt, a.ID)
	if err != nil {
		return fmt.Errorf("update announcement: %w", err)
	}
	return nil
}

func (repo *AnnouncementRepository) Delete(ctx context.Context, id uuid.UUID) (bool, error) {
	res, err := repo.db.ExecContext(ctx, `DELETE FROM announcements WHERE id = $1`, id)
	if err != nil {
		return false, fmt.Errorf("delete announcement: %w", err)
	}
	n, err := res.RowsAffected()
	return n > 0, err
}
