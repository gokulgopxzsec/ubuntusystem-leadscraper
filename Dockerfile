# Must match the go directive in go.mod. Building 1.26 code on the 1.24 image
# made the toolchain download a second Go mid-build, which is slow on a 2-core
# box and fails outright under GOTOOLCHAIN=local.
FROM golang:1.26-alpine AS builder

RUN apk add --no-cache git ca-certificates

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /bin/server ./cmd/server

FROM alpine:3.20

# docker-cli lets the worker launch gosom/google-maps-scraper as a sibling
# container. It is the client only; the daemon is the host's, reached through the
# socket that docker-compose mounts.
RUN apk add --no-cache ca-certificates tzdata docker-cli

RUN adduser -D -u 1001 app

COPY --from=builder /bin/server /app/server
# Migrations are read from disk at startup, so they must be in the image.
COPY --from=builder /src/migrations /app/migrations

# The Maps scraper writes its query and result files here, and the host bind
# mounts the same directory so the sibling container can see them.
RUN mkdir -p /app/data/gmaps && chown -R app:app /app/data

WORKDIR /app
USER app

EXPOSE 8080
ENTRYPOINT ["/app/server"]
