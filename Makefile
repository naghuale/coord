GO_FILES := $(shell git ls-files --cached --others --exclude-standard '*.go')

.PHONY: help fmt check-format vet test build demo roles check test-telepathy lint test-telepathy-host

help:
	@echo 'make demo   — полный цикл задачи в локальном транспорте'
	@echo 'make roles  — пример динамической смены ролей'
	@echo 'make check  — форматирование, vet, race tests, сборка'
	@echo 'make build  — бинарники в bin/'
	@echo 'make fmt    — форматирование Go-кода'
	@echo 'make test-telepathy — изолированные live-тесты (нужен TELEPATHY_SERVER)'

fmt:
	gofmt -w $(GO_FILES)

check-format:
	@test -z "$$(gofmt -l $(GO_FILES))" || { gofmt -l $(GO_FILES); exit 1; }

vet:
	go vet ./...

test:
	go test -race -count=1 ./...

build:
	mkdir -p bin
	go build -o bin/coord ./cmd/coord
	go build -o bin/coord-demo ./cmd/coord-demo
	go build -o bin/coord-watchdog ./cmd/coord-watchdog
	go build -o bin/coord-telepathy ./cmd/coord-telepathy

demo:
	go run ./cmd/coord-demo

roles:
	go run ./examples/roles

check: check-format vet test lint
	go build ./...
	go mod tidy -diff

test-telepathy:
	@test -n "$(TELEPATHY_SERVER)" || { echo 'Set TELEPATHY_SERVER to Telepathy 0.8.0 dist/server.mjs'; exit 1; }
	mkdir -p bin
	go build -o bin/telepathy-experiment ./integration/telepathy
	TELEPATHY_SERVER="$(TELEPATHY_SERVER)" python3 integration/telepathy/run.py
	TELEPATHY_SERVER="$(TELEPATHY_SERVER)" python3 integration/telepathy/run_checks.py
	TELEPATHY_SERVER="$(TELEPATHY_SERVER)" python3 integration/telepathy/run_subprocess.py

lint:
	@if command -v golangci-lint >/dev/null 2>&1; then golangci-lint run ./...; else echo "golangci-lint not installed; optional lint skipped"; fi


test-telepathy-host:
	@test -n "$(TELEPATHY_SERVER)" || { echo "Set TELEPATHY_SERVER to Telepathy 0.8.0 server.mjs"; exit 1; }
	mkdir -p bin
	go build -o bin/coord-telepathy ./cmd/coord-telepathy
	TELEPATHY_SERVER="$(TELEPATHY_SERVER)" python3 integration/telepathy/standalone.py

.PHONY: test-sessions
test-sessions:
	mkdir -p bin
	go build -o bin/coord ./cmd/coord
	python3 integration/sessions/smoke.py
