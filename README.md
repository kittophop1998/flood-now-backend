# apps/api

FloodNow backend. Go + Gin + PostgreSQL, Clean/Hexagonal architecture — see [`/docs/architecture.md`](../../docs/architecture.md) for the module boundaries and [`/docs/api-spec.md`](../../docs/api-spec.md) for the contract.

## Local dev

```bash
# from repo root
docker compose up -d postgres

cd apps/api
cp .env.example .env   # edit if needed
set -a && source .env && set +a
make migrate-up
make run                # http://localhost:4000
```

## Test

```bash
make test
```

## Env vars

See `.env.example`. `R2_*` / `IMAGEKIT_BASE_URL` are only required for the presign/image-delivery flow to actually work against Cloudflare; the rest of the API runs without them.

## Scheduled jobs

A daily R2 image cleanup runs in-process (`R2_CLEANUP_*` env vars; see [`/docs/database.md#image-cleanup`](../../docs/database.md#image-cleanup)). Set `R2_CLEANUP_DRY_RUN=true` to see what it would delete before trusting it in production.
