// Command migrate applies SQL migrations (embedded from migrations/) to
// DATABASE_URL. It tracks applied versions in schema_migrations so it is safe
// to run on every deploy — already-applied migrations are skipped.
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
	"fmt"
	"log"
	"os"

	"github.com/joho/godotenv"

	"floodnow-api/internal/adapters/outbound/postgres"
	"floodnow-api/migrations"
	"floodnow-api/pkg/migrate"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	_ = godotenv.Load()

	command := "up"
	if len(os.Args) > 1 {
		command = os.Args[1]
	}

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return fmt.Errorf("DATABASE_URL is required")
	}

	ctx := context.Background()
	db, err := postgres.Connect(ctx, dsn)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer db.Close()

	runner := migrate.Runner{
		DB:         db,
		Migrations: migrations.FS,
		Output:     os.Stdout,
	}
	return runner.Run(ctx, command)
}
