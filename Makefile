SHELL := /bin/bash
.DEFAULT_GOAL := help

COMPOSE := docker compose
E2E_SUBNET ?= 10.210.1.0/24
# Тесты идут в отдельном compose-проекте со своими томами: синтетика не попадает в рабочие данные.
# У тестового проекта своя подсеть, чтобы он работал одновременно с рабочим стеком.
E2E := CC_OVERLAY_SUBNET=$(E2E_SUBNET) docker compose -p cc-overlay-e2e -f compose.yaml -f compose.e2e.yaml
# Go запускается в контейнере (ADR-0003); в репозитории два модуля.
GO_API := $(E2E) run --rm --no-deps -w /repo/overlay-api go
GO_SYNTH := $(E2E) run --rm --no-deps -w /repo/tools/synth go
# Node тоже в контейнере; зависимости лежат в томе рабочего проекта, чтобы не ставить их на каждый прогон.
NODE := $(COMPOSE) --profile dev run --rm --no-deps overlay-dev

# Образы линтеров. Версии сервисов стека закреплены в compose.yaml.
YAMLLINT_IMAGE := cytopia/yamllint:1-0.14
HADOLINT_IMAGE := hadolint/hadolint:v2.15.1-alpine
SYNTH_IMAGE := cc-overlay/synth:dev

.PHONY: overlay-build overlay-run overlay-dev overlay-lock restart fmt go-cache verify-real help up up-history down logs ps wait check test test-e2e build synth tidy snapshot

help: ## Показать список команд
	@grep -hE '^[a-z0-9-]+:.*## ' $(MAKEFILE_LIST) | awk -F ':.*## ' '{printf "  %-12s %s\n", $$1, $$2}'

.env:
	@cp .env.example .env
	@echo "Создан .env из .env.example — поменяй в нём пароль Grafana."

up: .env ## Запустить ядро: коллектор, Prometheus и overlay-api
	$(COMPOSE) up -d --build --wait
	@$(MAKE) --no-print-directory wait

up-history: .env ## Запустить ядро и Grafana (http://127.0.0.1:3000)
	$(COMPOSE) --profile history up -d --build --wait
	@$(MAKE) --no-print-directory wait

restart: ## Перезапустить сервисы после правки конфигов (compose не замечает изменений в файлах)
	$(COMPOSE) --profile history restart
	@$(MAKE) --no-print-directory wait

down: ## Остановить стек; данные в томах сохраняются
	$(COMPOSE) --profile history down

logs: ## Последние логи сервисов и слежение за новыми
	$(COMPOSE) --profile history logs -f --tail=50

ps: ## Состояние контейнеров
	$(COMPOSE) --profile history ps

wait: ## Дождаться готовности коллектора и Loki
	@for i in $$(seq 1 30); do \
		curl -fs -o /dev/null http://127.0.0.1:13133/ && echo "коллектор готов" && break; \
		[ $$i = 30 ] && { echo "коллектор не ответил на 127.0.0.1:13133 за 30 с" >&2; exit 1; }; \
		sleep 1; \
	done
	@for i in $$(seq 1 60); do \
		curl -fs -o /dev/null http://127.0.0.1:3100/ready && echo "Loki готов" && exit 0; \
		sleep 1; \
	done; echo "Loki не ответил на 127.0.0.1:3100 за 60 с" >&2; exit 1

snapshot: ## Показать текущий снимок overlay-api
	@curl -fsS http://127.0.0.1:8787/api/v1/snapshot | python3 -m json.tool --no-ensure-ascii

check: .env ## Проверить конфиги: compose, коллектор, Prometheus, YAML, Dockerfile
	$(COMPOSE) --profile history config -q
	$(E2E) --profile test config -q
	$(COMPOSE) run --rm --no-deps otel-collector validate --config=/etc/otelcol-contrib/config.yaml
	$(COMPOSE) run --rm --no-deps --entrypoint promtool prometheus check config /etc/prometheus/prometheus.yml
	$(COMPOSE) run --rm --no-deps loki -config.file=/etc/loki/config.yaml -verify-config
	python3 -c 'import json,glob; [json.load(open(f)) for f in glob.glob("grafana/provisioning/dashboards/json/*.json")]' && echo "дашборды: JSON корректен"
	docker run --rm -v "$(CURDIR)":/data:ro $(YAMLLINT_IMAGE) -s .
	docker run --rm -i $(HADOLINT_IMAGE) hadolint - < tools/synth/Dockerfile
	docker run --rm -i $(HADOLINT_IMAGE) hadolint - < overlay-api/Dockerfile
	bash -n statusline/cc-overlay-statusline.sh
	bash -n hooks/cc-overlay-usage.sh
	bash -n tools/verify-real.sh

go-cache:
	@docker volume create cc-overlay-gomod >/dev/null
	@docker volume create cc-overlay-gobuild >/dev/null

test: .env go-cache ## Быстрые тесты без запуска стека: семантика PromQL и unit-тесты Go
	$(COMPOSE) run --rm --no-deps --entrypoint promtool \
		-v "$(CURDIR)/prometheus/tests":/tests:ro prometheus test rules /tests/totals_test.yml
	$(GO_API) sh -c 'test -z "$$(gofmt -l .)" || { gofmt -l .; echo "gofmt: файлы выше не отформатированы"; exit 1; }'
	$(GO_SYNTH) sh -c 'test -z "$$(gofmt -l .)" || { gofmt -l .; echo "gofmt: файлы выше не отформатированы"; exit 1; }'
	$(GO_API) go vet ./...
	$(GO_API) go test -count=1 ./...
	$(GO_SYNTH) go vet -tags e2e ./...
	$(GO_SYNTH) go test -count=1 ./...
	$(NODE) sh -c "npm ci --no-audit --no-fund && npm run typecheck && npm test && npm run build"

test-e2e: .env go-cache ## Сквозной тест на синтетике: synth → коллектор → Prometheus → overlay-api
	@$(E2E) down -v --remove-orphans >/dev/null 2>&1 || true
	$(E2E) up -d --build --wait
	@status=0; \
	$(E2E) run --rm -w /repo/tools/synth go go test -tags e2e -count=1 -v ./e2e/... || status=$$?; \
	if [ $$status -ne 0 ]; then $(E2E) logs --tail=40 otel-collector overlay-api; fi; \
	$(E2E) down -v --remove-orphans >/dev/null 2>&1; \
	exit $$status

# Сборка окна идёт средствами Windows (cmd.exe из WSL): Tauri не собирает Windows-приложение под Linux.
overlay-build: ## Собрать окно оверлея на Windows (нужны Rust и MSVC, см. docs/windows-toolchain.md)
	cd overlay && cmd.exe /c "set PATH=%USERPROFILE%\.cargo\bin;%PATH% && npm ci --no-audit --no-fund && npx tauri build --no-bundle"

overlay-run: ## Запустить собранное окно оверлея
	cd overlay/src-tauri/target/release && (./cc-overlay.exe >/dev/null 2>&1 &)

overlay-dev: .env ## Интерфейс оверлея в браузере: http://127.0.0.1:1420 (нужен запущенный стек)
	$(COMPOSE) --profile dev run --rm --no-deps --service-ports overlay-dev

overlay-lock: .env ## Обновить overlay/package-lock.json после правки зависимостей
	$(NODE) npm install --no-audit --no-fund

verify-real: ## Проверить, что метрики настоящей сессии Claude Code дошли до Prometheus
	@bash tools/verify-real.sh

build: .env ## Собрать образы overlay-api и генератора синтетики
	$(COMPOSE) build overlay-api
	docker build -t $(SYNTH_IMAGE) tools/synth

synth: build ## Минута синтетики в рабочий стек (метка job="claude-code-synth")
	docker run --rm --network cc-overlay_default $(SYNTH_IMAGE) -endpoint otel-collector:4317

fmt: .env go-cache ## Отформатировать код Go
	$(GO_API) gofmt -w .
	$(GO_SYNTH) gofmt -w .

tidy: .env go-cache ## Обновить go.mod и go.sum обоих модулей
	$(GO_API) go mod tidy
	$(GO_SYNTH) go mod tidy
