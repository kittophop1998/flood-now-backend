package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"floodnow-api/internal/domain/follow"
	"floodnow-api/internal/ports"
)

type FollowRepository struct {
	db *sql.DB
}

func NewFollowRepository(db *sql.DB) *FollowRepository {
	return &FollowRepository{db: db}
}

const followColumns = `id, device_id, kind, report_id, latitude, longitude, radius_m, created_at`

func scanFollow(row rowScanner) (*follow.Follow, error) {
	var f follow.Follow
	if err := row.Scan(&f.ID, &f.DeviceID, &f.Kind, &f.ReportID, &f.Latitude, &f.Longitude, &f.RadiusM, &f.CreatedAt); err != nil {
		return nil, err
	}
	return &f, nil
}

func (repo *FollowRepository) ListByDevice(ctx context.Context, deviceID string) ([]follow.Follow, error) {
	rows, err := repo.db.QueryContext(ctx, `SELECT `+followColumns+` FROM follows WHERE device_id = $1 ORDER BY created_at DESC`, deviceID)
	if err != nil {
		return nil, fmt.Errorf("list follows: %w", err)
	}
	defer rows.Close()

	out := []follow.Follow{}
	for rows.Next() {
		f, err := scanFollow(rows)
		if err != nil {
			return nil, fmt.Errorf("scan follow: %w", err)
		}
		out = append(out, *f)
	}
	return out, rows.Err()
}

func (repo *FollowRepository) CountByDevice(ctx context.Context, deviceID string) (int, error) {
	var n int
	if err := repo.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM follows WHERE device_id = $1`, deviceID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count follows: %w", err)
	}
	return n, nil
}

func (repo *FollowRepository) FindReportFollow(ctx context.Context, deviceID string, reportID uuid.UUID) (*follow.Follow, error) {
	row := repo.db.QueryRowContext(ctx, `SELECT `+followColumns+` FROM follows WHERE device_id = $1 AND kind = 'report' AND report_id = $2`, deviceID, reportID)
	f, err := scanFollow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find report follow: %w", err)
	}
	return f, nil
}

func (repo *FollowRepository) Create(ctx context.Context, f *follow.Follow) error {
	_, err := repo.db.ExecContext(ctx, `
		INSERT INTO follows (id, device_id, kind, report_id, latitude, longitude, radius_m, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		f.ID, f.DeviceID, f.Kind, f.ReportID, f.Latitude, f.Longitude, f.RadiusM, f.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("insert follow: %w", err)
	}
	return nil
}

func (repo *FollowRepository) Delete(ctx context.Context, deviceID string, id uuid.UUID) (bool, error) {
	res, err := repo.db.ExecContext(ctx, `DELETE FROM follows WHERE id = $1 AND device_id = $2`, id, deviceID)
	if err != nil {
		return false, fmt.Errorf("delete follow: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("delete follow: %w", err)
	}
	return n > 0, nil
}

// Notifications matches report events against the device's follows:
// report follows see every event of their report after the follow began;
// area follows see "created" events inside their radius at the given
// severities. Events the device caused itself are skipped.
func (repo *FollowRepository) Notifications(ctx context.Context, q ports.NotificationQuery) ([]ports.NotificationCandidate, error) {
	var b queryBuilder
	device := b.arg(q.DeviceID)
	since := b.arg(q.Since)
	sevList := make([]string, len(q.AreaSeverities))
	for i, s := range q.AreaSeverities {
		sevList[i] = b.arg(string(s))
	}
	sevIn := "FALSE"
	if len(sevList) > 0 {
		sevIn = "r.severity IN (" + strings.Join(sevList, ", ") + ")"
	}

	query := `
		SELECT ` + reportColumns + `, e.id, e.kind, e.created_at, f.id, f.kind
		FROM follows f
		JOIN report_events e ON e.created_at > ` + since + ` AND e.created_at >= f.created_at
		JOIN reports r ON r.id = e.report_id
		WHERE f.device_id = ` + device + ` AND e.device_id IS DISTINCT FROM f.device_id AND (
			(f.kind = 'report' AND e.report_id = f.report_id)
			OR (f.kind = 'area' AND e.kind = 'created' AND ` + sevIn + ` AND ` +
		distanceSQL("f.latitude", "f.longitude") + ` <= f.radius_m)
		)
		ORDER BY e.created_at DESC, e.id DESC
		LIMIT ` + b.arg(q.Limit)

	rows, err := repo.db.QueryContext(ctx, query, b.args...)
	if err != nil {
		return nil, fmt.Errorf("query notifications: %w", err)
	}
	defer rows.Close()

	out := []ports.NotificationCandidate{}
	for rows.Next() {
		var c ports.NotificationCandidate
		r, err := scanReport(rows, &c.EventID, &c.EventKind, &c.CreatedAt, &c.FollowID, &c.FollowKind)
		if err != nil {
			return nil, fmt.Errorf("scan notification: %w", err)
		}
		c.Report = *r
		out = append(out, c)
	}
	return out, rows.Err()
}
