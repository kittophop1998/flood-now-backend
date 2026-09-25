.PHONY: run build test migrate-up migrate-down

DATABASE_URL ?= postgres://floodnow:floodnow@localhost:5433/floodnow?sslmode=disable

run:
	go run ./cmd/api

build:
	go build -o bin/api ./cmd/api

test:
	go test ./...

migrate-up:
	for f in migrations/*.up.sql; do \
		echo "applying $$f"; \
		psql "$(DATABASE_URL)" -v ON_ERROR_STOP=1 -f $$f || exit 1; \
	done

migrate-down:
	for f in $$(ls -r migrations/*.down.sql); do \
		echo "reverting $$f"; \
		psql "$(DATABASE_URL)" -v ON_ERROR_STOP=1 -f $$f || exit 1; \
	done
