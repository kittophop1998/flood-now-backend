# syntax=docker/dockerfile:1

FROM golang:1.27.1-alpine AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api

FROM alpine:3.22 AS runtime
RUN apk add --no-cache ca-certificates && \
    adduser -D -u 10001 floodnow
COPY --from=build /out/api /usr/local/bin/api

USER floodnow
# Railway supplies PORT at runtime; 4000 is the local/default container port.
ENV PORT=4000
EXPOSE ${PORT}
ENTRYPOINT ["/usr/local/bin/api"]
