# Crypto Market Advisor — task shortcuts.
# Every target is one plain command, so anyone without make (Windows, for
# instance) can read it off this file and run it directly.

COMPOSE ?= docker compose
BACKEND_DIR := backend

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

.PHONY: up
up: ## Start postgres, backend and frontend
	$(COMPOSE) up -d --build

.PHONY: up-llm
up-llm: ## Start the stack together with the bundled llama.cpp server
	$(COMPOSE) --profile llm up -d --build

.PHONY: down
down: ## Stop the stack
	$(COMPOSE) down

.PHONY: clean
clean: ## Stop the stack and delete the database volume
	$(COMPOSE) down -v

.PHONY: logs
logs: ## Follow all container logs
	$(COMPOSE) logs -f

.PHONY: logs-backend
logs-backend: ## Follow backend logs only
	$(COMPOSE) logs -f backend

.PHONY: ps
ps: ## Show container status
	$(COMPOSE) ps

.PHONY: config
config: ## Validate the compose file
	$(COMPOSE) config >/dev/null && echo "docker-compose.yml is valid"

.PHONY: build
build: ## Build the backend binary locally
	cd $(BACKEND_DIR) && go build ./...

.PHONY: test
test: ## Run backend unit tests
	cd $(BACKEND_DIR) && go test ./...

.PHONY: test-race
test-race: ## Run backend tests with the race detector
	cd $(BACKEND_DIR) && go test -race ./...

.PHONY: test-integration
test-integration: ## Run repository integration tests (needs TEST_DATABASE_URL)
	cd $(BACKEND_DIR) && go test -tags=integration ./internal/repository/...

.PHONY: vet
vet: ## Run go vet
	cd $(BACKEND_DIR) && go vet ./...

.PHONY: lint
lint: ## Run golangci-lint (installs nothing; see docs/development.md if missing)
	cd $(BACKEND_DIR) && golangci-lint run

.PHONY: fmt
fmt: ## Format Go sources
	cd $(BACKEND_DIR) && gofmt -w . && go run golang.org/x/tools/cmd/goimports@latest -w .

.PHONY: migrate
migrate: ## Apply database migrations
	$(COMPOSE) run --rm backend migrate

.PHONY: frontend-install
frontend-install: ## Install frontend dependencies
	cd frontend && npm install

.PHONY: frontend-dev
frontend-dev: ## Run the Vite dev server against a local backend
	cd frontend && npm run dev

.PHONY: frontend-build
frontend-build: ## Build the frontend bundle
	cd frontend && npm run build

.PHONY: frontend-test
frontend-test: ## Run frontend tests
	cd frontend && npm test -- --run

.PHONY: check
check: vet test frontend-test ## Run every automated check
