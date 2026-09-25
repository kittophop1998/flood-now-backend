package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"floodnow-api/internal/domain/follow"
	"floodnow-api/internal/domain/report"
	"floodnow-api/internal/ports"
)

type FollowRepository struct {
	db *sql.DB
}

func NewFollowRepository(db *sql.DB) *FollowRepository {
	return &FollowRepository{db: db}
}

const followColumns = `f.id, f.device_id, f.kind, f.report_id, f.latitude, f.longitude, f.radius_m, f.created_at,
	f.name, f.icon, f.preferred_vehicle, f.notify, f.updated_at`

func scanFollow(row rowScanner, extra ...any) (*follow.Follow, error) {
	var f follow.Follow
	dest := []any{&f.ID, &f.DeviceID, &f.Kind, &f.ReportID, &f.Latitude, &f.Longitude, &f.RadiusM, &f.CreatedAt,
		&f.Name, &f.Icon, &f.PreferredVehicle, &f.Notify, &f.UpdatedAt}
	if err := row.Scan(append(dest, extra...)...); err != nil {
		return nil, err
	}
	return &f, nil
}

func kindStrings(kinds []follow.Kind) []string {
	out := make([]string, len(kinds))
	for i, k := range kinds {
		out[i] = string(k)
	}
	return out
}

func (repo *FollowRepository) ListByDevice(ctx context.Context, deviceID string, kinds ...follow.Kind) ([]follow.Follow, error) {
	rows, err := repo.db.QueryContext(ctx, `SELECT `+followColumns+` FROM follows f WHERE f.device_id = $1 AND f.kind = ANY($2) ORDER BY f.created_at DESC`,
		deviceID, kindStrings(kinds))
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

func (repo *FollowRepository) CountByDevice(ctx context.Context, deviceID string, kinds ...follow.Kind) (int, error) {
	var n int
	if err := repo.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM follows WHERE device_id = $1 AND kind = ANY($2)`, deviceID, kindStrings(kinds)).Scan(&n); err != nil {
		return 0, fmt.Errorf("count follows: %w", err)
	}
	return n, nil
}

func (repo *FollowRepository) FindReportFollow(ctx context.Context, deviceID string, reportID uuid.UUID) (*follow.Follow, error) {
	row := repo.db.QueryRowContext(ctx, `SELECT `+followColumns+` FROM follows f WHERE f.device_id = $1 AND f.kind = 'report' AND f.report_id = $2`, deviceID, reportID)
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
		INSERT INTO follows (id, device_id, kind, report_id, latitude, longitude, radius_m, created_at,
			name, icon, preferred_vehicle, notify, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		f.ID, f.DeviceID, f.Kind, f.ReportID, f.Latitude, f.Longitude, f.RadiusM, f.CreatedAt,
		f.Name, f.Icon, f.PreferredVehicle, f.Notify, f.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("insert follow: %w", err)
	}
	return nil
}

func (repo *FollowRepository) Delete(ctx context.Context, deviceID string, id uuid.UUID, kinds ...follow.Kind) (bool, error) {
	res, err := repo.db.ExecContext(ctx, `DELETE FROM follows WHERE id = $1 AND device_id = $2 AND kind = ANY($3)`, id, deviceID, kindStrings(kinds))
	if err != nil {
		return false, fmt.Errorf("delete follow: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("delete follow: %w", err)
	}
	return n > 0, nil
}

func (repo *FollowRepository) GetPlace(ctx context.Context, deviceID string, id uuid.UUID) (*follow.Follow, error) {
	row := repo.db.QueryRowContext(ctx, `SELECT `+followColumns+` FROM follows f WHERE f.id = $1 AND f.device_id = $2 AND f.kind = 'place'`, id, deviceID)
	f, err := scanFollow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get saved place: %w", err)
	}
	return f, nil
}

func (repo *FollowRepository) UpdatePlace(ctx context.Context, f *follow.Follow) error {
	_, err := repo.db.ExecContext(ctx, `
		UPDATE follows SET name = $1, icon = $2, latitude = $3, longitude = $4, radius_m = $5,
			preferred_vehicle = $6, notify = $7, updated_at = $8
		WHERE id = $9 AND device_id = $10 AND kind = 'place'`,
		f.Name, f.Icon, f.Latitude, f.Longitude, f.RadiusM, f.PreferredVehicle, f.Notify, f.UpdatedAt, f.ID, f.DeviceID,
	)
	if err != nil {
		return fmt.Errorf("update saved place: %w", err)
	}
	return nil
}

// PlaceSummaries counts the open, visible reports inside every saved place's
// watch radius with one LATERAL query (bbox prefilter on the report
// lat/lng index, then exact haversine), instead of one query per place.
func (repo *FollowRepository) PlaceSummaries(ctx context.Context, deviceID string, severe []report.Severity, now time.Time) ([]follow.PlaceWithSummary, error) {
	var b queryBuilder
	device := b.arg(deviceID)
	nowPH := b.arg(now)
	sevPH := b.arg(severityStrings(severe))
	query := `
		SELECT ` + followColumns + `, COALESCE(s.active_count, 0), COALESCE(s.severe_count, 0), s.latest
		FROM follows f
		LEFT JOIN LATERAL (
			SELECT COUNT(*) AS active_count,
				COUNT(*) FILTER (WHERE r.severity = ANY(` + sevPH + `)) AS severe_count,
				MAX(r.updated_at) AS latest
			FROM reports r
			WHERE r.hidden_at IS NULL AND r.resolved_at IS NULL AND r.expires_at > ` + nowPH + `
				AND r.latitude BETWEEN f.latitude - f.radius_m / 111320.0 AND f.latitude + f.radius_m / 111320.0
				AND r.longitude BETWEEN f.longitude - f.radius_m / (111320.0 * GREATEST(cos(radians(f.latitude)), 0.01))
					AND f.longitude + f.radius_m / (111320.0 * GREATEST(cos(radians(f.latitude)), 0.01))
				AND ` + distanceSQL("f.latitude", "f.longitude") + ` <= f.radius_m
		) s ON true
		WHERE f.device_id = ` + device + ` AND f.kind = 'place'
		ORDER BY f.created_at`
	rows, err := repo.db.QueryContext(ctx, query, b.args...)
	if err != nil {
		return nil, fmt.Errorf("query saved places: %w", err)
	}
	defer rows.Close()

	out := []follow.PlaceWithSummary{}
	for rows.Next() {
		var p follow.PlaceWithSummary
		f, err := scanFollow(rows, &p.Summary.ActiveCount, &p.Summary.SevereCount, &p.Summary.LatestUpdateAt)
		if err != nil {
			return nil, fmt.Errorf("scan saved place: %w", err)
		}
		p.Follow = *f
		out = append(out, p)
	}
	return out, rows.Err()
}

// Notifications matches report events against the device's follows:
// report follows see every event of their report after the follow began;
// area follows see "created" events inside their radius at the given
// severities; saved places with notifications on also see "reopened".
// Events the device caused itself and hidden reports are skipped.
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
		WHERE f.device_id = ` + device + ` AND e.device_id IS DISTINCT FROM f.device_id AND r.hidden_at IS NULL AND (
			(f.kind = 'report' AND e.report_id = f.report_id)
			OR (f.kind = 'area' AND e.kind = 'created' AND ` + sevIn + ` AND ` +
		distanceSQL("f.latitude", "f.longitude") + ` <= f.radius_m)
			OR (f.kind = 'place' AND f.notify AND e.kind IN ('created', 'reopened') AND ` + sevIn + ` AND ` +
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
