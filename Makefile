COMPOSE := $(shell if docker compose version >/dev/null 2>&1; then echo 'docker compose'; else echo 'docker-compose'; fi)
.PHONY: replay init infra setup demo local-images local-demo up down test build logs chaos-on chaos-off check
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

replay:
	go run ./cmd/flagguard -config guardrails.yaml -replay docs/evidence/checkout-trace.json -expect checkout_flow=error_rate -format text

.PHONY: replay-price
replay-price:
	go run ./cmd/flagguard -config deploy/blackbox/guardrails.yaml -replay deploy/blackbox/cases/sustained.json -expect price_response_v2=contract_pass_fraction -format text


.PHONY: test-price-policy
test-price-policy:
	go run ./cmd/flagguard -config deploy/blackbox/guardrails.yaml -suite deploy/blackbox/cases/suite.json -format text

.PHONY: rehearse-recovery
rehearse-recovery:
	go test -count=1 -v ./internal/server -run '^TestPriceRecoveryRehearsal$$'

.PHONY: stress-price
stress-price:
	go run ./cmd/flagguard -config deploy/blackbox/guardrails.yaml -replay deploy/blackbox/cases/sustained.json -gap-check price_response_v2 -format text

UBUNTU_CASE := docs/evidence/ubuntu-checkout
UBUNTU_POLICY ?= $(UBUNTU_CASE)/policy.yaml
.PHONY: replay-ubuntu
replay-ubuntu:
	go run ./cmd/flagguard -config "$(UBUNTU_POLICY)" -suite $(UBUNTU_CASE)/protection.json -format text
	go run ./cmd/flagguard -config "$(UBUNTU_POLICY)" -suite $(UBUNTU_CASE)/suite.json -format text

.PHONY: test-postgres-policy
test-postgres-policy:
	go run ./cmd/flagguard -config deploy/postgres/guardrails.yaml -suite deploy/postgres/cases/suite.json -format text
