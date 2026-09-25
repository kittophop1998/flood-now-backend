.PHONY: run build test migrate-up migrate-down migrate-status

DATABASE_URL ?= postgres://floodnow:floodnow@localhost:5433/floodnow?sslmode=disable

run:
	go run ./cmd/api

build:
	go build -o bin/api ./cmd/api

test:
	go test ./...

migrate-up:
	DATABASE_URL="$(DATABASE_URL)" go run ./cmd/migrate up

migrate-down:
	DATABASE_URL="$(DATABASE_URL)" go run ./cmd/migrate down

migrate-status:
	DATABASE_URL="$(DATABASE_URL)" go run ./cmd/migrate status
