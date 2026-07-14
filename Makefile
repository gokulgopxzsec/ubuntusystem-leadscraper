.PHONY: build run run-worker test test-cover lint clean \
        docker-build docker-up docker-down docker-logs docker-ps \
        dev-deps dev-deps-down smoke migrate-create

APP_NAME  := leadscraper
BUILD_DIR := build
API       ?= http://localhost:8080

build:
	go build -ldflags="-s -w" -o $(BUILD_DIR)/server ./cmd/server

run:
	go run ./cmd/server

run-worker:
	go run ./cmd/server --worker

test:
	go test ./... -count=1

test-cover:
	go test ./... -coverprofile=coverage.out
	go tool cover -html=coverage.out -o coverage.html

lint:
	golangci-lint run ./...

## dev-deps: Postgres and Redis only, for running the binaries natively.
## On a 2-core box this beats rebuilding the image on every change.
dev-deps:
	docker compose up -d postgres redis

dev-deps-down:
	docker compose stop postgres redis

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

## smoke: drive a CSV import end to end and show the scored leads.
smoke:
	@echo "== health =="
	@curl -sf $(API)/api/v1/health && echo
	@echo "\n== queue a csv import =="
	@curl -sf -X POST $(API)/api/v1/scrape \
		-H 'Content-Type: application/json' \
		-d '{"source":"csv","file":"sample-leads.csv","category":"cafe"}' && echo
	@echo "\n== waiting for the worker to drain the pipeline =="
	@sleep 25
	@echo "\n== scored leads =="
	@curl -sf $(API)/api/v1/leads

clean:
	rm -rf $(BUILD_DIR) coverage.out coverage.html

migrate-create:
	@read -p "Migration name: " name; \
	timestamp=$$(date +%Y%m%d%H%M%S); \
	touch migrations/$${timestamp}_$${name}.up.sql; \
	touch migrations/$${timestamp}_$${name}.down.sql; \
	echo "Created migrations/$${timestamp}_$${name}.{up,down}.sql"
