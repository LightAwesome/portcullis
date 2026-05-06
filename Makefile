.PHONY: build run test lint clean tidy help up down nuke ps logs dev \
        migrate-up migrate-down migrate-status migrate-create migrate-force
# O
# Default goal — runs when you just type `make`.
.DEFAULT_GOAL := help

help: ## Show available targets.
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

build: ## Compile the binary to ./bin/portcullis.
	go build -o bin/portcullis ./cmd/portcullis

run: ## Run the gateway directly (alias for `go run ... raise`).
	go run ./cmd/portcullis raise

test: ## Run all tests with race detector.
	go test -race ./...

lint: ## Run go vet (we'll add golangci-lint in Phase 4).
	go vet ./...

tidy: ## Tidy go.mod and go.sum.
	go mod tidy

clean: ## Remove built binaries.
	rm -rf bin/
up: ## Bring up dev infrastructure (postgres, redis).
	docker compose up -d
dev: ## Bring up infra and run the gateway in the foreground.
	@docker compose up -d
	@$(MAKE) run
down: ## Stop dev infrastructure (preserves data).
	docker compose down

nuke: ## Stop dev infrastructure AND DELETE ALL DATA.
	docker compose down -v

ps: ## Show running containers.
	docker compose ps

logs: ## Tail container logs (Ctrl-C to exit).
	docker compose logs -f

# === Migrations ===
# Note: requires PORTCULLIS_DATABASE_URL in environment.
# `make up` first to ensure the database is running.

MIGRATE = migrate -path migrations -database "$$PORTCULLIS_DATABASE_URL"

migrate-up: ## Apply all pending migrations.
	@source .env && $(MIGRATE) up

migrate-down: ## Roll back the most recent migration.
	@source .env && $(MIGRATE) down 1

migrate-status: ## Show current migration version.
	@source .env && $(MIGRATE) version

migrate-create: ## Create a new migration. Usage: make migrate-create name=add_users
	@if [ -z "$(name)" ]; then echo "Usage: make migrate-create name=<snake_case_name>"; exit 1; fi
	migrate create -ext sql -dir migrations -seq -digits 4 $(name)

migrate-force: ## Force schema_migrations to a version. Usage: make migrate-force v=2
	@if [ -z "$(v)" ]; then echo "Usage: make migrate-force v=<integer>"; exit 1; fi
	@source .env && $(MIGRATE) force $(v)
