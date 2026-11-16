SHELL := /bin/bash
.DEFAULT_GOAL := help

COMPOSE := docker compose
E2E_SUBNET ?= 10.210.1.0/24
# Тесты идут в отдельном compose-проекте со своими томами: синтетика не попадает в рабочие данные.
# У тестового проекта своя подсеть, чтобы он работал одновременно с рабочим стеком.
E2E := CC_OVERLAY_SUBNET=$(E2E_SUBNET) docker compose -p cc-overlay-e2e -f compose.yaml -f compose.e2e.yaml
GO := $(E2E) run --rm --no-deps go

# Образы линтеров. Версии сервисов стека закреплены в compose.yaml.
YAMLLINT_IMAGE := cytopia/yamllint:1-0.14
HADOLINT_IMAGE := hadolint/hadolint:v2.15.1-alpine
SYNTH_IMAGE := cc-overlay/synth:dev

.PHONY: go-cache verify-real help up up-history down logs ps wait check test test-e2e build synth tidy

help: ## Показать список команд
	@grep -hE '^[a-z0-9-]+:.*## ' $(MAKEFILE_LIST) | awk -F ':.*## ' '{printf "  %-12s %s\n", $$1, $$2}'

.env:
	@cp .env.example .env
	@echo "Создан .env из .env.example — поменяй в нём пароль Grafana."

up: .env ## Запустить ядро: коллектор и Prometheus
	$(COMPOSE) up -d --wait
	@$(MAKE) --no-print-directory wait

up-history: .env ## Запустить ядро и Grafana (http://127.0.0.1:3000)
	$(COMPOSE) --profile history up -d --wait
	@$(MAKE) --no-print-directory wait

down: ## Остановить стек; данные в томах сохраняются
	$(COMPOSE) --profile history down

logs: ## Последние логи сервисов и слежение за новыми
	$(COMPOSE) --profile history logs -f --tail=50

ps: ## Состояние контейнеров
	$(COMPOSE) --profile history ps

wait: ## Дождаться готовности коллектора
	@for i in $$(seq 1 30); do \
		curl -fsS -o /dev/null http://127.0.0.1:13133/ && echo "коллектор готов" && exit 0; \
		sleep 1; \
	done; echo "коллектор не ответил на 127.0.0.1:13133 за 30 с" >&2; exit 1

check: .env ## Проверить конфиги: compose, коллектор, Prometheus, YAML, Dockerfile
	$(COMPOSE) --profile history config -q
	$(E2E) --profile test config -q
	$(COMPOSE) run --rm --no-deps otel-collector validate --config=/etc/otelcol-contrib/config.yaml
	$(COMPOSE) run --rm --no-deps --entrypoint promtool prometheus check config /etc/prometheus/prometheus.yml
	docker run --rm -v "$(CURDIR)":/data:ro $(YAMLLINT_IMAGE) -s .
	docker run --rm -i $(HADOLINT_IMAGE) hadolint - < tools/synth/Dockerfile

go-cache:
	@docker volume create cc-overlay-gomod >/dev/null
	@docker volume create cc-overlay-gobuild >/dev/null

test: .env go-cache ## Быстрые тесты без запуска стека: семантика PromQL и unit-тесты Go
	$(COMPOSE) run --rm --no-deps --entrypoint promtool \
		-v "$(CURDIR)/prometheus/tests":/tests:ro prometheus test rules /tests/totals_test.yml
	$(GO) go vet -tags e2e ./...
	$(GO) go test -count=1 ./...

test-e2e: .env go-cache ## Сквозной тест на синтетике: synth → коллектор → Prometheus
	@$(E2E) down -v --remove-orphans >/dev/null 2>&1 || true
	$(E2E) up -d --wait
	@status=0; \
	$(E2E) run --rm go go test -tags e2e -count=1 -v ./e2e/... || status=$$?; \
	if [ $$status -ne 0 ]; then $(E2E) logs --tail=40 otel-collector; fi; \
	$(E2E) down -v --remove-orphans >/dev/null 2>&1; \
	exit $$status

verify-real: ## Проверить, что метрики настоящей сессии Claude Code дошли до Prometheus
	@bash tools/verify-real.sh

build: ## Собрать образ генератора синтетики
	docker build -t $(SYNTH_IMAGE) tools/synth

synth: build ## Минута синтетики в рабочий стек (метка job="claude-code-synth")
	docker run --rm --network cc-overlay_default $(SYNTH_IMAGE) -endpoint otel-collector:4317

tidy: .env go-cache ## Обновить go.mod и go.sum
	$(GO) go mod tidy
