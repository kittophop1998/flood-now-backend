// Command migrate applies SQL migrations (embedded from migrations/) to
// DATABASE_URL. It tracks applied versions in a schema_migrations table so
// it's safe to run on every deploy — already-applied migrations are skipped.
//
// Usage:
//
//	go run ./cmd/migrate            # apply all pending migrations
//	go run ./cmd/migrate up
//	go run ./cmd/migrate down       # revert the most recently applied migration
//	go run ./cmd/migrate status     # list applied/pending migrations
package main

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"log"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/joho/godotenv"

	"floodnow-api/internal/adapters/outbound/postgres"
	"floodnow-api/migrations"
)

type migration struct {
	version string
	name    string
	upSQL   string
	downSQL string
}

var versionPattern = regexp.MustCompile(`^(\d+)_(.+)\.up\.sql$`)

func main() {
	_ = godotenv.Load()

	cmd := "up"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		log.Fatal("DATABASE_URL is required")
	}

	ctx := context.Background()
	db, err := postgres.Connect(ctx, dsn)
	if err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer db.Close()

	if err := ensureSchemaMigrationsTable(ctx, db); err != nil {
		log.Fatalf("ensure schema_migrations table: %v", err)
	}

	all, err := loadMigrations()
	if err != nil {
		log.Fatalf("load migrations: %v", err)
	}

	applied, err := appliedVersions(ctx, db)
	if err != nil {
		log.Fatalf("read applied migrations: %v", err)
	}

	switch cmd {
	case "up":
		if err := up(ctx, db, all, applied); err != nil {
			log.Fatalf("migrate up: %v", err)
		}
	case "down":
		if err := down(ctx, db, all, applied); err != nil {
			log.Fatalf("migrate down: %v", err)
		}
	case "status":
		status(all, applied)
	default:
		log.Fatalf("unknown command %q (expected up, down, or status)", cmd)
	}
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

// loadMigrations reads the embedded *.up.sql / *.down.sql files and pairs
// them up by their numeric version prefix (e.g. "000001_init.up.sql").
func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrations.FS, ".")
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

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		content, err := fs.ReadFile(migrations.FS, name)
		if err != nil {
			return nil, err
		}

		switch {
		case strings.HasSuffix(name, ".up.sql"):
			mm := versionPattern.FindStringSubmatch(name)
			if mm == nil {
				return nil, fmt.Errorf("unexpected migration filename %q", name)
			}
			get(mm[1], mm[2]).upSQL = string(content)
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
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		applied[v] = true
	}
	return applied, rows.Err()
}

func up(ctx context.Context, db *sql.DB, all []migration, applied map[string]bool) error {
	ran := 0
	for _, m := range all {
		if applied[m.version] {
			continue
		}
		if m.upSQL == "" {
			return fmt.Errorf("migration %s_%s has no .up.sql", m.version, m.name)
		}
		log.Printf("applying %s_%s", m.version, m.name)
		if err := runInTx(ctx, db, func(tx *sql.Tx) error {
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
		log.Println("nothing to migrate, already up to date")
	}
	return nil
}

func down(ctx context.Context, db *sql.DB, all []migration, applied map[string]bool) error {
	var last *migration
	for i := len(all) - 1; i >= 0; i-- {
		if applied[all[i].version] {
			last = &all[i]
			break
		}
	}
	if last == nil {
		log.Println("no migrations to revert")
		return nil
	}
	if last.downSQL == "" {
		return fmt.Errorf("migration %s_%s has no .down.sql", last.version, last.name)
	}

	log.Printf("reverting %s_%s", last.version, last.name)
	return runInTx(ctx, db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, last.downSQL); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM schema_migrations WHERE version = $1`, last.version)
		return err
	})
}

func status(all []migration, applied map[string]bool) {
	for _, m := range all {
		state := "pending"
		if applied[m.version] {
			state = "applied"
		}
		fmt.Printf("%s  %-8s %s\n", m.version, state, m.name)
	}
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
