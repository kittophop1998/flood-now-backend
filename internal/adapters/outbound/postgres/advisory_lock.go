package postgres

import (
	"context"
	"database/sql"
	"fmt"
)

// imageCleanupLockKey is the advisory lock id for the image cleanup job, so
// at most one replica runs it at a time. A fixed, arbitrary int64 — it only
// needs to be unique among this app's advisory lock uses (there are none
// others today).
const imageCleanupLockKey = 875_201_001

// TryAdvisoryLock attempts to take the image cleanup session-level advisory
// lock on a dedicated connection. ok is false if another process/replica
// already holds it — the caller should skip this run, not wait. release
// must be called (even on ok == false, where it just closes the connection)
// once the caller is done.
func TryAdvisoryLock(ctx context.Context, db *sql.DB) (release func(), ok bool, err error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("acquire connection for advisory lock: %w", err)
	}
	release = func() { _ = conn.Close() }

	if err := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1)`, imageCleanupLockKey).Scan(&ok); err != nil {
		release()
		return nil, false, fmt.Errorf("try advisory lock: %w", err)
	}
	if !ok {
		release()
		return nil, false, nil
	}

	return func() {
		_, _ = conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1)`, imageCleanupLockKey)
		_ = conn.Close()
	}, true, nil
}
