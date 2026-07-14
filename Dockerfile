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

RUN apk add --no-cache ca-certificates tzdata

RUN adduser -D -u 1001 app

COPY --from=builder /bin/server /app/server
# Migrations are read from disk at startup, so they must be in the image.
COPY --from=builder /src/migrations /app/migrations

WORKDIR /app
USER app

EXPOSE 8080
ENTRYPOINT ["/app/server"]
