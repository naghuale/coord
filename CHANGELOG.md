# История изменений

## 2026-10-08 — production Run daemon (P0-1)

- Добавлен `coordd` с единственным `-config`, строгим JSON schema v1,
  обязательным Telepathy transport и exit codes 0/1/2; некорректная config
  отклоняется до state side effects (`TestCoorddInvalidConfigExitsTwoBeforeSideEffects`,
  `TestLoadRequiresKnownSchemaVersion`, `TestLoadValidatesCompleteTelepathyConfiguration`).
- `coordd` берёт namespace lock раньше Store/RoleRegistry; transport startup
  выполняется после lock, а отказ освобождает всё открытое. Process-level сценарии
  запускают реальную coordd composition в дочерних OS-процессах и проверяют
  блокировку второго writer, release/restart после kill, восстановление Run,
  replay outbox до readiness, сохранение committed result и штатный SIGTERM
  shutdown
  (`TestCoorddRefusesStartWithoutTransportBeforeLock`,
  `TestCoorddTakesNamespaceLockBeforeStores`,
  `TestCoorddTransportStartupFailureReleasesOwnership`,
  `TestCoorddProcessKillReleasesNamespaceAndRestarts`,
  `TestCoorddSecondProcessCannotOpenOwnedNamespace`,
  `TestCoorddProcessRestartRestoresAndReplaysOutboxBeforeReady`,
  `TestCoorddProcessRestartPreservesResultAndReceipt`,
  `TestCoorddProcessGracefulShutdownReleasesNamespace`).
- Общий Telepathy factory теперь используется daemon и standalone host; readiness
  остаётся закрытой при supervisor recovery, а shutdown закрывает handlers/loop,
  transport и durable ownership в этом порядке
  (`TestWorkerDuplicateExecutesOnceAndRequiresReceipt`,
  `TestDaemonReadinessKeepsMutationsClosedDuringRecovery`,
  `TestDaemonGracefulShutdownReleasesNamespaceAfterHandlers`).
- В ROADMAP добавлена модель интеграции агентов после P0-3: используется
  существующий envelope и структурированные Review/Audit results. (документация)
- Уточнены критерии P0-3/4/5 для проверки worktree в протокольных путях,
  per-Task worktree/writer lock и обязательной fail-closed OS isolation
  executor и требуемых доказательств из Linux CI. (документация)

## 2026-10-08 — fail-closed snapshot ownership

- Snapshot ownership uses the shared `filelock` implementation. On platforms
  without a supported process lock, opening the store is rejected before state
  is loaded; the system no longer proceeds without writer ownership
  (`TestRefusedOwnershipFailsBeforeLoadingState`,
  `TestLockOwnsSnapshotOrRefusesWithoutRelease`).
- `make check` runs cross-platform `go vet` for Windows; Solaris and AIX builds
  are neither checked nor guaranteed. (не проверено)
- `telepathy.Process.Close` no longer reports a false `context canceled` when an
  MCP server exits cleanly after stdin EOF; a parent-context deadline still
  returns an error (`telepathy.TestCloseAfterCleanExitIsNotAnError`; this race
  previously made `TestRPCChecksVersionAndToolFailure` fail intermittently).
- Run deadline handling (`failDeadline`) clears `ResumeStatus` with recovery
  state, so a terminal Run has no continuation point
  (`TestRunInvariantsHoldThroughLifecycle`).
- Выход run из `STALLED` очищает `ResumeStatus` в единственной точке перехода
  (`fsm.Run.Apply`), независимо от того, терминальный это переход или нет:
  раньше поздний heartbeat возвращал run в работу с сохранённой точкой
  продолжения, а RESULT после пропущенного probe давал `DONE` с
  `ResumeStatus` (`TestStalledExitClearsResumeStatus`).
- Ошибки разбора JSON в localchat HTTP (включая unknown field, хвост и
  превышение лимита тела) возвращаются как `400` с `code=invalid_input`, как
  и другие некорректные входы; сценарии проверяют неизменность rooms snapshot
  (`TestMalformedPOSTBodiesReturn400InvalidInputWithoutChangingRoomsState`,
  `TestSessionBlockInvalidFieldsReturn400WithoutChangingRoomsState`).
- По решению owner от 2026-10-08 автоматическая проверка содержимого checkpoint
  исключена из UX-1; перед `unblock` владелец проверяет checkpoint вручную,
  программная проверка остаётся в P1 ROADMAP. (документация)

## 2026-10-07 — coordinator daemon host runtime

- Добавлен библиотечный `daemon` runtime для запуска coordinator loop,
  control/metrics listeners и `/readyz`; обычная readiness учитывает pending
  supervisor recovery, а recovery control остаётся доступен для его завершения.
- Runtime выполняет startup tick до readiness, затем запускает transport receive;
  readiness остаётся закрытой при незавершённом supervisor recovery, а recovery
  endpoints доступны после инициализации receive. Shutdown использует единый
  timeout, закрывает HTTP handlers и освобождает Store/RoleRegistry; listeners
  ограничены loopback. Ошибки shutdown и закрытия durable stores возвращаются
  вызывающему коду, а не теряются в defer.
- Это host runtime текущего Run coordinator, а не завершённая canonical Task
  daemon или отдельный executable.

## 2026-10-07 — срез B UX-1: карточка состояния, блокер, `coord tui`

- `GET /availability` различает attachment, transport reachability, readiness,
  processing cursor, provider availability, blocker, ожидание и поколение.
  `stale_generation` виден отдельной строкой; provider без внешнего сигнала
  остаётся `unknown` — Coord не делает выводов о чужой модели.
- Блокер (`blocked_provider_quota` и другие известные причины) несёт
  reason/source/observed_at/retry_at, останавливает новое назначение роли на
  любом пути assign(), не трогает checkpoint и журнал, снимается только
  явным `coord session unblock` / командой TUI.
- Новая команда `coord tui`: кадр owner с раздельными полями и slash-командами
  (`/help /models /connect /sessions /rooms /join /leave /status /block
  /unblock`), те же эндпоинты, что и CLI; сетевой listener не открывается.
- Метод команды определяется одной таблицей для handler и client: рассинхрон
  двух списков раньше отдавал 405 на `/availability` и ломал запуск TUI.
- HTTP различает плохой вход (400, `code=invalid_input`) и конфликт состояния
  (409); ошибки указывают следующую допустимую команду. Некорректные reason,
  source и retry_at тоже относятся к 400, а не к конфликту.
- TUI: доступ к разделяемому состоянию идёт через мьютекс (`/join`, `/leave`,
  `/help`, `/status`, `/block`, `/unblock` больше не пишут поля без него);
  курсор `/watch` переносится в переподключение, поэтому обрыв потока не
  возвращает уже показанные строки; карточки перечитываются по тику, и внешний
  blocker виден без ввода команды.
- TUI: событие попадает в очередь кадра до продвижения курсора, поэтому
  переполнение очереди не теряет запись навсегда; горутина ввода, ожидающая
  отправки строки, освобождается при `/quit`; ожидание чтения stdin завершается
  вместе с процессом (`internal/localchat.TestQuitWithTrailingInputLeavesNoGoroutine`).
- TUI: подписка `/watch` живёт на одну комнату, поэтому `/join` другой комнаты
  и `/leave` рвут активный запрос; раньше поток прежней комнаты оставался
  открытым и события новой не приходили. Курсор комнаты двигает только цикл
  отрисовки — после того как событие реально показано, — поэтому события,
  доставленные в переполненную очередь покинутой комнаты, не теряются и не
  дублируются при возврате.
- `.gitignore` восстановлен: `/state/`, `/bin/`, credentials и логи снова
  не попадают в Git.
- Дедлайн делает run терминальным и очищает всю recovery-метаданную, включая
  `RecoveryClaim`: снимок больше не показывает активную блокировку у мёртвого
  run.
- Срез B одобрен owner. Срезы A (per-context профили) и C (discover/onboarding)
  не реализованы и требуют отдельного approval owner.
- Добавлены owner-команды `coord session block/unblock`; `block` принимает
  обязательный `--reason` и необязательный RFC3339 `--retry-at`. Повтор
  идентичного блокера теперь no-op без нового события/сдвига `observed_at`;
  просроченный `retry_at` отвергается как invalid input до поиска сессии.
- Успешные CLI-команды печатают машинно-читаемый результат; аргументы
  проверяются до чтения owner capability. Контракт проверяют
  `cmd/coord.TestSessionBlockAndUnblockUseOwnerCapability`,
  `cmd/coord.TestSessionBlockRejectsInvalidArgsBeforeRequest` и
  `cmd/coord.TestSessionUnblockReferencesRemainExecutable`.
- Перед `unblock` владелец должен вручную проверить checkpoint; CLI не
  подтверждает и не валидирует эту проверку.

## 2026-10-07 — согласован первый UX-сценарий (документация, без кода)

- docs/plans/ux-first-scenario/ALIGNED_UX.md: согласованное целевое поведение
  owner-карточек (раздельные attachment/reachability/readiness/cursor/activity/
  availability), per-context профиля с привязкой к generation и запрет
  auto-select newest credential, машинный блокер blocked_provider_quota с
  reason/source/observed_at/retry_at, discover с availability=unknown.
- docs/plans/ux-first-scenario/UX_TASK.md: задача UX-1 на реализацию
  (PENDING APPROVAL); код не менялся, команды профиля ещё не существуют.
- docs/plans/ux-first-scenario/PLAN.md: сводный план UX-1 в формате
  «что важно → что нужно → как сделаем → что будет»; порядок срезов
  B → A → C, LongCat recovery вне приёмки до снятия подтверждённой квоты.
- README и ROADMAP ссылается на план; приёмка раздела «от начала до
  восстановления» планируется с Codex Auditor и Nemotron; LongCat исключён
  до снятия подтверждённой provider quota.

## 2026-10-07 — устойчивые локальные ролевые сессии

- Общий coord CLI, owner-чат и независимый от TUI Unix API.
- Предварительное назначение/приглашение и WAITING_ASSIGNMENT.
- Устойчивые session_id, agent_id, context bindings, generation CAS и ротация capability.
- Checkpoint, explicit ack, идемпотентные операции и unknown внешние действия.
- Версионирование регламента координатором и подтверждение readiness.
- Раздельный owner API и scoped агентский контекст; доверие в пределах пользователя ОС.
- Локальная Git-проекция с export policy, backlog и recovery commit/cursor.
- Стандартные промпты четырёх ролей: границы полномочий, Task Contract, verdict и независимость.
- Общий coord prompt; сценарные/CLI проверки и opt-in тест отдельного реального AI.
- Crewflow не интегрируется; автоматического push runtime history нет.


## 2026-10-07 — standalone Telepathy host/bridge

- Добавлены пакет telepathy и cmd/coord-telepathy: реальный MCP stdio 0.8.0,
  pinned Ed25519 frames, host/worker с checkpoint, writer/session locks и receipt.
- Command worker отказывается повторять interrupted work без durable RESULT;
  готовый RESULT replay не запускает команду.
- Учтены Telepathy rate/duplicate limits и ограниченный archive replay.
- Добавлены make test-telepathy-host, инструкции настройки и отдельная
  спецификация Crewflow integration; CF-013A не изменяется.


## 2026-10-06 — агентские инструкции и расширенная отладка

- Добавлен RESULT_ACK: durable receipt в canonical outbox, terminal replay,
  executor.Receipt и идемпотентная повторная отправка RESULT. Обновлять обе стороны вместе.
- Deadline проверяется при получении событий; delayed heartbeat/progress regression
  не откатывают run, повтор ACK не откатывает STARTED.
- Executor проверяет полную identity до изменения checkpoint; duplicate TASK
  после собственного commit не требует исходного HEAD и не запускает работу заново.
- Store/Registry изолируют входные ссылки; registry имеет process lock и Close.
  После ErrPersistence дальнейшие записи прекращаются до close/reopen.
  Отвергаются revision overflow, неперечитываемые snapshots и неизвестные FSM states.
- Watchdog выполняет bounded cleanup при cancellation, обрабатывает все candidates
  после ошибки и удерживает claim при unknown/failed replacement cleanup.
- Atomic snapshot writer общий для checkpoint/registry/harness; ошибки close
  и cleanup обрабатываются. make check запускает установленный golangci-lint.
- Byte/signature adapters проверяют route/configuration, strict frame JSON; retry
  не выдаёт zero delay и не переполняет time.Duration.
- Добавлен tested Unix CLI/subprocess host examples/agent, инструкции обоих
  вариантов подключения, regression tests и fuzz targets.

## 2026-10-06 — durable executor replay и безопасный cleanup recovery

- ACK, heartbeat и RESULT сохраняются со состоянием до первого transport send;
  FIFO outbox сохраняет готовность перед результатом и не заменяет старые сообщения.
- RESULT_READY не откатывается при outage; успешный replay восстанавливает статус.
  Просроченные lease responses обновляются как новые durable protocol messages.
- Новый fence очищает сообщения, результат и blocker старого writer, сохраняя прогресс.
- Watchdog не освобождает claim при неподтверждённой остановке replacement.
- Store.Get копирует только запрошенный run вместо всего snapshot; изоляция вложенных данных сохраняется.
- Добавлены regression tests и сквозной coordinator/executor restart scenario.

## 2026-10-06 — уточнение границ экспериментов

- В EXPERIMENTS описаны проверяемые метрики и ограничения свидетельств после рестарта.
- Явно перечислены непроверенные сценарии с LLM, production bridge и транспортом.
  Runtime-код и lease-протокол не менялись.

## 2026-10-06 — fence diagnostics и live crash experiments

- Существующая точная проверка fence выделена перед проверкой owner/deduplication.
  Добавлены ErrStaleFence, stale/future rejection counters и защита от uint64 overflow.
- Unit tests подтверждают отклонение старого/будущего поколения, ABORT correlation,
  неизменность canonical run и сохранение owner/fence при failed recovery commit.
- Версионируемый test-only harness использует реальный Telepathy MCP 0.8.0.
  Проверены owner transfer после SIGKILL/flock release, coordinator restart,
  queued heartbeat во время outage и Python subprocess resume с SQLite idempotency.
- Добавлены `make test-telepathy`, инструкции воспроизведения и границы доказательств.
  Production bridge/watchdog/новые transports/lease enforcement не добавлялись.

## Начальная локальная версия — 2026-10-06

### Реализовано

- Go-модуль `coord` без внешних зависимостей.
- Типизированный wire protocol: TASK, ACK, HEARTBEAT, RESULT, PING/PONG,
  ABORT, ROLE_UPDATE/ROLE_ACK; строгая проверка payload и correlation.
- FSM, канонический owner, immutable run identity, retry и fencing generation.
- Атомарные snapshot, durable outbox, deduplication и Unix process lock.
- Checkpoint исполнителя, сохранение прогресса/результата и повторная доставка.
- Внешний watchdog: recovery claim, остановка writer, проверка HEAD,
  запуск replacement и CAS transfer.
- Четыре роли, требования к профилю/capabilities, атомарные ROLE_UPDATE,
  журнал событий и запрет аудита своей задачи по стабильному agent_id.
- Подпись Ed25519, Prometheus text endpoint и примеры alert rules.
- Проверки lifecycle, таймаутов, split-brain, восстановления, CAS и ролей.
- Демо, пример смены ролей, Makefile и документация эксплуатации/разработки.

### Границы версии

Реальный Telepathy bridge и команды управления агентскими процессами подключаются
отдельно. Приложение пока предоставляет ядро, демо и watchdog CLI; здесь нет
готового production daemon, распределённого consensus или аттестации модели.
Подробнее: [дорожная карта](docs/ROADMAP.md).
