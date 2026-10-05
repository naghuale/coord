# Быстрый старт

Нужны Go 1.23 или новее и Git. Для `make` нужен установленный Make.
Разрабатывалось и проверялось на macOS; process lock реализован для Unix.

Все команды ниже выполняются из корня репозитория `coord`.

## 1. Посмотреть работу задачи

```sh
go run ./cmd/coord-demo
```

Ожидаемый вывод:

```text
CF-009-run-1: DONE (snapshot persisted)
```

Демо создаёт coordinator и тестового исполнителя, отправляет TASK, принимает
ACK, STARTED и RESULT_READY, сохраняет RESULT и переводит run в DONE.
Snapshot находится в собственной временной папке и удаляется после завершения.
Внешняя сеть, Telepathy и реальные агентские сессии для демо не нужны.

## 2. Посмотреть смену ролей

```sh
go run ./examples/roles
```

Ожидаемый вывод:

```text
before: revision=1 executor=provider/model-v1
after: revision=2 executor=provider/model-v2 capabilities=[go]
auditor: profile=provider/audit-model capabilities=[security_audit]
reviewer: removed
audit events: 1
```

Пример добавляет AUDITOR, удаляет REVIEWER и меняет профиль EXECUTOR.
Capabilities исполнителя сохраняются. Для себя пример создаёт временный registry.
Проверку независимости аудитора можно запустить отдельно:

```sh
go test -v . -run '^TestRolesIndependenceAndImmutableAssignment$'
```

## 3. Проверить и собрать

```sh
make check
make build
./bin/coord-demo
./bin/coord-watchdog -help
```

Без Make:

```sh
gofmt -l .
go vet ./...
go test -race -count=1 ./...
go build ./...
go mod tidy -diff
```

`gofmt -l .` и `go mod tidy -diff` при исправном проекте ничего не выводят.
Бинарник watchdog запускается отдельно от наблюдаемых исполнителей; для рабочего
запуска ему нужны control API, token и три supervisor executable.
Настройка описана в [эксплуатации](OPERATIONS.md).

## 4. Посмотреть отдельные сценарии

```sh
# Потерян ACK: retry сохраняет attempt и owner.
go test -v . -run '^TestAckRetryPreservesAttemptAndDeduplicates$'

# Heartbeat пропал: PONG не считается рабочим прогрессом.
go test -v . -run '^TestHeartbeatMissPongDoesNotAdvanceFSM$'

# Перезапуск coordinator: outbox остаётся на диске.
go test -v . -run '^TestOutboxReplayAfterRestart$'

# Recovery: второй watchdog не получает claim, старый owner отклоняется.
go test -v . -run '^TestRecoveryCASAndOldOwnerRejected$'

# Watchdog не запускает замену до подтверждения остановки writer.
go test -v ./watchdog -run '^TestRecoveryOrdering$'

# Одновременно изменить одну revision может только один запрос.
go test -v ./roles -run '^TestConcurrentRevisionCAS$'
```

Эти команды запускают воспроизводимые тестовые сценарии, а не production chaos-тесты.
