package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

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

// Confirmation counts are correlated subqueries (backed by
// idx_report_confirmations_report_id) rather than a JOIN + GROUP BY, so the
// same column list composes with distance ordering and LIMIT.
const reportColumns = `
	r.id, r.type, r.severity, r.latitude, r.longitude, r.geometry_type,
	r.water_depth, r.water_level_cm, r.pass_walk, r.pass_motorcycle, r.pass_sedan, r.pass_suv_pickup,
	r.description, r.image_key, r.people_count, r.has_child, r.has_elderly, r.contact_phone,
	r.created_at, r.updated_at, r.last_verified_at, r.stale_at, r.expires_at, r.resolved_at,
	(SELECT COUNT(*) FROM report_confirmations c WHERE c.report_id = r.id AND c.status = 'still_active') AS still_active_count,
	(SELECT COUNT(*) FROM report_confirmations c WHERE c.report_id = r.id AND c.status = 'cleared') AS cleared_count
`

type rowScanner interface{ Scan(...any) error }

func scanReport(row rowScanner, extra ...any) (*report.ReportWithStats, error) {
	var (
		r                      report.ReportWithStats
		waterDepth             sql.NullString
		walk, moto, sedan, suv sql.NullString
	)
	dest := []any{
		&r.ID, &r.Type, &r.Severity, &r.Latitude, &r.Longitude, &r.GeometryType,
		&waterDepth, &r.WaterLevelCM, &walk, &moto, &sedan, &suv,
		&r.Description, &r.ImageKey, &r.PeopleCount, &r.HasChild, &r.HasElderly, &r.ContactPhone,
		&r.CreatedAt, &r.UpdatedAt, &r.LastVerifiedAt, &r.StaleAt, &r.ExpiresAt, &r.ResolvedAt,
		&r.StillActiveCount, &r.ClearedCount,
	}
	if err := row.Scan(append(dest, extra...)...); err != nil {
		return nil, err
	}
	if waterDepth.Valid {
		d := report.WaterDepth(waterDepth.String)
		r.WaterDepth = &d
	}
	if walk.Valid || moto.Valid || sedan.Valid || suv.Valid {
		r.Passability = &report.Passability{
			Walk:       passLevel(walk),
			Motorcycle: passLevel(moto),
			Sedan:      passLevel(sedan),
			SUVPickup:  passLevel(suv),
		}
	}
	return &r, nil
}

func passLevel(v sql.NullString) report.PassLevel {
	if !v.Valid {
		return report.PassUnknown
	}
	return report.PassLevel(v.String)
}

func passColumns(p *report.Passability) [4]any {
	if p == nil {
		return [4]any{nil, nil, nil, nil}
	}
	return [4]any{string(p.Walk), string(p.Motorcycle), string(p.Sedan), string(p.SUVPickup)}
}

func (repo *ReportRepository) Create(ctx context.Context, r *report.Report) error {
	tx, err := repo.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	var waterDepth any
	if r.WaterDepth != nil {
		waterDepth = string(*r.WaterDepth)
	}
	pass := passColumns(r.Passability)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO reports (
			id, type, severity, latitude, longitude, geometry_type,
			water_depth, water_level_cm, pass_walk, pass_motorcycle, pass_sedan, pass_suv_pickup,
			description, image_key, people_count, has_child, has_elderly, contact_phone,
			created_at, updated_at, last_verified_at, stale_at, expires_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23)`,
		r.ID, r.Type, r.Severity, r.Latitude, r.Longitude, r.GeometryType,
		waterDepth, r.WaterLevelCM, pass[0], pass[1], pass[2], pass[3],
		r.Description, r.ImageKey, r.PeopleCount, r.HasChild, r.HasElderly, r.ContactPhone,
		r.CreatedAt, r.UpdatedAt, r.LastVerifiedAt, r.StaleAt, r.ExpiresAt,
	); err != nil {
		return fmt.Errorf("insert report: %w", err)
	}
	if err := insertEvent(ctx, tx, r.ID, report.EventCreated, nil, r.CreatedAt); err != nil {
		return err
	}
	return tx.Commit()
}

func insertEvent(ctx context.Context, tx *sql.Tx, reportID uuid.UUID, kind report.EventKind, deviceID *string, at time.Time) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO report_events (report_id, kind, device_id, created_at) VALUES ($1, $2, $3, $4)`, reportID, kind, deviceID, at); err != nil {
		return fmt.Errorf("insert report event: %w", err)
	}
	return nil
}

func (repo *ReportRepository) GetByID(ctx context.Context, id uuid.UUID) (*report.ReportWithStats, error) {
	return getByID(ctx, repo.db, id)
}

type queryRower interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func getByID(ctx context.Context, q queryRower, id uuid.UUID) (*report.ReportWithStats, error) {
	row := q.QueryRowContext(ctx, `SELECT `+reportColumns+` FROM reports r WHERE r.id = $1`, id)
	r, err := scanReport(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get report: %w", err)
	}
	return r, nil
}

// queryBuilder accumulates WHERE clauses with numbered placeholders.
type queryBuilder struct {
	where []string
	args  []any
}

func (b *queryBuilder) arg(v any) string {
	b.args = append(b.args, v)
	return fmt.Sprintf("$%d", len(b.args))
}

func (b *queryBuilder) add(clause string) { b.where = append(b.where, clause) }

func (b *queryBuilder) whereSQL() string {
	if len(b.where) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(b.where, " AND ")
}

func (b *queryBuilder) inList(column string, values []string) {
	if len(values) == 0 {
		return
	}
	ph := make([]string, len(values))
	for i, v := range values {
		ph[i] = b.arg(v)
	}
	b.add(column + " IN (" + strings.Join(ph, ", ") + ")")
}

// statuses translates lifecycle states (see report.Report.Status) into
// predicates over the stored timestamps.
func (b *queryBuilder) statuses(statuses []report.Status, now time.Time) {
	if len(statuses) == 0 {
		return
	}
	n := b.arg(now)
	var ors []string
	for _, s := range statuses {
		switch s {
		case report.StatusActive:
			ors = append(ors, "(r.resolved_at IS NULL AND r.stale_at > "+n+" AND r.expires_at > "+n+")")
		case report.StatusPossiblyStale:
			ors = append(ors, "(r.resolved_at IS NULL AND r.stale_at <= "+n+" AND r.expires_at > "+n+")")
		case report.StatusExpired:
			ors = append(ors, "(r.resolved_at IS NULL AND r.expires_at <= "+n+")")
		case report.StatusResolved:
			ors = append(ors, "r.resolved_at IS NOT NULL")
		}
	}
	b.add("(" + strings.Join(ors, " OR ") + ")")
}

func typeStrings(ts []report.Type) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = string(t)
	}
	return out
}

func severityStrings(ss []report.Severity) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = string(s)
	}
	return out
}

func (repo *ReportRepository) List(ctx context.Context, filter ports.ReportFilter) ([]report.ReportWithStats, error) {
	var b queryBuilder
	b.statuses(filter.Statuses, filter.Now)
	if filter.BBox != nil {
		b.add("r.latitude BETWEEN " + b.arg(filter.BBox.MinLat) + " AND " + b.arg(filter.BBox.MaxLat))
		b.add("r.longitude BETWEEN " + b.arg(filter.BBox.MinLng) + " AND " + b.arg(filter.BBox.MaxLng))
	}
	b.inList("r.type", typeStrings(filter.Types))
	b.inList("r.severity", severityStrings(filter.Severities))
	if filter.UpdatedSince != nil {
		b.add("r.updated_at >= " + b.arg(*filter.UpdatedSince))
	}

	query := `SELECT ` + reportColumns + ` FROM reports r` + b.whereSQL() + ` ORDER BY r.last_verified_at DESC`
	if filter.Limit > 0 {
		query += " LIMIT " + b.arg(filter.Limit)
	}
	return repo.queryReports(ctx, query, b.args, false)
}

// distanceSQL is the haversine great-circle distance in meters between the
// report and a point. Plain SQL trig — no PostGIS (see docs/architecture.md).
func distanceSQL(latPH, lngPH string) string {
	return fmt.Sprintf(`(2 * 6371000 * asin(sqrt(
		power(sin(radians(r.latitude - %[1]s) / 2), 2) +
		cos(radians(%[1]s)) * cos(radians(r.latitude)) * power(sin(radians(r.longitude - %[2]s) / 2), 2)
	)))`, latPH, lngPH)
}

func (repo *ReportRepository) Nearby(ctx context.Context, filter ports.NearbyFilter) ([]report.ReportWithStats, error) {
	var b queryBuilder
	b.statuses(filter.Statuses, filter.Now)

	// Bounding-box prefilter so the lat/lng index narrows rows before the
	// exact distance check.
	dLat := filter.RadiusM / 111_320
	dLng := filter.RadiusM / (111_320 * math.Max(math.Cos(filter.Latitude*math.Pi/180), 0.01))
	b.add("r.latitude BETWEEN " + b.arg(filter.Latitude-dLat) + " AND " + b.arg(filter.Latitude+dLat))
	b.add("r.longitude BETWEEN " + b.arg(filter.Longitude-dLng) + " AND " + b.arg(filter.Longitude+dLng))
	b.inList("r.type", typeStrings(filter.Types))

	dist := distanceSQL(b.arg(filter.Latitude), b.arg(filter.Longitude))
	b.add(dist + " <= " + b.arg(filter.RadiusM))

	order := "distance_m ASC"
	switch filter.Sort {
	case ports.SortRecent:
		order = "r.updated_at DESC, distance_m ASC"
	case ports.SortSeverity:
		order = `CASE r.severity WHEN 'critical' THEN 4 WHEN 'high' THEN 3 WHEN 'moderate' THEN 2 ELSE 1 END DESC, distance_m ASC`
	}

	query := `SELECT ` + reportColumns + `, ` + dist + ` AS distance_m FROM reports r` + b.whereSQL() +
		` ORDER BY ` + order + ` LIMIT ` + b.arg(filter.Limit)
	return repo.queryReports(ctx, query, b.args, true)
}

func (repo *ReportRepository) queryReports(ctx context.Context, query string, args []any, withDistance bool) ([]report.ReportWithStats, error) {
	rows, err := repo.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query reports: %w", err)
	}
	defer rows.Close()

	reports := []report.ReportWithStats{}
	for rows.Next() {
		var (
			r    *report.ReportWithStats
			dist float64
		)
		if withDistance {
			r, err = scanReport(rows, &dist)
			if r != nil {
				r.DistanceM = &dist
			}
		} else {
			r, err = scanReport(rows)
		}
		if err != nil {
			return nil, fmt.Errorf("scan report: %w", err)
		}
		reports = append(reports, *r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query reports: %w", err)
	}
	return reports, nil
}

func (repo *ReportRepository) Confirm(ctx context.Context, params ports.ConfirmParams) (*report.ReportWithStats, error) {
	tx, err := repo.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	// Lock the report row so concurrent confirmations serialize and the
	// resolution decision below sees consistent counts.
	var resolvedAt *time.Time
	err = tx.QueryRowContext(ctx, `SELECT resolved_at FROM reports WHERE id = $1 FOR UPDATE`, params.ReportID).Scan(&resolvedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("lock report: %w", err)
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

	if params.Refresh != nil {
		if _, err := tx.ExecContext(ctx, `
			UPDATE reports SET last_verified_at = $1, stale_at = $2, expires_at = $3 WHERE id = $4`,
			params.Now, params.Refresh.StaleAt, params.Refresh.ExpiresAt, params.ReportID,
		); err != nil {
			return nil, fmt.Errorf("update report freshness: %w", err)
		}
		if err := insertEvent(ctx, tx, params.ReportID, report.EventConfirmed, &params.DeviceID, params.Now); err != nil {
			return nil, err
		}
	}

	var stillActive, cleared int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FILTER (WHERE status = 'still_active'), COUNT(*) FILTER (WHERE status = 'cleared')
		FROM report_confirmations WHERE report_id = $1`, params.ReportID,
	).Scan(&stillActive, &cleared); err != nil {
		return nil, fmt.Errorf("count confirmations: %w", err)
	}

	nextResolvedAt := params.Policy.NextResolvedAt(resolvedAt, stillActive, cleared, params.Now)
	if _, err := tx.ExecContext(ctx, `UPDATE reports SET resolved_at = $1, updated_at = $2 WHERE id = $3`,
		nextResolvedAt, params.Now, params.ReportID,
	); err != nil {
		return nil, fmt.Errorf("update report resolution: %w", err)
	}
	if ev := report.ResolutionEvent(resolvedAt, nextResolvedAt); ev != "" {
		if err := insertEvent(ctx, tx, params.ReportID, ev, &params.DeviceID, params.Now); err != nil {
			return nil, err
		}
	}

	r, err := getByID(ctx, tx, params.ReportID)
	if err != nil {
		return nil, fmt.Errorf("reload confirmed report: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit tx: %w", err)
	}
	return r, nil
}
