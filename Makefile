.PHONY: help fmt check-format vet test build demo roles check test-telepathy

help:
	@echo 'make demo   — полный цикл задачи в локальном транспорте'
	@echo 'make roles  — пример динамической смены ролей'
	@echo 'make check  — форматирование, vet, race tests, сборка'
	@echo 'make build  — бинарники в bin/'
	@echo 'make fmt    — форматирование Go-кода'
	@echo 'make test-telepathy — изолированные live-тесты (нужен TELEPATHY_SERVER)'

fmt:
	gofmt -w .

check-format:
	@test -z "$$(gofmt -l .)" || { gofmt -l .; exit 1; }

vet:
	go vet ./...

test:
	go test -race -count=1 ./...

build:
	mkdir -p bin
	go build -o bin/coord-demo ./cmd/coord-demo
	go build -o bin/coord-watchdog ./cmd/coord-watchdog

demo:
	go run ./cmd/coord-demo

roles:
	go run ./examples/roles

check: check-format vet test
	go build ./...
	go mod tidy -diff

test-telepathy:
	@test -n "$(TELEPATHY_SERVER)" || { echo 'Set TELEPATHY_SERVER to Telepathy 0.8.0 dist/server.mjs'; exit 1; }
	mkdir -p bin
	go build -o bin/telepathy-experiment ./integration/telepathy
	TELEPATHY_SERVER="$(TELEPATHY_SERVER)" python3 integration/telepathy/run.py
	TELEPATHY_SERVER="$(TELEPATHY_SERVER)" python3 integration/telepathy/run_checks.py
	TELEPATHY_SERVER="$(TELEPATHY_SERVER)" python3 integration/telepathy/run_subprocess.py
