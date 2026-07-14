.PHONY: build start run run-worker run-all test test-cover lint clean \
        docker-build docker-up docker-down docker-logs docker-ps \
        dev-deps dev-deps-down smoke migrate-create

APP_NAME  := leadscraper
BUILD_DIR := build
API       ?= http://localhost:8080

build:
	go build -ldflags="-s -w" -o $(BUILD_DIR)/server ./cmd/server

## start: the whole app in one command (deps, build, API + worker + dashboard).
start:
	./run.sh

## run: API and dashboard only. Pair with run-worker in a second terminal.
run:
	go run ./cmd/server --api

## run-worker: the pipeline only.
run-worker:
	go run ./cmd/server --worker

## run-all: API + worker in one process, assuming postgres and redis are up.
run-all:
	go run ./cmd/server

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

## smoke: scrape Google Maps end to end against a running app, then print the
## ranked leads. Override with: make smoke CATEGORY=tailor LOCATION=Kochi
CATEGORY ?= bakery
LOCATION ?= Kochi
LIMIT    ?= 10

smoke:
	@echo "== health =="
	@curl -sf $(API)/api/v1/health && echo
	@echo ""
	@echo "== scraping Google Maps: $(CATEGORY) in $(LOCATION) =="
	@curl -sf -X POST $(API)/api/v1/scrape \
		-H 'Content-Type: application/json' \
		-d '{"source":"google_maps","category":"$(CATEGORY)","location":"$(LOCATION)","limit":$(LIMIT)}' && echo
	@echo ""
	@echo "== waiting for the pipeline (the browser scrape takes a few minutes) =="
	@until [ "$$(curl -sf $(API)/api/v1/ready | sed -E 's/.*\"queue_depth\":([0-9]+).*/\1/')" = "0" ]; do printf '.'; sleep 5; done
	@echo ""
	@echo ""
	@echo "== ranked leads =="
	@curl -sf $(API)/api/v1/leads

clean:
	rm -rf $(BUILD_DIR) coverage.out coverage.html

migrate-create:
	@read -p "Migration name: " name; \
	timestamp=$$(date +%Y%m%d%H%M%S); \
	touch migrations/$${timestamp}_$${name}.up.sql; \
	touch migrations/$${timestamp}_$${name}.down.sql; \
	echo "Created migrations/$${timestamp}_$${name}.{up,down}.sql"
