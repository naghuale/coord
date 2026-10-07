# coord

Самостоятельный Go-модуль координации агентов. Зависимости — только stdlib.
Транспорт, процесс исполнения и supervision подключаются через явные интерфейсы.

Самостоятельный Telepathy host/worker: `make build`, затем `bin/coord-telepathy -help`.
Настройка, подписи и проверка: [TELEPATHY_HOST.md](docs/TELEPATHY_HOST.md).

[Текущая рабочая версия и её проверки](docs/CURRENT_VERSION.md).

## Ролевые сессии

`make build`, затем `bin/coord prompt` — общий вход AI-агента.
`bin/coord --state state/team --server none rooms` запускает локальный daemon.
Комнаты, owner, оба входа, checkpoint, generation и Git-история:
[SESSION_CLI.md](docs/SESSION_CLI.md).

Шаблоны Executor, Orchestrator, Auditor и Reviewer: [ROLE_PROMPTS.md](docs/ROLE_PROMPTS.md).

## Документация

- [Быстрый старт и примеры команд](docs/QUICKSTART.md)
- [Инструкция агенту: Telepathy и CLI/subprocess](docs/AGENT_INSTRUCTIONS.md)
- [Проверенные сценарии и границы отладки](docs/VERIFICATION.md)
- [Независимый аудит удобства и наблюдаемости](docs/UX_AUDIT.md)
- [Архитектура и жизненный цикл](docs/ARCHITECTURE.md)
- [Telepathy, plugin и уровни совместимости](docs/TRANSPORT.md)
- [Настройка, control API и эксплуатация](docs/OPERATIONS.md)
- [Разработка, проверки и Git](docs/DEVELOPMENT.md)
- [Дальнейшее развитие](docs/ROADMAP.md)
- [Реальные Telepathy-эксперименты P0–P3](docs/EXPERIMENTS.md)
- [История изменений](CHANGELOG.md) и [авторство](AUTHORS.md)

## Запуск и проверка

```sh
cd coord
go run ./cmd/coord-demo
go test -race -count=1 ./...
go vet ./...
go build ./...
```

Из корня проекта также доступны `make demo`, `make roles`, `make check`,
`make build`. Бинарники создаются в `bin/`, runtime state хранится вне Git.

Демо выполняет `TASK → ACK → STARTED → RESULT_READY → RESULT → RESULT_ACK → DONE` через
in-process транспорт, сохраняя snapshot в собственной временной папке.
Это проверка ядра, а не интеграция с действующими сессиями CF-009.

## Компоненты

| Пакет | Назначение |
|---|---|
| `envelope` | Строгое декодирование, типизированные payload, полные SHA, UUID, Ed25519 |
| `transport` | Интерфейсы `Transport`/`Client`, byte adapter, подписанные frames |
| `fsm` | Разрешённые переходы, запрет раннего RESULT, обработка FAILURE/PARTIAL |
| `store` | Изолированные копии, mutex, публикация состояния после durable commit |
| `checkpoint` | JSON snapshot, temp + fsync + rename + fsync directory, права 0600 |
| `retry` | Экспоненциальный backoff, jitter и верхний предел задержки |
| `executor` | Проверка HEAD, ACK, progress/result checkpoint, retry, replay |
| `watchdog` | Recovery claim, fencing, проверка HEAD, spawn, CAS transfer |
| `roles` | Стабильные identities, роли, capabilities, атомарные обновления и журнал |
| корневой пакет | Координатор, durable outbox, таймауты, метрики, supervisor HTTP API |

## Подключение к приложению

Создайте `store.New(checkpoint.File{Path: "state/runs.json"})`, затем
`coord.New(tp, store, coord.DefaultConfig())`. Запустите `Coordinator.Run(ctx)`;
используйте `Assign(ctx, fsm.Run{...})` для назначения задач. `Attempt` и `Fence`
начинаются с 1; для существующего attempt можно указать его номер. `TargetSHA`
содержит полный SHA, `Deadline` — абсолютный дедлайн. После остановки Run вызовите
`Store.Close()` и `Registry.Close()` для подключённого реестра ролей.

На Unix `Store.New` автоматически удерживает kernel flock snapshot до Close:
два координатора с одним canonical snapshot одновременно не запускаются.
Один coordinator владеет своим run snapshot и role registry. На других ОС
process lock необходимо предоставить реализацией `Persistence.Lock`.

Byte client реализует `Send(ctx, session, raw)` и `Incoming(ctx)`. Передайте его
в `transport.Adapter{Client: client, Session: session}`. `Send` должен соблюдать
context и возвращать успех после подтверждения доставки транспортом.
`Receive` вызывается один раз. Закрытие incoming останавливает координатор.
Для ошибок декодирования задайте `Malformed: func(){c.Metrics.Inc("malformed_messages_total")}`.

Пакет `coord/telepathy` реализует byte interface через проверенный MCP stdio API
Telepathy 0.8.0. Это наш adapter, а не официальный SDK. Подписанный standalone
host/worker и его process-level тест описаны в [TELEPATHY_HOST.md](docs/TELEPATHY_HOST.md).

## Семантика доставки и восстановления

* TASK и его outbox записываются **до** отправки. При рестарте outbox повторяется
  с прежним message_id. Это at-least-once доставка; executor обязан устранять
  повторное выполнение по `(task, run, attempt, fence)`, а не только message_id.
* ACK timeout вызывает STALLED и ограниченный retry. Протокольный retry получает
  новый message_id, сохраняя attempt, SHA, fence и owner. Старый корректный ACK
  принимается до recovery claim. Transport retry сохраняет message_id.
* Heartbeat timeout вызывает STALLED и PING с nonce/message correlation. PONG
  подтверждает канал, но не заменяет HEARTBEAT и не переводит run в STARTED.
* После исчерпания retry выставляется `RecoveryNeeded`; новый writer автоматически
  не назначается. Поздний корректный heartbeat может восстановить run до claim.
* Claim атомарен и durable. Пока claim занят, сообщения прежнего owner не меняют
  состояние. Claim не истекает автоматически: после падения watchdog оператор
  сначала проверяет остановку supervisor и writer, затем освобождает claim.
* Watchdog подтверждает остановку старого writer, проверяет HEAD и `git diff --check`,
  запускает замену, затем делает CAS transfer. Attempt и SHA сохраняются, fence
  увеличивается, owner меняется. Новая сессия сначала отвечает ACK.
* FAILURE и PARTIAL сохраняются с outcome и артефактами, завершая run как FAILED.
  Общий deadline завершает run как FAILED; остановка рабочего процесса остаётся
  обязанностью supervisor, а не следствием изменения FSM.

Подписанные сообщения аутентифицируются через
`transport.SignedClient{Client: rawClient, PrivateKey: key, Peers: pinnedPublicKeys}`.
Ключи поступают из доверенного реестра. Подпись покрывает SHA в сообщении, но не
доказывает содержимое диска или фактическую модель провайдера.

## Исполнитель

`executor.Executor` настраивается с transport, отдельным checkpoint файлом,
retry policy, `UpdateInterval`, `SendTimeout`, `Now: time.Now`.
`Accept` сверяет HEAD; ACK, heartbeat и RESULT сохраняются в checkpoint вместе
с изменением состояния до первой отправки. Outbox отправляется в порядке записи:
неотправленная готовность результата предшествует RESULT. Доставленные ACK и
heartbeat удаляются; RESULT остаётся до correlated RESULT_ACK координатора.
После отказа транспорта checkpoint содержит `BLOCKED/TELEPATHY_UNAVAILABLE`
и outbox, но `RESULT_READY` сохраняется как необратимое состояние.
После reconnect той же сессии можно вызвать `Replay`. Просроченные ACK и
heartbeat получают новый message_id и next_update_by, сохраняемые до отправки;
transport retries используют неизменное сообщение. Успешный replay снимает
transport blocker и восстанавливает состояние приложения.
Новая сессия принимает TASK с новым fence: прогресс сохраняется, outbox,
результат и blocker старого writer очищаются.

Владелец executor loop должен сериализовать вызовы, удерживать исключительный
worktree lock, проверять AssignedRole и фактический профиль, прекращать работу
при ABORT/потере lease, а при неудачном reconnect завершать процесс для supervisor.
Повторный TASK не должен запускать работу второй раз. Локальный RESULT остаётся
RESULT_READY до обработки RESULT_ACK через executor.Receipt: transport delivered
не является таким подтверждением. Здесь нет автоматического исполнения кода задачи.

## Роли и независимость аудита

Создайте `roles.New("state/roles.json")`, зарегистрируйте доверенные
`agent_id → session` через `Register`, присвойте registry в `c.RoleRegistry`
до запуска Run. Несколько сессий одного агента регистрируются с одинаковым agent_id.
Каждое назначение теперь требует `RequiredRole: roles.Requirement{Role: ..., Model: ...,
Capabilities: ...}`. Model необязателен; все требуемые capabilities обязательны.

COORDINATOR управляет workflow; EXECUTOR выполняет работу; REVIEWER даёт feedback;
AUDITOR выполняет независимую проверку. REVIEWER может принадлежать той же стороне,
что EXECUTOR. AUDITOR не может иметь тот же agent_id, что любой EXECUTOR этой задачи,
включая прежних владельцев после recovery. Запрет действует и в обратном порядке:
бывший AUDITOR не становится EXECUTOR той же задачи. Это проверка identities;
для организационной независимости нужен дополнительный policy layer.

Пример wire update (остальные обязательные поля header опущены):

```json
{
  "type": "ROLE_UPDATE",
  "role_update_payload": {
    "revision": 1,
    "added_roles": [{"role": "AUDITOR", "profile": "provider/audit-model", "capabilities": ["security_audit"]}],
    "removed_roles": ["REVIEWER"],
    "updated_roles": [{"role": "EXECUTOR", "profile": "provider/new-model"}]
  }
}
```

`revision` — текущая версия записи агента, CAS предотвращает lost updates.
Все операции применяются вместе; при ошибке ни одна не сохраняется. Пропущенное
поле capabilities сохраняет прежние значения, пустой массив очищает их.
Успех возвращает ROLE_ACK с in_reply_to и новой revision. Повтор message_id — no-op.
Журнал `Registry.Events()` хранится в том же snapshot. Registry удерживает kernel lock
до Close; второй writer того же файла отклоняется. Активный run и отправленный
TASK сохраняют снимок профиля/revision; ROLE_UPDATE влияет на будущие назначения.
Capabilities и названия моделей остаются заявлениями агента, не аттестацией.

## Внешний watchdog

Разместите `c.ControlHandler(token)` на отдельном HTTP listener и передайте token
watchdog через окружение `COORD_CONTROL_TOKEN`. Для удалённого доступа используйте TLS.

```sh
go run ./cmd/coord-watchdog \
  -coordinator http://127.0.0.1:8090 \
  -fence-command /path/to/fence-writer \
  -spawn-command /path/to/spawn-session \
  -stop-command /path/to/stop-session
```

Команды запускаются без shell; timeout каждой — 30 секунд:

* fence получает `old_session run fence`; код 0 означает подтверждённую остановку
  процесса и освобождение writer lock, а не только отправленный сигнал.
* spawn получает `run attempt target_sha worktree next_fence`, выводит один адрес
  сессии. Процесс ожидает TASK перед записью; при включённых ролях supervisor
  предварительно регистрирует его с доверенным agent_id и подходящим профилем.
* stop получает адрес replacement и подтверждает завершение процесса и освобождение
  writer lock. Ошибка transfer может означать потерянный ответ после commit:
  stop должен работать и для replacement, уже получившего TASK.
  Если stop не подтверждён, watchdog сохраняет recovery claim для ручной проверки,
  не разрешая новый recovery того же поколения. Неудачные sweeps логируются.

Snapshot исполнителя и канонический snapshot координатора — разные файлы.
Произвольные незакоммиченные правки допустимы как progress; проверка HEAD и diff
не доказывает отсутствие повреждений. Проверку checkpoint/artifact hashes добавляет
supervisor для своего формата артефактов.

## Метрики и границы

`c.Metrics` — HTTP handler формата Prometheus. Есть inbound/outbound по kind,
ACK/delivery histograms, heartbeat misses/received, stalls, rejected/duplicate
messages, split-brain, watchdog restarts, malformed envelopes и FSM errors.
ACK latency измеряется от первого известного delivery до ACK. Метрики в памяти
сбрасываются при рестарте; канонические runs и role events сохраняются.

Примеры алертов приведены в `prometheus-alerts.yml`. Ядро протестировано с fake
clock/transport, HTTP supervisor и disk snapshots. Демо не является production
chaos-тестом Telepathy. Файловое хранилище сохраняет весь snapshot при изменении;
для тысяч runs следует заменить его на transactional database и разгрузить
последовательные transport sends. Распределённый multi-coordinator consensus,
автоматическое истечение recovery claims и аттестация моделей не реализованы.

RESULT_ACK расширяет локальный schema_version=1: обновляйте обе стороны вместе.
Регрессия progress и более старый timestamp heartbeat внутри fence отклоняются.
Это не заменяет sequence/epoch protocol при равных timestamps и clock skew.
Unix-пример `go run ./examples/agent` подключает CLI через stdin/stdout и
проверяет полный lifecycle; ограничения и команда запуска — в инструкции агенту.

## Отдельные будущие планы

[Интеграция с Crewflow](docs/plans/crewflow/INTEGRATION_TASK.md) — отдельная
не реализованная задача; она не требуется для текущего standalone запуска.
