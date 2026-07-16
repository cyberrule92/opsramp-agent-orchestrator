GO       ?= go
PKG      := ./...
COMPOSE  ?= docker compose
BASE_URL ?= http://localhost:8080

.PHONY: help build test vet tidy run-orchestrator run-agent up down logs ps rebuild seed clean

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

build: ## Compile both binaries into ./bin
	$(GO) build -trimpath -o bin/orchestrator ./cmd/orchestrator
	$(GO) build -trimpath -o bin/demo-agent ./cmd/demo-agent

vet: ## go vet
	$(GO) vet $(PKG)

test: ## Run unit tests
	$(GO) test $(PKG)

tidy: ## Tidy modules
	$(GO) mod tidy

up: ## Build and start the full stack (postgres + orchestrator + demo-agent)
	$(COMPOSE) up -d --build

down: ## Stop the stack (keep volumes)
	$(COMPOSE) down

clean: ## Stop the stack and remove volumes
	$(COMPOSE) down -v

logs: ## Tail logs
	$(COMPOSE) logs -f --tail=100

ps: ## Show running services
	$(COMPOSE) ps

rebuild: ## Rebuild images without cache
	$(COMPOSE) build --no-cache

scale-agents: ## Run N demo agents: make scale-agents N=5
	$(COMPOSE) up -d --scale demo-agent=$(or $(N),3)

seed: ## Push an example config to the default group
	./scripts/seed.sh $(BASE_URL)
