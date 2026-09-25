package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"floodnow-api/internal/domain/moderation"
	"floodnow-api/internal/domain/report"
	"floodnow-api/internal/ports"
)

type ModerationRepository struct {
	db *sql.DB
}

func NewModerationRepository(db *sql.DB) *ModerationRepository {
	return &ModerationRepository{db: db}
}

const complaintColumns = `m.id, m.report_id, m.device_id, m.reason, m.details, m.status, m.created_at, m.resolved_at`

func scanComplaint(row rowScanner) (*moderation.Complaint, error) {
	var c moderation.Complaint
	if err := row.Scan(&c.ID, &c.ReportID, &c.DeviceID, &c.Reason, &c.Details, &c.Status, &c.CreatedAt, &c.ResolvedAt); err != nil {
		return nil, err
	}
	return &c, nil
}

// setHidden flips a locked report's visibility and records the event.
func setHidden(ctx context.Context, tx *sql.Tx, reportID uuid.UUID, hide bool, reason report.HiddenReason, now time.Time) error {
	if hide {
		if _, err := tx.ExecContext(ctx, `UPDATE reports SET hidden_at = $1, hidden_reason = $2 WHERE id = $3`, now, reason, reportID); err != nil {
			return fmt.Errorf("hide report: %w", err)
		}
		return insertEvent(ctx, tx, reportID, report.EventHidden, nil, now)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE reports SET hidden_at = NULL, hidden_reason = NULL WHERE id = $1`, reportID); err != nil {
		return fmt.Errorf("unhide report: %w", err)
	}
	return insertEvent(ctx, tx, reportID, report.EventUnhidden, nil, now)
}

// AddComplaint locks the report row so concurrent complaints see a
// consistent pending count, inserts the complaint (a device's repeat is
// ignored), then applies the policy's auto-hide rule.
func (repo *ModerationRepository) AddComplaint(ctx context.Context, p ports.ComplaintParams) (*moderation.Complaint, bool, error) {
	tx, err := repo.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	var hiddenAt *time.Time
	err = tx.QueryRowContext(ctx, `SELECT hidden_at FROM reports WHERE id = $1 FOR UPDATE`, p.Complaint.ReportID).Scan(&hiddenAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("lock report: %w", err)
	}

	c := p.Complaint
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO moderation_reports (id, report_id, device_id, reason, details, status, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (report_id, device_id) DO NOTHING`,
		c.ID, c.ReportID, c.DeviceID, c.Reason, c.Details, c.Status, c.CreatedAt); err != nil {
		return nil, false, fmt.Errorf("insert complaint: %w", err)
	}
	stored, err := scanComplaint(tx.QueryRowContext(ctx, `SELECT `+complaintColumns+` FROM moderation_reports m WHERE m.report_id = $1 AND m.device_id = $2`,
		c.ReportID, c.DeviceID))
	if err != nil {
		return nil, false, fmt.Errorf("reload complaint: %w", err)
	}

	hidden := false
	if hiddenAt == nil {
		var pending int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM moderation_reports WHERE report_id = $1 AND status = 'pending'`,
			c.ReportID).Scan(&pending); err != nil {
			return nil, false, fmt.Errorf("count complaints: %w", err)
		}
		if p.Policy.ShouldAutoHide(pending) {
			if err := setHidden(ctx, tx, c.ReportID, true, report.HiddenAutoThreshold, p.Now); err != nil {
				return nil, false, err
			}
			hidden = true
		}
	}
	return stored, hidden, tx.Commit()
}

func (repo *ModerationRepository) CountByDeviceSince(ctx context.Context, deviceID string, since time.Time) (int, error) {
	var n int
	if err := repo.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM moderation_reports WHERE device_id = $1 AND created_at >= $2`,
		deviceID, since).Scan(&n); err != nil {
		return 0, fmt.Errorf("count device complaints: %w", err)
	}
	return n, nil
}

// complaintsPerItem caps how many individual complaints the queue shows per
// incident (the counts cover all of them).
const complaintsPerItem = 10

// Queue loads the review queue in a fixed number of queries regardless of
// its length: grouped counts, the reports, their complaints, their events.
func (repo *ModerationRepository) Queue(ctx context.Context, limit int) ([]ports.ModerationQueueItem, error) {
	rows, err := repo.db.QueryContext(ctx, `
		SELECT report_id, COUNT(*), MAX(created_at) FROM moderation_reports
		WHERE status = 'pending'
		GROUP BY report_id
		ORDER BY COUNT(*) DESC, MAX(created_at) DESC
		LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("query moderation queue: %w", err)
	}
	var items []ports.ModerationQueueItem
	index := map[uuid.UUID]int{}
	var ids []uuid.UUID
	for rows.Next() {
		var item ports.ModerationQueueItem
		var id uuid.UUID
		if err := rows.Scan(&id, &item.ComplaintCount, &item.LatestAt); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan moderation queue: %w", err)
		}
		item.Reasons = map[moderation.Reason]int{}
		index[id] = len(items)
		ids = append(ids, id)
		items = append(items, item)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return []ports.ModerationQueueItem{}, nil
	}
	idArg := uuidStrings(ids)

	reportRows, err := repo.db.QueryContext(ctx, `SELECT `+reportColumns+` FROM reports r WHERE r.id = ANY($1::uuid[])`, idArg)
	if err != nil {
		return nil, fmt.Errorf("query queued reports: %w", err)
	}
	for reportRows.Next() {
		r, err := scanReport(reportRows)
		if err != nil {
			reportRows.Close()
			return nil, fmt.Errorf("scan queued report: %w", err)
		}
		items[index[r.ID]].Report = *r
	}
	reportRows.Close()

	complaintRows, err := repo.db.QueryContext(ctx, `
		SELECT `+complaintColumns+`, m.n FROM (
			SELECT *, row_number() OVER (PARTITION BY report_id ORDER BY created_at DESC) AS n
			FROM moderation_reports WHERE status = 'pending' AND report_id = ANY($1::uuid[])
		) m ORDER BY m.report_id, m.created_at DESC`, idArg)
	if err != nil {
		return nil, fmt.Errorf("query queued complaints: %w", err)
	}
	for complaintRows.Next() {
		var (
			c moderation.Complaint
			n int
		)
		if err := complaintRows.Scan(&c.ID, &c.ReportID, &c.DeviceID, &c.Reason, &c.Details, &c.Status, &c.CreatedAt, &c.ResolvedAt, &n); err != nil {
			complaintRows.Close()
			return nil, fmt.Errorf("scan queued complaint: %w", err)
		}
		item := &items[index[c.ReportID]]
		item.Reasons[c.Reason]++
		if n <= complaintsPerItem {
			item.Complaints = append(item.Complaints, c)
		}
	}
	complaintRows.Close()

	events, err := eventsFor(ctx, repo.db, ids, 20)
	if err != nil {
		return nil, err
	}
	for id, i := range index {
		items[i].Events = events[id]
	}
	return items, nil
}

func (repo *ModerationRepository) Apply(ctx context.Context, reportID uuid.UUID, action moderation.Action, now time.Time) (bool, error) {
	tx, err := repo.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	var hiddenAt *time.Time
	err = tx.QueryRowContext(ctx, `SELECT hidden_at FROM reports WHERE id = $1 FOR UPDATE`, reportID).Scan(&hiddenAt)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("lock report: %w", err)
	}

	hide, complaintStatus := action.Outcome()
	if hide != (hiddenAt != nil) {
		if err := setHidden(ctx, tx, reportID, hide, report.HiddenAdmin, now); err != nil {
			return false, err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE moderation_reports SET status = $1, resolved_at = $2 WHERE report_id = $3 AND status = 'pending'`,
		complaintStatus, now, reportID); err != nil {
		return false, fmt.Errorf("resolve complaints: %w", err)
	}
	return true, tx.Commit()
}
