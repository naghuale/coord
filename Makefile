.PHONY: help fmt check-format vet test build demo roles check

help:
	@echo 'make demo   — полный цикл задачи в локальном транспорте'
	@echo 'make roles  — пример динамической смены ролей'
	@echo 'make check  — форматирование, vet, race tests, сборка'
	@echo 'make build  — бинарники в bin/'
	@echo 'make fmt    — форматирование Go-кода'

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
