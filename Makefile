.PHONY: build run test lint clean tidy help

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
