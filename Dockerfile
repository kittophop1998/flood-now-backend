# syntax=docker/dockerfile:1

# =========================
# Build stage
# =========================
FROM golang:1.27.1-alpine AS builder

WORKDIR /src

# Install certificates/git in case Go modules require HTTPS/git
RUN apk add --no-cache ca-certificates git

# Copy dependency files first for Docker layer caching
COPY go.mod go.sum ./

RUN go mod download

# Copy application source
COPY . .

# Build static Go binary
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build \
    -trimpath \
    -ldflags="-s -w" \
    -o /out/api \
    ./cmd/api


# =========================
# Runtime stage
# =========================
FROM alpine:3.22 AS runtime

RUN apk add --no-cache ca-certificates && \
    adduser -D -u 10001 floodnow

WORKDIR /app

COPY --from=builder /out/api /usr/local/bin/api

USER floodnow

# Railway overrides PORT automatically at runtime
ENV PORT=4000

EXPOSE 4000

ENTRYPOINT ["/usr/local/bin/api"]