// Package migrate applies paired SQL migration files to a database/sql DB.
// It has no database-driver or application configuration dependency, so each
// command can provide its own connection and embedded migration filesystem.
package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"io/fs"
	"regexp"
	"sort"
	"strings"
)

type migration struct {
	version string
	name    string
	upSQL   string
	downSQL string
}

var versionPattern = regexp.MustCompile(`^(\d+)_(.+)\.up\.sql$`)

// Runner applies SQL migrations from Migrations using DB. Output receives
// progress and status messages; a nil Output discards them.
type Runner struct {
	DB         *sql.DB
	Migrations fs.FS
	Output     io.Writer
}

// Run executes up, down, or status. An empty command is treated as up.
func (r Runner) Run(ctx context.Context, command string) error {
	if r.DB == nil {
		return fmt.Errorf("migration database is required")
	}
	if r.Migrations == nil {
		return fmt.Errorf("migration filesystem is required")
	}
	if command == "" {
		command = "up"
	}
	if command != "up" && command != "down" && command != "status" {
		return fmt.Errorf("unknown command %q (expected up, down, or status)", command)
	}

	if err := ensureSchemaMigrationsTable(ctx, r.DB); err != nil {
		return fmt.Errorf("ensure schema_migrations table: %w", err)
	}
	all, err := loadMigrations(r.Migrations)
	if err != nil {
		return fmt.Errorf("load migrations: %w", err)
	}
	applied, err := appliedVersions(ctx, r.DB)
	if err != nil {
		return fmt.Errorf("read applied migrations: %w", err)
	}

	switch command {
	case "up":
		if err := r.up(ctx, all, applied); err != nil {
			return fmt.Errorf("migrate up: %w", err)
		}
	case "down":
		if err := r.down(ctx, all, applied); err != nil {
			return fmt.Errorf("migrate down: %w", err)
		}
	case "status":
		if err := r.status(all, applied); err != nil {
			return fmt.Errorf("write migration status: %w", err)
		}
	}
	return nil
}

func ensureSchemaMigrationsTable(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    text PRIMARY KEY,
			name       text NOT NULL,
			applied_at timestamptz NOT NULL DEFAULT now()
		)`)
	return err
}

// loadMigrations reads embedded *.up.sql / *.down.sql files and pairs them by
// their numeric version prefix (for example, "000001_init.up.sql").
func loadMigrations(source fs.FS) ([]migration, error) {
	entries, err := fs.ReadDir(source, ".")
	if err != nil {
		return nil, err
	}
	byVersion := map[string]*migration{}
	get := func(version, name string) *migration {
		m := byVersion[version]
		if m == nil {
			m = &migration{version: version, name: name}
			byVersion[version] = m
		}
		return m
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		content, err := fs.ReadFile(source, name)
		if err != nil {
			return nil, err
		}
		switch {
		case strings.HasSuffix(name, ".up.sql"):
			parts := versionPattern.FindStringSubmatch(name)
			if parts == nil {
				return nil, fmt.Errorf("unexpected migration filename %q", name)
			}
			get(parts[1], parts[2]).upSQL = string(content)
		case strings.HasSuffix(name, ".down.sql"):
			base := strings.TrimSuffix(name, ".down.sql")
			parts := strings.SplitN(base, "_", 2)
			if len(parts) != 2 {
				return nil, fmt.Errorf("unexpected migration filename %q", name)
			}
			get(parts[0], parts[1]).downSQL = string(content)
		}
	}

	out := make([]migration, 0, len(byVersion))
	for _, m := range byVersion {
		out = append(out, *m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	return out, nil
}

func appliedVersions(ctx context.Context, db *sql.DB) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	applied := map[string]bool{}
	for rows.Next() {
		var version string
		if err := rows.Scan(&version); err != nil {
			return nil, err
		}
		applied[version] = true
	}
	return applied, rows.Err()
}

func (r Runner) up(ctx context.Context, all []migration, applied map[string]bool) error {
	ran := 0
	for _, m := range all {
		if applied[m.version] {
			continue
		}
		if m.upSQL == "" {
			return fmt.Errorf("migration %s_%s has no .up.sql", m.version, m.name)
		}
		if err := r.print("applying %s_%s\n", m.version, m.name); err != nil {
			return err
		}
		if err := runInTx(ctx, r.DB, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, m.upSQL); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version, name) VALUES ($1, $2)`, m.version, m.name)
			return err
		}); err != nil {
			return fmt.Errorf("apply %s_%s: %w", m.version, m.name, err)
		}
		ran++
	}
	if ran == 0 {
		return r.print("nothing to migrate, already up to date\n")
	}
	return nil
}

func (r Runner) down(ctx context.Context, all []migration, applied map[string]bool) error {
	var last *migration
	for i := len(all) - 1; i >= 0; i-- {
		if applied[all[i].version] {
			last = &all[i]
			break
		}
	}
	if last == nil {
		return r.print("no migrations to revert\n")
	}
	if last.downSQL == "" {
		return fmt.Errorf("migration %s_%s has no .down.sql", last.version, last.name)
	}
	if err := r.print("reverting %s_%s\n", last.version, last.name); err != nil {
		return err
	}
	return runInTx(ctx, r.DB, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, last.downSQL); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM schema_migrations WHERE version = $1`, last.version)
		return err
	})
}

func (r Runner) status(all []migration, applied map[string]bool) error {
	for _, m := range all {
		state := "pending"
		if applied[m.version] {
			state = "applied"
		}
		if err := r.print("%s  %-8s %s\n", m.version, state, m.name); err != nil {
			return err
		}
	}
	return nil
}

func (r Runner) print(format string, args ...any) error {
	if r.Output == nil {
		return nil
	}
	_, err := fmt.Fprintf(r.Output, format, args...)
	return err
}

func runInTx(ctx context.Context, db *sql.DB, fn func(tx *sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}
