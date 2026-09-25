# syntax=docker/dockerfile:1

FROM golang:1.27-alpine AS build
WORKDIR /src

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=bind,source=go.mod,target=go.mod \
    --mount=type=bind,source=go.sum,target=go.sum \
    go mod download

COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api

FROM alpine:3.21 AS runtime
RUN apk add --no-cache ca-certificates && \
    adduser -D -u 10001 floodnow
COPY --from=build /out/api /usr/local/bin/api

USER floodnow
EXPOSE 4000
ENTRYPOINT ["/usr/local/bin/api"]
