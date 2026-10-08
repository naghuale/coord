# P0-1: Production Daemon Runtime Design

- **Статус:** Accepted for implementation
- **Область:** executable, configuration и process lifecycle текущего Run runtime
- **Нормативные источники:** ADR-0001 (шаги 6–7, 11), ADR-0004 (RT-01…RT-06),
  ADR-0005 (ST-01…ST-06), `TASK-MODEL.md`, `TARGET-ARCHITECTURE.md`, `ROADMAP.md`
- **Назначение:** deployment design по ADR-0004 RT-01; решения о runtime и
  storage остаются в ADR-0004/0005

Этот документ фиксирует текущую реализацию и целевую границу P0-1. Решения
владельца от 2026-10-08 (R38) закрывают вопросы executable, config, transport и
порядка захвата state namespace; реализация обязана соблюдать их acceptance.

## 1. Startup и deployment

### Ответственность

ADR-0004 Process Ownership возлагает запуск и остановку daemon на внешний
supervisor. Оператор задаёт deployment/configuration; supervisor запускает
процесс и передаёт ему сигнал остановки. Coord не управляет собственным
процессом и не запускает процессы агентов (ADR-0004 RT-02, Process Ownership).

### Текущий executable

Самостоятельного production daemon executable раньше не было. `cmd/coord` — CLI и
localchat host; `cmd/coord-telepathy` — отдельно настроенный одно-run host или
worker. Пакет `daemon` является библиотечным host.

Production executable реализован как отдельный `cmd/coordd`; подкоманда
`coord daemon` не вводится. Это отделяет lifecycle production runtime от CLI и
localchat host.

Runtime config задаётся одним schema-versioned файлом; флаг передаёт только путь
к этому файлу. Отсутствующая или неизвестная schema version блокирует запуск,
флагов-переопределений нет. Это оставляет конфигурацию проверяемой как единый
версионированный контракт.

### Transport requirement

Production agent transport обязателен для `coordd`. Если transport не
сконфигурирован, процесс завершается с понятной ошибкой до захвата namespace
lock; daemon не удерживает lock и не остаётся в `not-ready`. Это не позволяет
производственному daemon выглядеть запущенным без канала координации.
Ошибки до lock имеют exit 2 и сообщают `ownership: not acquired` и `namespace
lock: not acquired`; ошибка запуска/проверки Telepathy после lock имеет exit 1
и сообщает `namespace lock: released` после cleanup. Поддерживаемый production
kind сейчас только `telepathy`.

## 2. Build и wiring runtime

Сегодня `daemon.New` принимает уже готовый `*coord.Coordinator` и конфигурацию
listeners. Coordinator уже владеет Store и RoleRegistry. Store открывает
Persistence, берёт доступный у неё process lock, затем загружает состояние
(`store/store.go:31-57`). `coord.New` требует готовые Transport и Store и
проверяет загруженные Runs (`coordinator.go:88-109`). `daemon.New` валидирует
loopback listeners и связывает handlers (`daemon/daemon.go:53-79`). Поэтому
daemon package сам не строит runtime из конфигурации.

Целевая composition boundary P0-1:

```text
validated schema-versioned config (path flag only; no overrides)
  → validate required Telepathy config and key files before side effects
  → namespace ownership
  → Store
  → RoleRegistry
  → start Telepathy and verify session
  → Coordinator
  → daemon host / listeners
  → Run
```

Это composition boundary реализует authority boundary и единый write path
(ADR-0004 RT-02, RT-04). `telepathy.OpenTransport` — общая factory для `coordd`
и standalone host: она запускает MCP process, проверяет `list_peers`, создаёт
signed Client и Adapter; close function владеет `Process.Close`. Так не возникает
второй копии connect sequence.

Порядок закрытия обратный: остановить приём новых запросов, остановить HTTP и
Coordinator loop, закрыть transport resources, затем RoleRegistry/Store и
освободить namespace ownership. Любая ошибка сборки закрывает уже созданные
ресурсы; readiness не публикуется. Этот порядок реализации должен учитывать,
что Store/RoleRegistry сегодня сами получают lock при создании: нельзя создать
частично работающий host, который обслуживает запросы после ошибки следующей
инициализации.

`coordd` принимает только `transport.kind=telepathy`; test/null transport в
production config отсутствует. Неподдержанный kind и статически неполная
конфигурация отклоняются с exit 2 до namespace lock. Запуск MCP и проверка его
реальной версии/сессии выполняются после lock; отказ закрывает уже открытые
ресурсы, возвращает exit 1 и сообщает, что ownership освобождён. Подключение
Production CLI и Telepathy agent writes к Task command path — P0-3, ADR-0004 RT-04,
ADR-0001 шаг 11; доставка не
равна acceptance.

## 3. Single owner и state namespace

RT-01 требует отказ второму daemon до обслуживания запросов; ST-02 уточняет
exclusive process lock на весь state namespace. Перед открытием Store и
RoleRegistry `coordd` захватывает одну namespace lock; затем P0-2 composition
получает compatibility lock legacy `runs.json`, canonical Store захватывает lock
`canonical.json`, после чего открывается RoleRegistry. При обычном отказе startup
освобождает уже взятые locks в обратном порядке и не начинает serve. Если
canonical state или legacy run делает startup not-ready, P0-2 удерживает namespace
lock и запускает только diagnostic runtime согласно `p0-2-task/DESIGN.md` §7;
при занятом legacy lock этот runtime не читает snapshot. File-level locks компонентов
остаются дополнительной защитой. Отсутствие обязательного transport
проверяется до этой lock, как определено в R38. Unix snapshot lock использует
kernel `flock`; процессное завершение освобождает его, lock file может остаться.
На неподдерживаемой платформе checkpoint lock возвращает `ErrUnavailable`, и
store отказывает fail-closed.

В текущем коде `store.New` удерживает lock Persistence до `Store.Close`
(`store/store.go:31-71`). Run snapshot и RoleRegistry имеют разные lock-и и
открываются отдельно. P0-1 executable должен гарантировать, что все требуемые
части runtime открыты и удерживаются до запуска HTTP/loop; второй процесс,
который не получил любую обязательную часть ownership, завершает startup до
serve. Текущие проверки Store/RoleRegistry по отдельности не доказывают
атомарное владение единым state namespace — это проверяемый gap реализации.

В P0-1 acceptance нужен второй реальный OS-процесс на том же namespace:
первый удерживает ownership, второй не становится ready и не принимает
изменяющие запросы. Проверка только второго Store в том же тестовом процессе не
заменяет этот сценарий. Отдельный тест `TestCoorddTakesNamespaceLockBeforeStores`
проверяет, что namespace lock захвачена прежде любых file-level lock Store и
RoleRegistry.

P0-2 добавляет временный compatibility lock для legacy `runs.json`. `coordd`
берёт его после namespace lock и до lock `canonical.json`, удерживает до
shutdown, включая запуск на пустом или терминальном legacy snapshot. Это
закрывает гонку, в которой legacy `localchat` или standalone host мог бы открыть
прежний Run Store уже после readiness нового daemon. Если lock занят, `coordd`
остаётся диагностически `NOT_READY`, не читает legacy файл и не запускает
mutations/transport. Этот
compatibility lock удаляется только после перевода production writers на daemon
command path в P0-3.

## 4. Durable boundary и P0-2

ST-01 требует одну durable фиксацию `Canonical State + Domain Event + Outbox`
(и Inbox receipt, когда нужен для дедупликации). ST-03 запрещает публикацию
памяти до успешного commit; при неопределённой ошибке дальнейшие записи
запрещаются до reopen/recovery.

Текущая `store.Persistence` сохраняет только `map[RunID]Run`
(`store/store.go:14-21`). `Store.Update` сериализует мутации, вызывает Save и
публикует память после успеха; ошибка Save переводит Store в failed state, так
как snapshot мог быть переименован до ошибки sync (`store/store.go:106-132`).
Это текущая Run-only граница, а не готовая транзакция Task/Event/Outbox.

P0-1 не создаёт фиктивные Task/Event/Outbox записи. P0-2 расширяет canonical
document до Task aggregate, Task.Fence, Events и Outbox и должен обеспечить
их одну атомарную транзакцию по ADR-0001 шаг 7 и ADR-0005 ST-01. Ошибка
durable commit в P0-1 прекращает runtime; daemon не продолжает со старой
in-memory копией. Неопределённый исход требует повторной загрузки и проверки,
а не слепого повтора команды.

## 5. Startup, readiness и control API

Целевая RT-03 последовательность:

1. Получить exclusive ownership state.
2. Загрузить durable state, проверить версии схемы и инварианты.
3. Восстановить runtime indexes и вычисляемые представления.
4. Восстановить durable Outbox и незавершённые delivery/recovery operations.
5. Проверить отсутствие конфликтующих active writer Runs на Task и
   согласованность `Task.Fence` с Run fence snapshots.
6. Завершить recovery, которому нужно внешнее доказательство остановки старого
   writer, через supervisor.

Обычные изменяющие операции и ready должны оставаться закрыты до выполнения
всех применимых проверок. По RT-03 recovery control endpoints могут быть
доступны в состоянии not-ready после инициализации transport receive, чтобы
supervisor завершил stop-proof; это исключение не открывает обычные mutation
commands.

Текущая реализация (`daemon/daemon.go`):

- `New` связывает `/readyz`, control handler и metrics handler (`:50-77`).
- `Run` выполняет startup `Tick`, затем запускает listeners и loop
  (`:131-156`).
- Readiness сейчас проверяет только `transportReady` и
  `Coordinator.RecoveryPending()` (`:97-124`); причины —
  `transport_receive_not_initialized` и `supervisor_recovery_pending`.
- Control handler доступен после инициализации receive через
  `afterTransportReady` (`:116-125`), даже если `RecoveryPending()` оставляет
  `/readyz` закрытым. Сейчас это соответствует RT-03: `GET /runs` показывает
  только Runs, ожидающие supervisor recovery, а `POST /claim`, `/release`,
  `/transfer`, `/fail` выполняют recovery operations, защищённые проверками
  `RecoveryNeeded` и `Status == Stalled` (`control.go:27-61`, `supervisor.go`).
  `/fail` — recovery outcome под claim; он входит в recovery allowlist (R33).
  Обычных mutation commands в этом control API пока нет; Assign проходит через
  localchat host. Когда P0-1/P0-2 добавит первую обычную daemon command,
  маршруты и методы нужно классифицировать явно: recovery allowlist доступна
  при not-ready после transport receive, остальные закрыты до ready (RT-03,
  RT-04). `TestDaemonReadinessKeepsMutationsClosedDuringRecovery` защищает это
  будущее правило и должен падать, если новый маршрут не классифицирован.
- `checkpoint.validateRuns` и `coord.New` проверяют только существующие Run
  поля. Полные schema/reference/FSM проверки, Outbox replay, Task.Fence,
  single-writer-per-Task и Task-scoped recovery отсутствуют, потому что Task
  model ещё не реализована (ADR-0005 Context/ST-04; ADR-0004 RT-03).

P0-1 readiness может честно означать только готовность текущего Run runtime.
Она не называется полной RT-03 readiness. P0-2 добавляет канонические проверки;
production readiness закрывается только после всех применимых шагов 1–6.

## 6. Crash/restart и process-level acceptance

После `kill -9` ОС освобождает kernel lock. Следующий daemon может получить
ownership, но сам факт получения lock не доказывает, что внешний agent writer
остановлен. Перед продолжением новый процесс загружает и валидирует durable
snapshot; выполняет recovery/outbox действия, предусмотренные текущей моделью;
запрашивает у supervisor stop proof для recovery, которому он необходим; затем
только открывает readiness. P0-1 проверяет replay существующего Run outbox до
readiness; Task.Fence и атомарность canonical Task/Event/Outbox относятся к
P0-2, поэтому P0-1 не заявляет эти гарантии.

P0-1 process acceptance tests:

1. `TestCoorddProcessKillReleasesNamespaceAndRestarts`: реальная coordd
   composition в child достигает readiness, получает SIGKILL, затем новая
   composition принимает тот же namespace и достигает readiness. Это проверяет
   kernel lock из ST-02, не stop proof внешнего agent writer из RT-03/RT-06.
2. `TestCoorddSecondProcessCannotOpenOwnedNamespace`: второй coordd child на том
   же namespace получает отказ на namespace lock до открытия Store/RoleRegistry;
   durable snapshots остаются побайтно неизменными.
3. `TestCoorddProcessRestartRestoresAndReplaysOutboxBeforeReady`: после SIGKILL
   новый coordd восстанавливает существующий Run и доставляет сохранённый Task
   outbox до `/readyz`; Attempt и Fence остаются прежними. Это проверяет только
   существующий Run Outbox, не canonical Task/Event/Outbox из P0-2. Transport
   delivery не означает application acceptance (ADR-0001).
4. `TestCoorddProcessRestartPreservesResultAndReceipt`: новый coordd доставляет
   сохранённый RESULT_ACK до readiness; после штатной остановки проверяются
   terminal Result и прежние Attempt/Fence.
5. `TestDaemonReadinessKeepsMutationsClosedDuringRecovery`: при pending recovery
   доступны только разрешённые recovery controls; обычные mutations отвечают
   отказом до readiness (RT-03).
6. `TestCoorddProcessGracefulShutdownReleasesNamespace`: SIGTERM останавливает
   coordd child; следующий process получает тот же namespace после остановки
   HTTP handlers, loop, transport и durable stores (ST-02, ADR-0004 RT-03).
7. `TestCoorddRejectsUnknownTransportKind` и config tests проверяют неизвестный
   kind, неизвестные поля, неизвестную/отсутствующую schema version и exit 2
   без state side effects.
8. `TestCoorddTransportFailureReportsReleasedOwnership` проверяет ошибку
   Telepathy startup после lock, exit 1, cleanup и немедленное повторное
   получение namespace ownership.

План Task-level recovery для P0-2/P0-5 должен дополнительно убивать writer до и
после передачи владения и проверять: Attempt не меняется при recovery, Task.Fence
монотонно увеличивается, старый writer отвергается, committed Result/receipt не
теряется, replacement не начинает запись до stop proof. Attempt/Fence требования
следуют ADR-0001 и не доказываются текущим Run-only restart test.

`daemon.TestDaemonReopensFileBackedRunSnapshotAndReleasesLock` проверяет reopen
новым Coordinator в одном процессе и штатное освобождение lock; он не является
process-kill acceptance.

## 7. Текущие write paths и миграция к daemon command path

| Путь | Текущее поведение | P0 цель / ограничение |
|---|---|---|
| `cmd/coord` обычные Run-команды и localchat executor | Создают localchat service; он открывает свой Store на `runs.json` (`internal/localchat/service.go:148`) и Coordinator (`:159-161`). Executor checkpoints пишутся отдельно в `executors/<run>.json` (`:431`, `:503`). Это отдельный host, не production daemon. | До P0-3 legacy Run writes используют старый Store только если daemon не владеет namespace. P0-2 `coordd` удерживает общий `runs.json.lock`; после захвата daemon локальный host не может параллельно открыть Store. Legacy rooms заморожены шагом 1 ADR-0001; executor state остаётся отдельной областью. |
| `cmd/coord` rooms/session API и TUI | `rooms.Open` открывает отдельный `rooms.json` с собственным lock (`internal/localchat/service.go:181`, `internal/rooms/rooms.go:135`). | Legacy/compatibility до перевода нужных операций; не считать их Task/Run write path. Не добавлять новые rooms workflow (ADR-0001 шаг 1). |
| Auxiliary localchat state | `connection.json` сохраняет выбранный маршрут Telepathy (`internal/localchat/service.go:168`, `:308`); `answers/<run>.json` сохраняет текстовый ответ на запрос (`:336`); `events.jsonl` записывает отправленные Envelope целиком (`:522`); `owner.key` содержит локальный owner credential (`internal/localchat/owner.go:16`). | Это routing, handoff, transport diagnostic и credential state, не canonical Task state. Не включать в Task Event Log. До P0-3 отдельно проверить secret redaction для ответов/Envelope, доступ к credential и дальнейшую судьбу каждого файла. |
| Rooms history export | `rooms.Export` проецирует историю Rooms в `state/history`; `include_content` намеренно может включать текст (`internal/rooms/history.go`, `internal/localchat/http.go:271-272`). | Legacy Git projection, не canonical history. До вывода Rooms сохранить явно выбранный режим экспорта и отдельно проверить отсутствие секретов в export path; без `include_content` содержание событий очищается, но это не заменяет ingest quarantine P0-3. |
| `cmd/coord-telepathy` standalone host | `telepathyhost.Run` сам открывает `state/runs.json`, создаёт Coordinator и делает `Assign` (`internal/telepathyhost/host.go:21-97`). | Одно-run integration/reference path сегодня. До P0-3 он и coordd взаимно исключаются тем же `runs.json.lock`; P0-3 превращает host в adapter/command client daemon, без второго Store/Coordinator. |
| `cmd/coord-telepathy` worker и `executor.Executor` | Worker пишет executor checkpoint `executor.json` (`internal/telepathyhost/worker.go:117-122`); `executor.Executor` сохраняет checkpoint через внедрённый `checkpoint.File` (`executor/checkpoint.go:180-201`). Это отдельное executor state, не каноническое Coord state. | Сохраняет границу executor vs coordinator; production binding к Task/Run — после P0-2/P0-3. Checkpoint writer ownership должен быть проверен вместе с worktree writer lock; он не заменяет daemon ownership. |
| `cmd/coord-watchdog` | Использует authenticated control API и внешние fence/spawn/stop commands (`cmd/coord-watchdog/main.go:16-33`). | Supervisor остаётся внешним владельцем процессов; P0-5 конкретизирует stop proof. Никаких прямых storage writes. |
| `integration/telepathy` | Integration harness отдельно открывает `runs.json` в `integration/telepathy/main.go:346-366` и `integration/telepathy/checks.go:86-100`, создаёт Coordinator и вызывает `Assign`; worker fixture сохраняет собственное scripted state через `save`. Это test/integration writer, не production daemon. | Использовать только уникальный временный state directory. Не запускать параллельно с daemon на общем namespace; не считать lock refusal этого harness проверкой production daemon. |
| `examples/agent` | Локальный reference harness сам открывает Store/Coordinator, пишет `executor.json` и запускает тестовый процесс агента (`examples/agent/main.go:119-130`). | Не production daemon и не второй production writer; все его файлы должны быть изолированы от production state namespace. |
| `cmd/coord-demo`, test fakes | Демонстрационные/тестовые in-process пути. | Не использовать как production deployment. |

Полный список runtime writers должен быть сверён перед P0 implementation на
актуальных entry points; таблица фиксирует найденные пути, а не утверждает, что
каждый legacy writer уже подключён к daemon.

## 8. Порядок P0 и зависимости

Порядок соответствует ADR-0001 шагам 6–11 и `TARGET-ARCHITECTURE.md`:

| Slice | Содержание | Проверяемый результат |
|---|---|---|
| P0-1 | Daemon runtime skeleton: executable composition, namespace ownership, lifecycle, Run-only crash/restart acceptance | Второй OS process отвергается; process kill/restart восстанавливает Run snapshot; ready и mutation guard fail-closed |
| P0-2 | Task aggregate, `task_<ULID>` adoption, Task↔Run mapping, Task.Fence и единый State+Event+Outbox commit | Идентичность не выводится из legacy `Run.Task`; Assign/Tick/recovery/startup допускают не более одного active writer на Task |
| После P0-2 | Review/Audit и Discussion Graph; затем ограниченная Shadow Validation | Канонические structured results и event-backed graph готовы до подключения production writers; сверяются только факты с однозначным соответствием |
| P0-3 | Production CLI и Telepathy agent writes через daemon command boundary к Task/Run | После Review/Audit, Discussion Graph и Shadow Validation все production Task/Run mutations идут через daemon; проверяются TaskID, RunID, Attempt, session generation и Task.Fence; delivery не означает acceptance |
| P0-4 | Production executor lifecycle и writer lock | Повторный TASK не запускает второго writer; ABORT/lease-loss и неоднозначный restart fail-safe |
| P0-5 | Supervisor конкретной установки и end-to-end recovery | Stop proof предшествует ownership transfer; acceptance проверена на целевой установке |

API/Dashboard и ADR-0006 находятся после runtime readiness и canonical write path
(ADR-0001 шаги 12–13), поэтому не входят в P0-1.

## 9. Явный non-scope

- Task aggregate, TaskID runtime enforcement, Task.Fence и Task-level ownership:
  P0-2.
- Полная Task/Event/Outbox transaction и её startup validation: P0-2.
- Production CLI and Telepathy canonical Task write paths: P0-3.
- Production executor lifecycle и конкретный supervisor: P0-4/P0-5.
- API/Integration contract: ADR-0006; Dashboard, Discussion Graph, Project Map.
- Review/Audit, External References, synchronization.
- Новые workflow функции или новые авторитетные состояния в `internal/rooms`.
- Multi-host, active-active и distributed leader election.
- Неоднозначный автоматический импорт `internal/rooms`.

## 10. Проверяемые критерии P0-1

- [x] Schema-versioned config отклоняет неизвестные поля и CLI override без
  state side effects (`TestCoorddInvalidConfigExitsTwoBeforeSideEffects`).
- [ ] Соответствие всех записанных executable/config/state namespace решений
  решению Owner не сверяется автоматическим тестом.
- [x] Весь runtime получает ownership до listeners; второй OS-процесс не ready,
  а неподдерживаемый lock завершается `ErrUnavailable`
  (`TestCoorddSecondProcessCannotOpenOwnedNamespace`,
  `TestLockExclusivityOrFailClosed`).
- [x] `TestCoorddRefusesStartWithoutTransportBeforeLock` и
  `TestCoorddRejectsUnknownTransportKind` доказывают, что transport config
  отклоняется до namespace lock с exit 2 и диагностикой ownership.
- [x] `TestCoorddTakesNamespaceLockBeforeStores` доказывает, что единая namespace
  lock берётся раньше file-level locks Store и RoleRegistry; эти locks сохраняются.
- [x] Ошибки сборки освобождают уже открытые ресурсы; shutdown закрывает loop и
  handlers до Store/RoleRegistry ownership release
  (`TestCoorddTransportFailureReportsReleasedOwnership`,
  `TestDaemonGracefulShutdownReleasesNamespaceAfterHandlers`).
- [x] Обычные mutation commands закрыты при not-ready; recovery-only path явно
  отделён согласно RT-03 (`TestDaemonReadinessKeepsMutationsClosedDuringRecovery`).
- [x] P0-1 process-kill/restart и shutdown tests используют отдельные OS
  processes и реальную `coordd` composition; in-process reopen и прямые
  filelock/store helper tests не засчитываются
  (`TestCoorddProcessKillReleasesNamespaceAndRestarts`,
  `TestCoorddSecondProcessCannotOpenOwnedNamespace`,
  `TestCoorddProcessGracefulShutdownReleasesNamespace`).
- [x] Acceptance tests покрывают ownership refusal/release, Run restore, Run
  outbox replay до readiness, Result receipt preservation, mutation gate при
  recovery и graceful shutdown (RT-01/03/04, ST-01/02/03):
  `TestCoorddProcessKillReleasesNamespaceAndRestarts`,
  `TestCoorddSecondProcessCannotOpenOwnedNamespace`,
  `TestCoorddProcessRestartRestoresAndReplaysOutboxBeforeReady`,
  `TestCoorddProcessRestartPreservesResultAndReceipt`,
  `TestDaemonReadinessKeepsMutationsClosedDuringRecovery`,
  `TestCoorddProcessGracefulShutdownReleasesNamespace`.
- [ ] Полную RT-03 readiness и Task/Event/Outbox atomicity не подтверждают
  принятые P0-1 тесты; эти гарантии остаются в P0-2.
- [ ] Все production writer paths перечислены и либо проходят daemon command
  path, либо явно обозначены legacy/non-production с планом перехода. Полнота
  списка и классификации не проверяется принятым тестом.
- [ ] Смысловая согласованность P0 dependencies между
  `TARGET-ARCHITECTURE.md`, ADR-0001 и ROADMAP не проверяется принятым тестом;
  нужна отдельная документная сверка.

## Решения владельца 2026-10-08 (R38)

1. Production executable — отдельный `cmd/coordd`; подкоманды `coord daemon` нет.
   Причина: production lifecycle отделён от CLI и localchat host.
2. Конфигурация — один schema-versioned файл, флаг задаёт только его путь;
   отсутствующая или неизвестная версия отклоняется при старте, override-флагов
   нет. Причина: runtime получает один проверяемый конфигурационный контракт.
3. Agent transport обязателен; отсутствие конфигурации приводит к понятному
   отказу до namespace lock, без удержания lock и зависания в `not-ready`.
   Причина: daemon не должен выглядеть рабочим без канала координации.
4. Одна namespace lock захватывается до file-level locks Store и RoleRegistry;
   существующие file locks остаются. Причина: второй daemon не должен успеть
   открыть часть runtime до обнаружения конфликта владельца.
