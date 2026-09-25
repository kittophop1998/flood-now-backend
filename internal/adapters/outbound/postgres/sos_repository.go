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

	"floodnow-api/internal/domain/apperr"
	"floodnow-api/internal/domain/sos"
	"floodnow-api/internal/ports"
)

type SOSRepository struct {
	db *sql.DB
}

func NewSOSRepository(db *sql.DB) *SOSRepository {
	return &SOSRepository{db: db}
}

const sosColumns = `s.id, s.device_id, s.client_id, s.type, s.description, s.latitude, s.longitude,
	s.people_count, s.contact_phone, s.status, s.helper_device_id, s.created_at, s.updated_at, s.closed_at`

func scanSOS(row rowScanner, extra ...any) (*sos.Request, error) {
	var r sos.Request
	dest := []any{&r.ID, &r.DeviceID, &r.ClientID, &r.Type, &r.Description, &r.Latitude, &r.Longitude,
		&r.PeopleCount, &r.ContactPhone, &r.Status, &r.HelperDeviceID, &r.CreatedAt, &r.UpdatedAt, &r.ClosedAt}
	if err := row.Scan(append(dest, extra...)...); err != nil {
		return nil, err
	}
	return &r, nil
}

func insertSOSEvent(ctx context.Context, tx *sql.Tx, id uuid.UUID, status sos.Status, actor sos.Role, at time.Time) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO sos_events (sos_id, status, actor, created_at) VALUES ($1, $2, $3, $4)`, id, status, actor, at); err != nil {
		return fmt.Errorf("insert sos event: %w", err)
	}
	return nil
}

func (repo *SOSRepository) Create(ctx context.Context, r *sos.Request) error {
	tx, err := repo.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO sos_requests (id, device_id, client_id, type, description, latitude, longitude,
			people_count, contact_phone, status, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$11)`,
		r.ID, r.DeviceID, r.ClientID, r.Type, r.Description, r.Latitude, r.Longitude,
		r.PeopleCount, r.ContactPhone, r.Status, r.CreatedAt,
	); err != nil {
		if isUniqueViolation(err) {
			return apperr.Conflict("you already have an open SOS request; cancel it before sending a new one")
		}
		return fmt.Errorf("insert sos: %w", err)
	}
	if err := insertSOSEvent(ctx, tx, r.ID, r.Status, sos.RoleRequester, r.CreatedAt); err != nil {
		return err
	}
	return tx.Commit()
}

func (repo *SOSRepository) Get(ctx context.Context, id uuid.UUID) (*sos.Request, error) {
	r, err := scanSOS(repo.db.QueryRowContext(ctx, `SELECT `+sosColumns+` FROM sos_requests s WHERE s.id = $1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get sos: %w", err)
	}
	return r, nil
}

func (repo *SOSRepository) FindByClientID(ctx context.Context, deviceID, clientID string) (*sos.Request, error) {
	r, err := scanSOS(repo.db.QueryRowContext(ctx, `SELECT `+sosColumns+` FROM sos_requests s WHERE s.device_id = $1 AND s.client_id = $2`, deviceID, clientID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find sos by client id: %w", err)
	}
	return r, nil
}

func (repo *SOSRepository) Events(ctx context.Context, id uuid.UUID) ([]sos.Event, error) {
	rows, err := repo.db.QueryContext(ctx, `SELECT status, actor, created_at FROM sos_events WHERE sos_id = $1 ORDER BY created_at, id`, id)
	if err != nil {
		return nil, fmt.Errorf("query sos events: %w", err)
	}
	defer rows.Close()
	out := []sos.Event{}
	for rows.Next() {
		var e sos.Event
		if err := rows.Scan(&e.Status, &e.Actor, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan sos event: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (repo *SOSRepository) ListForDevice(ctx context.Context, deviceID string, limit int) ([]sos.Request, error) {
	rows, err := repo.db.QueryContext(ctx, `
		SELECT `+sosColumns+` FROM sos_requests s
		WHERE s.device_id = $1 OR s.helper_device_id = $1
		ORDER BY (s.status IN ('completed', 'cancelled')), s.created_at DESC
		LIMIT $2`, deviceID, limit)
	if err != nil {
		return nil, fmt.Errorf("list sos: %w", err)
	}
	defer rows.Close()
	out := []sos.Request{}
	for rows.Next() {
		r, err := scanSOS(rows)
		if err != nil {
			return nil, fmt.Errorf("scan sos: %w", err)
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// Transition is a compare-and-set on the status (and assignment, for helper
// actions): concurrent changes make it a no-op instead of a lost update.
func (repo *SOSRepository) Transition(ctx context.Context, t ports.SOSTransition) (bool, error) {
	tx, err := repo.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	var b queryBuilder
	set := "status = " + b.arg(t.To) + ", updated_at = " + b.arg(t.Now)
	switch {
	case t.To == sos.StatusWaiting:
		set += ", helper_device_id = NULL" // helper withdrew: back to the pool
	case t.To.IsClosed():
		set += ", closed_at = " + b.arg(t.Now)
	}
	where := "id = " + b.arg(t.ID) + " AND status = " + b.arg(t.From)
	if t.Actor == sos.RoleHelper {
		if t.HelperDeviceID == nil {
			return false, fmt.Errorf("helper transition without helper device")
		}
		where += " AND helper_device_id = " + b.arg(*t.HelperDeviceID)
	}
	res, err := tx.ExecContext(ctx, `UPDATE sos_requests SET `+set+` WHERE `+where, b.args...)
	if err != nil {
		return false, fmt.Errorf("update sos status: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return false, nil
	}
	if err := insertSOSEvent(ctx, tx, t.ID, t.To, t.Actor, t.Now); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// Accept locks the helper's row so one helper's parallel accepts serialize
// on the active-assignment cap, then claims the request only if it is still
// waiting — a second helper racing for it updates zero rows.
func (repo *SOSRepository) Accept(ctx context.Context, id uuid.UUID, helperDeviceID string, maxActive int, now time.Time) (bool, error) {
	tx, err := repo.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	var active bool
	err = tx.QueryRowContext(ctx, `SELECT active FROM helpers WHERE device_id = $1 FOR UPDATE`, helperDeviceID).Scan(&active)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !active) {
		return false, apperr.Conflict("turn on helper mode first")
	}
	if err != nil {
		return false, fmt.Errorf("lock helper: %w", err)
	}
	var open int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM sos_requests WHERE helper_device_id = $1 AND status IN ('matched', 'on_the_way', 'arrived')`,
		helperDeviceID).Scan(&open); err != nil {
		return false, fmt.Errorf("count helper assignments: %w", err)
	}
	if open >= maxActive {
		return false, apperr.Conflict(fmt.Sprintf("you already have %d open requests; finish one first", open))
	}
	res, err := tx.ExecContext(ctx, `
		UPDATE sos_requests SET status = 'matched', helper_device_id = $1, updated_at = $2
		WHERE id = $3 AND status = 'waiting' AND device_id <> $1`, helperDeviceID, now, id)
	if err != nil {
		return false, fmt.Errorf("accept sos: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return false, nil
	}
	if err := insertSOSEvent(ctx, tx, id, sos.StatusMatched, sos.RoleHelper, now); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// sosDistanceSQL is the haversine distance between an SOS row and a point.
func sosDistanceSQL(latPH, lngPH string) string {
	return fmt.Sprintf(`(2 * 6371000 * asin(sqrt(
		power(sin(radians(s.latitude - %[1]s) / 2), 2) +
		cos(radians(%[1]s)) * cos(radians(s.latitude)) * power(sin(radians(s.longitude - %[2]s) / 2), 2)
	)))`, latPH, lngPH)
}

func radiusBox(lat, lng, radiusM float64) ports.BBox {
	dLat := radiusM / 111_320
	dLng := radiusM / (111_320 * math.Max(math.Cos(lat*math.Pi/180), 0.01))
	return ports.BBox{MinLat: lat - dLat, MaxLat: lat + dLat, MinLng: lng - dLng, MaxLng: lng + dLng}
}

func (repo *SOSRepository) NearbyWaiting(ctx context.Context, q ports.NearbySOSQuery) ([]ports.SOSWithDistance, error) {
	if len(q.Types) == 0 {
		return []ports.SOSWithDistance{}, nil
	}
	var b queryBuilder
	b.add("s.status = 'waiting'")
	b.add(b.bboxSQL("s.latitude", "s.longitude", radiusBox(q.Latitude, q.Longitude, q.RadiusM)))
	types := make([]string, len(q.Types))
	for i, t := range q.Types {
		types[i] = string(t)
	}
	b.inList("s.type", types)
	b.add("s.device_id <> " + b.arg(q.ExcludeDeviceID))
	b.add("s.created_at >= " + b.arg(q.CreatedSince))
	dist := sosDistanceSQL(b.arg(q.Latitude), b.arg(q.Longitude))
	b.add(dist + " <= " + b.arg(q.RadiusM))

	rows, err := repo.db.QueryContext(ctx, `SELECT `+sosColumns+`, `+dist+` AS distance_m FROM sos_requests s`+b.whereSQL()+
		` ORDER BY distance_m LIMIT `+b.arg(q.Limit), b.args...)
	if err != nil {
		return nil, fmt.Errorf("query nearby sos: %w", err)
	}
	defer rows.Close()
	out := []ports.SOSWithDistance{}
	for rows.Next() {
		var item ports.SOSWithDistance
		r, err := scanSOS(rows, &item.DistanceM)
		if err != nil {
			return nil, fmt.Errorf("scan nearby sos: %w", err)
		}
		item.Request = *r
		out = append(out, item)
	}
	return out, rows.Err()
}

// CountHelpersNear counts active helpers whose own radius covers the point
// and who have a matching capability (GIN-indexed array overlap).
func (repo *SOSRepository) CountHelpersNear(ctx context.Context, q ports.HelperCountQuery) (int, error) {
	caps := make([]string, len(q.Capabilities))
	for i, c := range q.Capabilities {
		caps[i] = string(c)
	}
	box := radiusBox(q.Latitude, q.Longitude, 10_000) // largest helper radius
	var n int
	err := repo.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM helpers h
		WHERE h.active AND h.capabilities && $1::text[] AND h.device_id <> $2 AND h.location_at >= $3
			AND h.latitude BETWEEN $4 AND $5 AND h.longitude BETWEEN $6 AND $7
			AND (2 * 6371000 * asin(sqrt(
				power(sin(radians(h.latitude - $8) / 2), 2) +
				cos(radians($8)) * cos(radians(h.latitude)) * power(sin(radians(h.longitude - $9) / 2), 2)
			))) <= h.radius_m`,
		caps, q.ExcludeDeviceID, q.LocationSince, box.MinLat, box.MaxLat, box.MinLng, box.MaxLng, q.Latitude, q.Longitude,
	).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count helpers: %w", err)
	}
	return n, nil
}

const helperColumns = `device_id, active, capabilities, radius_m, display_name, contact_phone,
	latitude, longitude, location_at, created_at, updated_at`

func (repo *SOSRepository) GetHelper(ctx context.Context, deviceID string) (*sos.Helper, error) {
	var (
		h    sos.Helper
		caps string
	)
	// Capabilities are read as a comma-joined string: database/sql can't scan
	// a Postgres array portably.
	err := repo.db.QueryRowContext(ctx, `SELECT device_id, active, array_to_string(capabilities, ','), radius_m, display_name,
		contact_phone, latitude, longitude, location_at, created_at, updated_at FROM helpers WHERE device_id = $1`, deviceID).Scan(
		&h.DeviceID, &h.Active, &caps, &h.RadiusM, &h.DisplayName, &h.ContactPhone,
		&h.Latitude, &h.Longitude, &h.LocationAt, &h.CreatedAt, &h.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get helper: %w", err)
	}
	h.Capabilities = []sos.Capability{}
	for _, c := range strings.Split(caps, ",") {
		if c != "" {
			h.Capabilities = append(h.Capabilities, sos.Capability(c))
		}
	}
	return &h, nil
}

func (repo *SOSRepository) UpsertHelper(ctx context.Context, h *sos.Helper) error {
	caps := make([]string, len(h.Capabilities))
	for i, c := range h.Capabilities {
		caps[i] = string(c)
	}
	_, err := repo.db.ExecContext(ctx, `
		INSERT INTO helpers (`+helperColumns+`)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		ON CONFLICT (device_id) DO UPDATE SET
			active = EXCLUDED.active, capabilities = EXCLUDED.capabilities, radius_m = EXCLUDED.radius_m,
			display_name = EXCLUDED.display_name, contact_phone = EXCLUDED.contact_phone,
			latitude = EXCLUDED.latitude, longitude = EXCLUDED.longitude, location_at = EXCLUDED.location_at,
			updated_at = EXCLUDED.updated_at`,
		h.DeviceID, h.Active, caps, h.RadiusM, h.DisplayName, h.ContactPhone,
		h.Latitude, h.Longitude, h.LocationAt, h.CreatedAt, h.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("upsert helper: %w", err)
	}
	return nil
}

func (repo *SOSRepository) UpdateHelperLocation(ctx context.Context, deviceID string, lat, lng float64, at time.Time) error {
	if _, err := repo.db.ExecContext(ctx, `UPDATE helpers SET latitude = $1, longitude = $2, location_at = $3 WHERE device_id = $4`,
		lat, lng, at, deviceID); err != nil {
		return fmt.Errorf("update helper location: %w", err)
	}
	return nil
}
