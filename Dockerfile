# Build stage
FROM golang:1.22 AS builder
WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /gocql-observer

# Runtime stage
FROM scratch
WORKDIR /observer

COPY --from=builder /gocql-observer ./gocql-observer

ENV LOG_DIRECTORY=/observer/logs \
    QUERY_INTERVAL=15s

VOLUME ["/observer/logs"]
ENTRYPOINT ["/observer/gocql-observer"]
