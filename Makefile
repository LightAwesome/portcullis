.PHONY: build run test lint clean tidy help up down nuke ps logs

# Default goal — runs when you just type `make`.
.DEFAULT_GOAL := help

help: ## Show available targets.
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

build: ## Compile the binary to ./bin/portcullis.
	go build -o bin/portcullis ./cmd/portcullis

run: ## Run the gateway directly via go run.
	go run ./cmd/portcullis

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

down: ## Stop dev infrastructure (preserves data).
	docker compose down

nuke: ## Stop dev infrastructure AND DELETE ALL DATA.
	docker compose down -v

ps: ## Show running containers.
	docker compose ps

logs: ## Tail container logs (Ctrl-C to exit).
	docker compose logs -f
