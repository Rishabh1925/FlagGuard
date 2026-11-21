COMPOSE := $(shell if docker compose version >/dev/null 2>&1; then echo 'docker compose'; else echo 'docker-compose'; fi)
.PHONY: init infra setup demo local-images local-demo up down test build logs chaos-on chaos-off check
init:
	python3 scripts/init-env.py
infra: init
	$(COMPOSE) up -d flagsmith flagsmith-task-processor prometheus
setup: infra
	python3 scripts/bootstrap.py
demo: setup
	$(COMPOSE) up --build -d
local-images:
	python3 scripts/build-local-images.py
local-demo: setup local-images
	$(COMPOSE) up -d --no-build
up: init
	$(COMPOSE) up --build -d
down:
	$(COMPOSE) down
test:
	go test -race ./...
build:
	go build -o bin/flagguard ./cmd/flagguard
	go build -o bin/demo ./cmd/demo
logs:
	$(COMPOSE) logs -f --tail=50
chaos-on:
	curl -fsS -X POST http://localhost:8085/api/demo/chaos -H 'Content-Type: application/json' -d '{"enabled":true}'
chaos-off:
	curl -fsS -X POST http://localhost:8085/api/demo/chaos -H 'Content-Type: application/json' -d '{"enabled":false}'
check: test build
	$(COMPOSE) config --quiet
