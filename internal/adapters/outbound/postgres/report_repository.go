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
	"github.com/jackc/pgx/v5/pgconn"

	"floodnow-api/internal/domain/apperr"
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
	r.client_id, r.hidden_at, r.hidden_reason,
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
		&r.ClientID, &r.HiddenAt, &r.HiddenReason,
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
			created_at, updated_at, last_verified_at, stale_at, expires_at, client_id
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24)`,
		r.ID, r.Type, r.Severity, r.Latitude, r.Longitude, r.GeometryType,
		waterDepth, r.WaterLevelCM, pass[0], pass[1], pass[2], pass[3],
		r.Description, r.ImageKey, r.PeopleCount, r.HasChild, r.HasElderly, r.ContactPhone,
		r.CreatedAt, r.UpdatedAt, r.LastVerifiedAt, r.StaleAt, r.ExpiresAt, r.ClientID,
	); err != nil {
		if isUniqueViolation(err) {
			return apperr.Conflict("a report with this client_id already exists")
		}
		return fmt.Errorf("insert report: %w", err)
	}
	if err := insertEvent(ctx, tx, r.ID, report.EventCreated, nil, r.CreatedAt); err != nil {
		return err
	}
	return tx.Commit()
}

// isUniqueViolation reports a Postgres unique-constraint error.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
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

func (repo *ReportRepository) FindByClientID(ctx context.Context, clientID string) (*report.ReportWithStats, error) {
	row := repo.db.QueryRowContext(ctx, `SELECT `+reportColumns+` FROM reports r WHERE r.client_id = $1`, clientID)
	r, err := scanReport(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find report by client id: %w", err)
	}
	return r, nil
}

func (repo *ReportRepository) Events(ctx context.Context, reportID uuid.UUID) ([]ports.ReportEvent, error) {
	byReport, err := eventsFor(ctx, repo.db, []uuid.UUID{reportID}, 100)
	if err != nil {
		return nil, err
	}
	return byReport[reportID], nil
}

func uuidStrings(ids []uuid.UUID) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = id.String()
	}
	return out
}

// eventsFor loads the latest `perReport` events of several reports in one query.
func eventsFor(ctx context.Context, db *sql.DB, ids []uuid.UUID, perReport int) (map[uuid.UUID][]ports.ReportEvent, error) {
	out := map[uuid.UUID][]ports.ReportEvent{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := db.QueryContext(ctx, `
		SELECT report_id, id, kind, created_at FROM (
			SELECT report_id, id, kind, created_at,
				row_number() OVER (PARTITION BY report_id ORDER BY created_at DESC, id DESC) AS n
			FROM report_events WHERE report_id = ANY($1::uuid[])
		) e WHERE n <= $2 ORDER BY report_id, created_at DESC, id DESC`, uuidStrings(ids), perReport)
	if err != nil {
		return nil, fmt.Errorf("query report events: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			reportID uuid.UUID
			e        ports.ReportEvent
		)
		if err := rows.Scan(&reportID, &e.ID, &e.Kind, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan report event: %w", err)
		}
		out[reportID] = append(out[reportID], e)
	}
	return out, rows.Err()
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

// bboxSQL is the lat/lng range predicate (index-backed) for one box.
func (b *queryBuilder) bboxSQL(latCol, lngCol string, box ports.BBox) string {
	return "(" + latCol + " BETWEEN " + b.arg(box.MinLat) + " AND " + b.arg(box.MaxLat) +
		" AND " + lngCol + " BETWEEN " + b.arg(box.MinLng) + " AND " + b.arg(box.MaxLng) + ")"
}

func (repo *ReportRepository) List(ctx context.Context, filter ports.ReportFilter) ([]report.ReportWithStats, error) {
	var b queryBuilder
	b.add("r.hidden_at IS NULL")
	b.statuses(filter.Statuses, filter.Now)
	if filter.BBox != nil {
		b.add(b.bboxSQL("r.latitude", "r.longitude", *filter.BBox))
	}
	if len(filter.BBoxes) > 0 {
		ors := make([]string, len(filter.BBoxes))
		for i, box := range filter.BBoxes {
			ors[i] = b.bboxSQL("r.latitude", "r.longitude", box)
		}
		b.add("(" + strings.Join(ors, " OR ") + ")")
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
	b.add("r.hidden_at IS NULL")
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

// Aggregate groups reports into a lat/lng grid in SQL, so a zoomed-out map
// receives one row per occupied cell rather than every report.
func (repo *ReportRepository) Aggregate(ctx context.Context, filter ports.AggregateFilter) ([]ports.AggregateCell, error) {
	var b queryBuilder
	b.add("r.hidden_at IS NULL")
	b.statuses(filter.Statuses, filter.Now)
	b.add(b.bboxSQL("r.latitude", "r.longitude", filter.BBox))
	b.inList("r.type", typeStrings(filter.Types))
	b.inList("r.severity", severityStrings(filter.Severities))
	cell := b.arg(filter.CellDeg)

	query := `
		SELECT AVG(r.latitude), AVG(r.longitude), COUNT(*),
			COUNT(*) FILTER (WHERE r.severity IN ('high', 'critical')),
			MAX(CASE r.severity WHEN 'critical' THEN 4 WHEN 'high' THEN 3 WHEN 'moderate' THEN 2 ELSE 1 END),
			MAX(r.updated_at)
		FROM reports r` + b.whereSQL() + `
		GROUP BY floor(r.latitude / ` + cell + `), floor(r.longitude / ` + cell + `)
		ORDER BY COUNT(*) DESC
		LIMIT ` + b.arg(filter.MaxCells)
	rows, err := repo.db.QueryContext(ctx, query, b.args...)
	if err != nil {
		return nil, fmt.Errorf("aggregate reports: %w", err)
	}
	defer rows.Close()

	ranks := map[int]report.Severity{1: report.SeverityLow, 2: report.SeverityModerate, 3: report.SeverityHigh, 4: report.SeverityCritical}
	out := []ports.AggregateCell{}
	for rows.Next() {
		var (
			c    ports.AggregateCell
			rank int
		)
		if err := rows.Scan(&c.Latitude, &c.Longitude, &c.Count, &c.SevereCount, &rank, &c.LatestUpdateAt); err != nil {
			return nil, fmt.Errorf("scan aggregate cell: %w", err)
		}
		c.MaxSeverity = ranks[rank]
		out = append(out, c)
	}
	return out, rows.Err()
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
		event := report.EventConfirmed
		if params.Update != nil {
			if err := applyConditionUpdate(ctx, tx, params.ReportID, *params.Update); err != nil {
				return nil, err
			}
			event = report.EventUpdated
		}
		if err := insertEvent(ctx, tx, params.ReportID, event, &params.DeviceID, params.Now); err != nil {
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

// applyConditionUpdate overwrites the report's condition with the update's
// non-nil fields (COALESCE keeps the rest). A new water_depth also clears the
// legacy exact water_level_cm, which would otherwise contradict it.
func applyConditionUpdate(ctx context.Context, tx *sql.Tx, reportID uuid.UUID, u report.ConditionUpdate) error {
	var severity, waterDepth any
	if u.Severity != nil {
		severity = string(*u.Severity)
	}
	if u.WaterDepth != nil {
		waterDepth = string(*u.WaterDepth)
	}
	pass := passColumns(u.Passability)
	if _, err := tx.ExecContext(ctx, `
		UPDATE reports SET
			severity = COALESCE($1, severity),
			water_depth = COALESCE($2, water_depth),
			water_level_cm = CASE WHEN $2::text IS NULL THEN water_level_cm END,
			pass_walk = COALESCE($3, pass_walk),
			pass_motorcycle = COALESCE($4, pass_motorcycle),
			pass_sedan = COALESCE($5, pass_sedan),
			pass_suv_pickup = COALESCE($6, pass_suv_pickup),
			image_key = COALESCE($7, image_key)
		WHERE id = $8`,
		severity, waterDepth, pass[0], pass[1], pass[2], pass[3], u.ImageKey, reportID,
	); err != nil {
		return fmt.Errorf("update report condition: %w", err)
	}
	return nil
}
