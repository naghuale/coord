# Проверка и границы отладки

Базовые автоматические проверки воспроизводятся локально без реальных модельных
сессий и сети провайдера; live-сценарии отдельно указаны ниже. HTTP tests используют локальный httptest; subprocess tests —
собственный тестовый executable в t.TempDir(), а не настоящий AI-agent CLI.

## Команды

```sh
make check
git diff --check
go test ./examples/agent -count=1
go test ./envelope -run '^$' -fuzz '^FuzzDecode$' -fuzztime=5s -parallel=2
go test ./envelope -run '^$' -fuzz '^FuzzVerify$' -fuzztime=5s -parallel=2
```

`make check` включает gofmt, vet, все race tests, golangci-lint (если установлен),
сборку и go mod tidy -diff.
Fuzzing ограничен временем и не доказывает отсутствие всех возможных ошибок.

## Подтверждённые сценарии

| Сценарий | Проверяющий тест |
|---|---|
| ACK/heartbeat durable до первого send | executor.TestResponsesDurableBeforeFirstSend |
| Готовность предшествует RESULT после outage | executor.TestReadinessAndResultSurviveOutageInOrder |
| Старый fence не попадает в replay | executor.TestRecoveryClearsOldGenerationMessages |
| Отменённая отправка сохраняет outbox | executor.TestCanceledSendRemainsReplayable |
| Просроченные lease responses обновляются до отправки | executor.TestReplayRenewsExpiredResponsesBeforeSending |
| Чужая identity не меняет checkpoint | executor.TestMismatchedTaskCannotMutateCheckpoint |
| Дубликат TASK после собственного commit принимается без проверки исходного HEAD | executor.TestDuplicateTaskRemainsAcceptableAfterHeadAdvances |
| RESULT_ACK durable, correlated и idempotent | executor.TestReceiptDurablyCompletesExecutorAndIsIdempotent |
| Поддельный receipt не удаляет RESULT | executor.TestForgedOrStaleReceiptCannotRemoveResult |
| Абсолютный deadline действует без Tick | coord.TestAbsoluteDeadlineEnforcedWithoutTick |
| Запоздалый heartbeat не откатывает прогресс | coord.TestDelayedHeartbeatCannotRegressProgress |
| Свежий повтор ACK не откатывает STARTED | coord.TestRepeatedAckDoesNotResetStartedRun |
| Receipt не наследует backoff старого PING | coord.TestNewReceiptDoesNotInheritEarlierSendBackoff |
| Терминальный run повторяет недоставленный receipt | coord.TestCommittedResultReceiptSurvivesTransportFailureAndRestart |
| Нельзя принять JSON с хвостом и выполнить его первую операцию | coord.TestControlRejectsTrailingJSONBeforeMutation |
| Входные ссылки не изменяют опубликованный Store/Registry | store.TestLoadedAndCommittedStateCannotBeMutatedExternally; roles.TestRegistryDoesNotRetainCallerOwnedProfilesAndEvents |
| После неопределённого commit нужна повторная загрузка | store.TestFailedCommitRequiresReopenBeforeFurtherWrites; roles.TestRegistryPersistenceFailureRequiresReopen |
| Два registry writer не владеют одним snapshot | roles.TestRegistryHasExclusiveProcessOwnership |
| Неподтверждённый stop/unknown spawn сохраняет claim | watchdog.TestFailedReplacementStopRetainsClaim; watchdog.TestFailedSpawnRequiresConfirmedCleanup |
| Cancellation не запрещает bounded cleanup | watchdog.TestCancellationStillAllowsBoundedCleanup |
| Ошибка одного run не блокирует обработку других | watchdog.TestSweepDoesNotStarveOtherRunsAfterOneFailure |
| Fence/revision не оборачиваются | coord.TestRecoveryRejectsFenceOverflow; roles.TestRevisionOverflowIsRejected |
| Byte transport отбрасывает неправильного получателя | transport.TestAdapterFiltersMalformedAndWrongRecipient |
| Подпись проверяется на round-trip | transport.TestSignedClientRoundTripAndInvalidFrames |
| Save не заменяет snapshot состоянием, которое Load не принимает | checkpoint.TestSaveRejectsStateThatCannotBeReloaded |
| Checkpoint всегда предоставляет Lock; на неподдержанной платформе отказ происходит до Load | checkpoint.TestLockOwnsSnapshotOrRefusesWithoutRelease; filelock.TestLockExclusivityOrFailClosed; store.TestRefusedOwnershipFailsBeforeLoadingState |
| Новый Coordinator в том же процессе повторно открывает file-backed Run snapshot; второй store отклонён при активном host, lock освобождается после shutdown | daemon.TestDaemonReopensFileBackedRunSnapshotAndReleasesLock |
| TaskID ULID генерация/разбор, timestamp и границы формата | taskid package tests; пакет пока не импортируется production-кодом и не доказывает валидацию Run/snapshot/protocol |
| Jitter положителен, Duration не переполняется | retry.TestNanosecondJitterNeverReturnsZero; retry.TestDelayNeverOverflowsAtDurationLimit |
| CLI host проходит полный lifecycle и фиксирует receipt | examples/agent.TestLocalAgentSuccessAndFailure |
| Таймаут останавливает дочерний writer | examples/agent.TestLocalAgentDeadlineStopsDescendantWriter |
| Telepathy worker после ABORT подтверждает остановку writer process group | internal/telepathyhost.TestWorkerAbortStopsWriterProcessGroup |
| Потеря heartbeat renewal останавливает writer process group | internal/telepathyhost.TestWorkerLeaseLossStopsWriterProcessGroup |
| Повторный TASK не запускает команду второй раз | internal/telepathyhost.TestWorkerDuplicateExecutesOnceAndRequiresReceipt |
| Restart с неоднозначным запуском fail-closed без повторного исполнения | internal/telepathyhost.TestWorkerRefusesAmbiguousRestart |

## Что эти проверки не доказывают

- Работу конкретного LLM frontend и его Telepathy plugin в живой установке.
- Надёжность delivery/ordering у внешнего транспорта.
- Корректность произвольных agent subprocess, daemonized descendants и внешних side effects.
- Поведение при power loss в любой точке fsync/rename и повреждении диска.
- Высокую нагрузку, bounded queues и отсутствие задержек при медленных sends.
- Полный порядок сообщений при одинаковых timestamps или больших расхождениях часов.

Прогресс монотонен внутри fence; старые timestamps heartbeat отвергаются.
Это защита от подтверждённого отката, а не распределённый sequence protocol.
Canonical snapshot сохраняется целиком; transport sends остаются последовательными.
Production bridge к canonical Task runtime, standalone daemon executable,
sequence/epoch policy и comprehensive chaos suite по-прежнему требуют отдельной
реализации, как указано в ROADMAP. Библиотечный Run-coordinator host уже есть;
его границы описаны ниже и в ADR-0004.
`make check` включает compile-only `go vet` для `GOOS=windows`, подтверждая
доступность fail-closed Lock метода. Сборка для Solaris и AIX не входит в
проверки проекта и не гарантируется; на этих и других неподдержанных ОС
`filelock.Lock` отказывает fail-closed вместо работы без process ownership.

## Изолированные Telepathy process tests

На этой локальной версии выполнен make test-telepathy с установленным Telepathy
0.8.0: round-trip и work_count=1, P0/P1 (fencing и coordinator restart), P2/P3
(subprocess resume и идемпотентные SQLite side effects) завершились PASS.
Использованы scripted peers в отдельных TELEPATHY_HOME, без реального LLM frontend.
Это подтверждает указанные сценарии, а не production bridge для вашей AI-сессии.

Runtime evidence в state/ отделено marker state/go.mod от основного Go-модуля,
чтобы исторические копии исходников не считались production packages. Архивы
экспериментов не изменялись. Ошибки close/cleanup snapshot writer возвращаются
вызывающему коду; atomic writer общий для checkpoint, registry и harness.

## Standalone Telepathy host/worker (2026-10-07)

`make test-telepathy-host TELEPATHY_SERVER=...` проверяет отдельные процессы
поставляемого CLI, подписи, RESULT_ACK, оба DONE и рестарт worker без повторной
команды. Unit/race tests проверяют потерю receipt, replay, ambiguous restart,
чужой worktree, MCP version/isError/cancellation и archive replay.
Это не интеграция canonical Crewflow и не тест реальной модели.
См. [TELEPATHY_HOST.md](TELEPATHY_HOST.md).


## Ролевые сессии — проверка 2026-10-07

- make check: vet, полный race suite, линтер 0 issues, build, tidy.
- Сценарные тесты: восстановление роли/checkpoint, generation fencing,
  идемпотентность, отдельные contexts, WAITING_ASSIGNMENT, owner API,
  регламент/readiness, failure persistence, recovery Git commit/cursor.
- make test-sessions: PASS, state/session-smoke-1791335129873216000/report.json.
- Изолированный Telepathy host/worker: PASS,
  state/telepathy-standalone/1791334844319908000/report.json; оба DONE,
  work_count=1, host restart во время работы, worker restart без исполнения.
- Дополнительный real AI subprocess: PASS,
  state/role-ai-1791334104399915000/report.json. Отдельная OpenCode AI-сессия
  сама выполнила scoped CLI, ответила дважды, сохранила checkpoint и восстановилась
  после остановки daemon. Owner-текст взят дословно из диалога; интерактивный
  owner terminal этим прогоном не проверен. Transport — Unix API, не Telepathy.
- Реальные отдельные OpenCode TUI: CHAT-1 PASS, журнал комнаты
  2ddfab7a-4236-4445-af99-2e44fe8516d7, state/tui-team/evidence/report.json.
  Owner явно пригласил Codex, затем coordinator назначил и пригласил
  nemotron-executor и longcat-auditor. Модели подтверждены сообщениями самих
  агентов, provider rollout отдельно не проверялся. В журнале: ответ Executor
  seq14, замечание Auditor seq20, ответ Executor seq22, verdict seq25 и
  исправление Executor seq28. Оба сохранили checkpoints своими capability.
- RECOVERY-1 BLOCKED_PROVIDER_QUOTA (подтверждено owner): daemon остановлен/запущен; session UUID, роли,
  generation и progress сохранены, cursors не откатились. Coordinator и Executor
  выполнили resume до generation2. Старую запись coordinator лично проверил:
  stale session generation; Executor сообщил аналогичный отказ в seq40.
  LongCat Auditor остаётся generation1: Free usage exceeded, retry примерно через 22 часа. Его recovery не проверен. Executor
  подтвердил ready на generation2 в seq43 и сообщил о готовности в seq44. Полная проверка восстановления обоих TUI не объявляется PASS.
- Качество ответа проверялось отдельно от протокола: seq14/22 содержали ошибки
  о restart и глобальном fencing, потребовалось вмешательство coordinator.
  Даже исправленный seq28 избыточно требует новый транспортный channel.
  Интерактивное участие owner в terminal, рестарт TUI и выключение компьютера
  этим прогоном не проверялись. Runtime Git-история приватная, локальная.

Raw evidence и capability остаются в ignored state, не в source Git.
Аппаратное выключение машины, диск-loss и произвольные внешние side effects
этими тестами не доказаны. Для команд и ограничений см. SESSION_CLI.md.
