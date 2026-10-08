# Исторический срез Coord на 2026-10-07

## Статус документа (R102)

Этот документ сохраняет описание и результаты проверки локального варианта,
зафиксированного 2026-10-07. Он исторический и не описывает текущую каноническую
модель Coord: после завершения P0-2 2026-10-08 Task, Run, DomainEvent, Inbox и
Outbox хранятся в canonical snapshot `coordd` (коммиты `be7820a`, `f280440`,
`66c0ddf`, `8eb6e38`). Для текущих границ архитектуры и оставшейся работы
используйте [ARCHITECTURE.md](ARCHITECTURE.md) и [ROADMAP.md](ROADMAP.md).

Проверено 2026-10-07. Это самостоятельный локальный Go-проект.
Рабочий lifecycle не требует установки, состояния или API Crewflow.

## Ролевой слой

`cmd/coord` — самостоятельный локальный daemon и CLI. Комнаты, owner, сессии,
оба входа, checkpoints и Git-проекция описаны в [SESSION_CLI.md](SESSION_CLI.md).
`make test-sessions` проверяет процессный restart без моделей; реальный AI отдельно.

## Что запускать

- `examples/agent`: локальный CLI/subprocess host с in-process transport.
- `cmd/coord-telepathy`: самостоятельные host и worker через установленный
  Telepathy 0.8.0 MCP stdio, с pinned Ed25519 keys.
- [TELEPATHY_HOST.md](TELEPATHY_HOST.md): настройка host/worker.
- [AGENT_INSTRUCTIONS.md](AGENT_INSTRUCTIONS.md): инструкция агенту.

Worker запускает явно настроенный executable, передаёт prompt в stdin и работает
в выделенном Git worktree. Он сохраняет TASK/результат и принимает завершение
только после correlated RESULT_ACK. Произвольная живая AI-сессия автоматически
не подключается: её CLI/API и неинтерактивные аргументы настраиваются отдельно.

## Проверки этой версии

```sh
make check
git diff --check
make test-telepathy-host TELEPATHY_SERVER=/absolute/path/to/telepathy/0.8.0/dist/server.mjs
```

Получены PASS:

- форматирование, go vet, полный go test -race -count=1 ./...;
- установленный golangci-lint: 0 issues; сборка; go mod tidy -diff;
- подписанный lifecycle через реальные отдельные процессы Telepathy;
- SIGKILL и restart host во время работы команды;
- DONE и пустой outbox у host и worker после RESULT_ACK;
- перезапуск завершённого worker без повторного исполнения;
- потеря receipt и повтор сохранённого RESULT без повторного исполнения (fake transport);
- correlated PING/PONG, отказ чужому worktree, отказ ambiguous restart;
- обработка MCP isError, несовместимой версии, cancellation и archive replay.

Последний live evidence:
`state/telepathy-standalone/1791323824254774000/report.json`.
Тест использовал отдельный TELEPATHY_HOME, тестовые keys/anchors, отдельный
Git-репозиторий и тестовую subprocess-команду. Пользовательские AI-сессии и
рабочие задачи не запускались. Real model: false.

## Практические ограничения

Unix process groups и advisory writer lock; команда не должна daemonize/setsid.
После прерывания до durable RESULT автоматический rerun запрещён.
Автоматический fencing/spawn supervisor внешних writers отсутствует.
Ролевой daemon обслуживает несколько комнат отдельно от run lifecycle.
У Telepathy есть rate/duplicate limits и только ограниченный archive replay;
детали и интервалы указаны в TELEPATHY_HOST.md.
Exit code 0 агента означает результат subprocess, а не аудит качества его работы.
Power-loss и произвольные внешние side effects этим набором не доказаны.

Документ фиксирует локальный вариант; будущие интеграционные планы не являются
его требованиями. Результаты проверок — VERIFICATION.md.
