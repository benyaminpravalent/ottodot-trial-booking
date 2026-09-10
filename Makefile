# Ottodot trial booking — developer commands.
# Windows without `make`: every target is a plain `go run ./cmd/...` — see README → How to run.

.PHONY: dev build lint test test-race verify db-up db-down db-migrate db-seed db-reset demo-race devdb

dev:            ## run the HTTP server (API + pages) on $PORT (default 3000)
	go run ./cmd/server

build:          ## compile all binaries into ./bin
	go build -o bin/ ./cmd/...

lint:           ## gofmt + go vet (+ staticcheck when installed)
	@test -z "$$(gofmt -l .)" || (echo "gofmt: files need formatting:" && gofmt -l . && exit 1)
	go vet ./...
	@command -v staticcheck >/dev/null 2>&1 && staticcheck ./... || echo "staticcheck not installed; skipped"

test:           ## run all tests against a real Postgres (DATABASE_URL_TEST or embedded)
	go test ./... -count=1

test-race:      ## same, with the Go race detector (needs cgo; Linux/macOS/CI)
	go test ./... -count=1 -race

verify: lint test   ## everything a reviewer should run before trusting the build

db-up:          ## start Postgres 16 via docker compose (creates ottodot + ottodot_test)
	docker compose up -d --wait

db-down:        ## stop Postgres and delete its volume
	docker compose down -v

db-migrate:     ## apply pending SQL migrations to DATABASE_URL
	go run ./cmd/migrate

db-seed:        ## truncate and load the fixed demo dataset
	go run ./cmd/seed

db-reset:       ## drop every table, re-migrate, re-seed
	go run ./cmd/migrate -reset
	go run ./cmd/seed

demo-race:      ## the last-seat race, live: two payers, one seat
	go run ./cmd/demo-race

devdb:          ## no Docker? run a real local Postgres 16 on :5432 (embedded-postgres)
	go run ./cmd/devdb
