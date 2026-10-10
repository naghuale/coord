# Исторический срез Coord на 2026-10-07

> **Исторический документ.**
>
> Этот файл MUST NOT использоваться как:
>
> - текущая инструкция запуска;
> - источник активной версии Telepathy;
> - источник текущих путей, assignments или session configuration;
> - основание для readiness, recovery или process control;
> - источник текущей архитектурной зрелости.
>
> Для текущей работы используются `AGENTS.md`, `.coord/project.yaml`,
> `operations/project/CURRENT.yaml`, `operations/project/OPEN-WORK.yaml`,
> `ARCHITECTURE.md`, `TARGET-ARCHITECTURE.md` и актуальный assignment.

## Статус документа (R102)

Этот документ сохраняет описание и результаты проверки локального варианта,
зафиксированного 2026-10-07. Он исторический и не описывает текущую каноническую
модель Coord.

Актуальный статус реализации ведётся в
[architecture/COMPONENT-STATUS.yaml](architecture/COMPONENT-STATUS.yaml),
[ARCHITECTURE.md](ARCHITECTURE.md) и
[TARGET-ARCHITECTURE.md](TARGET-ARCHITECTURE.md).

Проверено 2026-10-07. Это самостоятельный локальный Go-проект.
Рабочий lifecycle не требует установки, состояния или API Crewflow.

## Позднейшая пометка

После этого среза основные canonical-компоненты P0-2 были реализованы:
Task, Run, DomainEvent, Inbox и Outbox вошли в canonical snapshot `coordd`.
Это не означает закрытие всей границы P0-2. Согласно ROADMAP, Shadow Validation
и приёмка границы этапа оставались открытыми.

Коммиты, относящиеся к этому расширению: `be7820a`, `f280440`, `66c0ddf`, `8eb6e38`.

Эта пометка предназначена только для навигации и не является текущей
архитектурной картой.

## Ролевой слой

В историческом срезе `cmd/coord` являлся самостоятельным локальным legacy daemon/CLI
ролевого и rooms-контура. Он не является текущим canonical `coordd`. Комнаты, owner,
сессии, оба входа, checkpoints и Git-проекция описаны в [SESSION_CLI.md](SESSION_CLI.md).
`make test-sessions` проверяет процессный restart без моделей; реальный AI отдельно.

## Что запускалось

- `examples/agent`: локальный CLI/subprocess host с in-process transport.
- `cmd/coord-telepathy`: самостоятельные host и worker через установленный
  Telepathy 0.8.0 MCP stdio, с pinned Ed25519 keys.
- [TELEPATHY_HOST.md](TELEPATHY_HOST.md): настройка host/worker.
- [AGENT_INSTRUCTIONS.md](AGENT_INSTRUCTIONS.md): инструкция агенту.

Версия Telepathy 0.8.0 относится только к этому историческому срезу и не является
разрешением обновлять, запускать или использовать её в текущих сессиях. Текущая
версия и compatibility status определяются активной проектной конфигурацией
и открытыми Issues.

Worker запускал явно настроенный executable, передавал prompt в stdin и работал
в выделенном Git worktree. Он сохранял TASK/результат и принимал завершение
только после correlated RESULT_ACK. Произвольная живая AI-сессия автоматически
не подключалась: её CLI/API и неинтерактивные аргументы настраивались отдельно.

## Проверки этой версии

На историческом срезе выполнялись:

```sh
make check
git diff --check
make test-telepathy-host \
  TELEPATHY_SERVER=/absolute/path/to/telepathy/0.8.0/dist/server.mjs
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

Во время проверки evidence находилось по локальному пути
`state/telepathy-standalone/1791323824254774000/report.json`.
Этот путь не является переносимым доказательством и может отсутствовать
в текущем окружении. Результат не должен использоваться как актуальная
runtime attestation.

Тест использовал отдельный TELEPATHY_HOME, тестовые keys/anchors, отдельный
Git-репозиторий и тестовую subprocess-команду. Пользовательские AI-сессии и
рабочие задачи не запускались. Реальная AI-модель в проверке не участвовала:
использовалась тестовая subprocess-команда.

## Практические ограничения

Unix process groups и advisory writer lock; команда не должна daemonize/setsid.
После прерывания до durable RESULT автоматический rerun запрещён.
Автоматический fencing/spawn supervisor внешних writers отсутствует.
Ролевой daemon обслуживал несколько комнат отдельно от run lifecycle.
У Telepathy есть rate/duplicate limits и только ограниченный archive replay;
детали и интервалы указаны в [TELEPATHY_HOST.md](TELEPATHY_HOST.md).
Exit code 0 агента означает результат subprocess, а не аудит качества его работы.
Power-loss и произвольные внешние side effects этим набором не доказаны.

Документ фиксирует локальный вариант; будущие интеграционные планы не являются
его требованиями. Результаты проверок — [VERIFICATION.md](VERIFICATION.md).
