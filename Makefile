.PHONY: build run test lint docker-up docker-down docker-build clean

APP_NAME := leadscraper
BUILD_DIR := build

build:
	go build -ldflags="-s -w" -o $(BUILD_DIR)/server ./cmd/server

run:
	go run ./cmd/server

run-worker:
	go run ./cmd/server --worker

test:
	go test ./... -v -count=1

test-cover:
	go test ./... -coverprofile=coverage.out
	go tool cover -html=coverage.out -o coverage.html

lint:
	golangci-lint run ./...

docker-build:
	docker build -t $(APP_NAME):latest .

docker-up:
	docker compose up --build -d

docker-down:
	docker compose down

docker-logs:
	docker compose logs -f

docker-ps:
	docker compose ps

clean:
	rm -rf $(BUILD_DIR) coverage.out coverage.html

migrate-create:
	@read -p "Migration name: " name; \
	timestamp=$$(date +%Y%m%d%H%M%S); \
	touch migrations/$${timestamp}_$${name}.up.sql; \
	touch migrations/$${timestamp}_$${name}.down.sql; \
	echo "Created migrations/$${timestamp}_$${name}.{up,down}.sql"

.PHONY: migrate-create
