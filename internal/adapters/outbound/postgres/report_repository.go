package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"floodnow-api/internal/domain/report"
	"floodnow-api/internal/ports"
)

type ReportRepository struct {
	db *sql.DB
}

func NewReportRepository(db *sql.DB) *ReportRepository {
	return &ReportRepository{db: db}
}

const reportColumnsWithStats = `
	r.id, r.type, r.severity, r.latitude, r.longitude, r.water_level_cm, r.description,
	r.image_key, r.people_count, r.has_child, r.has_elderly, r.contact_phone,
	r.created_at, r.updated_at, r.last_verified_at, r.expires_at,
	COUNT(*) FILTER (WHERE c.status = 'still_active') AS still_active_count,
	COUNT(*) FILTER (WHERE c.status = 'cleared') AS cleared_count
`

func scanReportWithStats(row interface{ Scan(...any) error }) (*report.ReportWithStats, error) {
	var r report.ReportWithStats
	err := row.Scan(
		&r.ID, &r.Type, &r.Severity, &r.Latitude, &r.Longitude, &r.WaterLevelCM, &r.Description,
		&r.ImageKey, &r.PeopleCount, &r.HasChild, &r.HasElderly, &r.ContactPhone,
		&r.CreatedAt, &r.UpdatedAt, &r.LastVerifiedAt, &r.ExpiresAt,
		&r.StillActiveCount, &r.ClearedCount,
	)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

func (repo *ReportRepository) Create(ctx context.Context, r *report.Report) error {
	_, err := repo.db.ExecContext(ctx, `
		INSERT INTO reports (
			id, type, severity, latitude, longitude, water_level_cm, description,
			image_key, people_count, has_child, has_elderly, contact_phone,
			created_at, updated_at, last_verified_at, expires_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`,
		r.ID, r.Type, r.Severity, r.Latitude, r.Longitude, r.WaterLevelCM, r.Description,
		r.ImageKey, r.PeopleCount, r.HasChild, r.HasElderly, r.ContactPhone,
		r.CreatedAt, r.UpdatedAt, r.LastVerifiedAt, r.ExpiresAt,
	)
	if err != nil {
		return fmt.Errorf("insert report: %w", err)
	}
	return nil
}

func (repo *ReportRepository) GetByID(ctx context.Context, id uuid.UUID) (*report.ReportWithStats, error) {
	row := repo.db.QueryRowContext(ctx, `
		SELECT `+reportColumnsWithStats+`
		FROM reports r
		LEFT JOIN report_confirmations c ON c.report_id = r.id
		WHERE r.id = $1
		GROUP BY r.id`, id)

	r, err := scanReportWithStats(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get report: %w", err)
	}
	return r, nil
}

func (repo *ReportRepository) List(ctx context.Context, filter ports.ReportFilter) ([]report.ReportWithStats, error) {
	query := `
		SELECT ` + reportColumnsWithStats + `
		FROM reports r
		LEFT JOIN report_confirmations c ON c.report_id = r.id
		WHERE 1=1`
	args := []any{}
	argN := 0
	arg := func(v any) string {
		argN++
		args = append(args, v)
		return fmt.Sprintf("$%d", argN)
	}

	if !filter.IncludeExpired {
		query += " AND r.expires_at > now()"
	}
	if filter.BBox != nil {
		query += " AND r.latitude BETWEEN " + arg(filter.BBox.MinLat) + " AND " + arg(filter.BBox.MaxLat)
		query += " AND r.longitude BETWEEN " + arg(filter.BBox.MinLng) + " AND " + arg(filter.BBox.MaxLng)
	}
	query += " GROUP BY r.id ORDER BY r.created_at DESC"

	rows, err := repo.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list reports: %w", err)
	}
	defer rows.Close()

	var reports []report.ReportWithStats
	for rows.Next() {
		r, err := scanReportWithStats(rows)
		if err != nil {
			return nil, fmt.Errorf("scan report: %w", err)
		}
		reports = append(reports, *r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list reports: %w", err)
	}
	return reports, nil
}

func (repo *ReportRepository) Confirm(ctx context.Context, params ports.ConfirmParams) (*report.ReportWithStats, error) {
	tx, err := repo.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM reports WHERE id = $1 FOR UPDATE)`, params.ReportID).Scan(&exists); err != nil {
		return nil, fmt.Errorf("check report exists: %w", err)
	}
	if !exists {
		return nil, nil
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO report_confirmations (id, report_id, device_id, status, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$5)
		ON CONFLICT (report_id, device_id)
		DO UPDATE SET status = EXCLUDED.status, updated_at = EXCLUDED.updated_at`,
		uuid.New(), params.ReportID, params.DeviceID, params.Status, params.Now,
	); err != nil {
		return nil, fmt.Errorf("upsert confirmation: %w", err)
	}

	if params.NewExpiry != nil {
		if _, err := tx.ExecContext(ctx, `
			UPDATE reports SET last_verified_at = $1, expires_at = $2, updated_at = $1 WHERE id = $3`,
			params.Now, *params.NewExpiry, params.ReportID,
		); err != nil {
			return nil, fmt.Errorf("update report freshness: %w", err)
		}
	}

	row := tx.QueryRowContext(ctx, `
		SELECT `+reportColumnsWithStats+`
		FROM reports r
		LEFT JOIN report_confirmations c ON c.report_id = r.id
		WHERE r.id = $1
		GROUP BY r.id`, params.ReportID)

	r, err := scanReportWithStats(row)
	if err != nil {
		return nil, fmt.Errorf("reload confirmed report: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit tx: %w", err)
	}

	return r, nil
}
