# P0-2: Canonical Task State Design

- **Статус:** Accepted for implementation
- **Область:** каноническая Task-модель, связь Task ↔ Run и durable state boundary
- **Нормативные источники:** ADR-0001 (CM-01, CM-06…CM-10), ADR-0003
  (TID-01…TID-06), ADR-0004 (RT-01…RT-06), ADR-0005 (ST-01…ST-07),
  `TASK-MODEL.md` (TM-01…TM-26), `ROADMAP.md` (P0-2)
- **Назначение:** implementation design для P0-2. Этот документ ссылается на
  принятые нормы и не вводит новых инвариантов.

## 1. Текущая модель → целевая модель

### Текущее состояние по коду

- `fsm.Run` (`fsm/fsm.go:36-69`) содержит строковое `Task`, `RunID`, `Attempt`,
  собственный `Fence`, протокольный `Status`, дедупликацию `Seen` и
  `Outbox []envelope.Envelope`. Отдельных `Task`, `TaskStatus`, `StagePlan`,
  `Task.Fence`, `Revision` и `Run.TaskID` нет.
- `store.Persistence` (`store/store.go:18-20`) загружает и сохраняет только
  `map[string]fsm.Run`. `Store.Update` сериализует изменение, сохраняет снимок и
  публикует новую память только после успешного `Save` (`store/store.go:106-137`).
- `checkpoint.Snapshot` (`checkpoint/checkpoint.go:12-15`) имеет `Version` и
  только `Runs`. `checkpoint.File` уже использует atomic file replacement, но
  не содержит новой канонической модели.
- `Coordinator.Assign`, `Handle`, `Tick` и recovery меняют Run через текущий
  Store. Fence сравнивается с `Run.Fence`; `Handle` сравнивает envelope.Task с
  сохранённым `Run.Task` (`coordinator.go:121-158, 224-335, 386-462`).
- `taskid.ID` и ULID generator существуют и протестированы, но TaskID ещё не
  выдаётся Task aggregate и не проверяется при сохранении канонической Task.
- `coordd` P0-1 открывает `runs.json` и `roles.json` в одном namespace
  (`cmd/coordd/main.go:114-124`); это production composition текущей Run-модели.

### Целевая граница

P0-2 вводит одну полную `CanonicalState` и одну persistence transaction
boundary. Для начала она включает:

```go
type CanonicalState struct {
    SchemaVersion uint32
    Tasks         map[taskid.ID]Task
    Runs          map[string]Run
    Events        []DomainEvent
    Inbox         map[string]InboxReceipt
    Outbox        map[string]OutboxRecord
}

type Task struct {
    ID          taskid.ID
    Title       string
    Description string
    OwnerID     string
    Revision    uint64
    LastEventSeq uint64
    StagePlan   StagePlan
    CurrentStage *Stage
    Status      TaskStatus
    Fence       uint64
    CreatedAt   time.Time
    UpdatedAt   time.Time
}

type DomainEvent struct {
    ID        string
    TaskID    taskid.ID
    RunID     string
    EventSeq  uint64 // непрерывен и уникален внутри Task
    Type      string
    CreatedAt time.Time
}

type Run struct {
    Task       string // прежнее fsm.Run.Task; имя и значение сохраняются
    TaskID     taskid.ID
    RunID      string
    Role       Role
    SessionID  string
    Attempt    uint64
    Fence      uint64 // снимок Task.Fence
    Status     RunStatus
    // остальные текущие Run-поля сохраняются по их существующей семантике
}
```

Это эскиз размещения полей из TASK-MODEL, а не новая схема-контракт. Имена
`Stage`, `TaskStatus`, `RunStatus`, `Role`, `DomainEvent`, `InboxReceipt` и
`OutboxRecord` должны быть уточнены кодом по существующим правилам; их значения
не переопределяются этим планом.

Граница размещения:

| Данные | Текущее место | Целевое место P0-2 |
|---|---|---|
| TaskID, OwnerID, Revision, StagePlan, CurrentStage, Task Status, Task Fence | отсутствуют | `Task` (TM-01, TM-05…TM-12) |
| Attempt, протокольный Run Status, RunID, SessionID | `fsm.Run` | `Run`; Attempt остаётся свойством Run (TM-16…TM-22) |
| Fence | `fsm.Run.Fence` — локально проверяемая эпоха | `Task.Fence` — авторитетная эпоха; `Run.Fence` — выданный снимок (RT-06, TM-20) |
| Связь Task ↔ Run | неявная строка `Run.Task` | явный `Run.TaskID`; Task не дублирует список RunID (TM-02) |
| Domain Event | отсутствует в canonical store | append-only `Events` в той же canonical state transaction; `EventSeq` непрерывен внутри Task, `Task.LastEventSeq` хранит последний номер (CM-01, ST-01, TM-26 (R56)) |
| Outbox | вложен в `fsm.Run.Outbox` | одна очередь в canonical state; не хранить вторую копию в Run (ST-01) |
| Дедупликация входа | `fsm.Run.Seen` | сохраняется для legacy-протокола; `Inbox` используется новыми daemon commands; сведение — P0-3, без TTL/pruning (R54) |

Полная грамматика `StagePlan` хранится и валидируется по TM-08. В P0-2
исполняется только `EXECUTION`; остальные стадии не запускаются. План,
содержащий этап, который runtime не умеет исполнять, отклоняется fail-closed
(R53).

`Session` transport presence, Review/Audit, Discussion, Graph, External
References и Artifacts не получают отдельного storage в этом срезе. Позднейшие
канонические агрегаты расширяют эту же `CanonicalState` и transaction boundary;
вторая независимая persistence для них не вводится (ADR-0001 CM-01, ADR-0005).

## 2. TaskID, связь с Run и wire compatibility

- `Task.ID` имеет тип `taskid.ID` и формат `task_<ULID>` (TM-01, TID-06).
- `Run.TaskID` — отдельная связь на Task; она никогда не вычисляется из legacy
  `Run.Task` (TM-02, TM-03).
- При генерации `TaskID` commit boundary проверяет уникальность сохранённого
  ключа и повторяет генерацию при коллизии согласно TID-01/ST-07. Ошибка
  генератора не создаёт Task, Event или Outbox (TID-01a).
- Старое значение `fsm.Run.Task` сохраняется byte-for-byte и продолжает
  сравниваться с legacy envelope.Task, пока отдельная wire migration не принята
  (TM-03). P0-2 не переименовывает и не переписывает поле в сохранённых снимках.
- Новый wire field `TaskID` и изменение schema version относятся к P0-3. До него
  входящий старый envelope разрешается по существующему RunID, затем Coord
  получает TaskID из проверенной связи Run → Task; `envelope.Task` проверяется
  по прежнему правилу и не используется для поиска/создания Task. P0-2 не
  предоставляет внешний agent write path; его перевод на daemon command boundary
  относится к P0-3.
- Операторского способа создавать Task в P0-2 нет. Создание доступно только
  внутреннему API daemon и тестам; внешний write path вводится вместе с
  ADR-0006/P0-3 (R52).
- Будущий отдельный importer Runs требует однозначной mapping table
  `RunID → TaskID`; группировка по одинаковому `Run.Task`, номеру issue,
  названию или репозиторию запрещена (TM-03, TM-04, TID-03). P0-2 импорт не
  выполняет (R51).

## 3. Единая durable transaction

### Persistence boundary

Текущий `store.Persistence` заменяется новым интерфейсом, который принимает и
возвращает полную `CanonicalState`, а не только Run map. Интерфейс должен
сохранять single-owner namespace lock из P0-1 и atomic snapshot backend из
ADR-0005. Старый Run-only adapter не становится параллельным writer для того же
канонического state.

Эскиз:

```go
type Persistence interface {
    Load() (CanonicalState, error)
    Save(CanonicalState) error
    Lock() (release func() error, err error)
}
```

Это API-эскиз, не публичный контракт. Storage не предоставляет CAS; Task.Fence и
Revision проверяются в сериализованной daemon command boundary под namespace
lock (ST-02, ST-06).

### Порядок команды

Каждая изменяющая команда выполняется в таком порядке:

1. Command loop сериализует команду; daemon уже владеет namespace lock.
2. Coordinator проверяет субъект/разрешение, ожидаемую Revision, TaskID, RunID,
   Attempt, Session generation, Task.Fence и применимость перехода согласно
   существующим нормам (RT-04, RT-06, TM-05, TM-06, CM-07).
3. Из текущей памяти строится изолированный candidate `CanonicalState`.
4. В одной копии обновляются Task/Run, добавляется соответствующий DomainEvent,
   Outbox record и, когда требуется дедупликация входной команды, Inbox receipt.
5. `checkTaskInvariants` и валидация всей state/reference/FSM согласованности
   выполняются до записи (ST-04, RT-03 шаги 2 и 5).
6. Snapshot целиком кодируется с schema version и сохраняется atomic replace:
   temp file → write → file sync → close → rename → directory sync. Event не
   пишется отдельным JSONL или независимым файлом (ST-01).
7. Память публикует candidate только после успешного durable commit. После
   неопределённой ошибки commit daemon прекращает mutations и остаётся not-ready
   до reopen и повторной проверки snapshot (ST-03, RT-03).
8. Только после commit может начаться outbox delivery. Ошибка доставки оставляет
   durable запись на повтор; delivery не равна acceptance (ADR-0001, RT-04).

Таким образом одна транзакция имеет вид:

```text
CanonicalState(Task + Run + Event + Outbox [+ Inbox receipt])
                 = one atomic snapshot commit
```

Event Log остаётся append-only аудиторской историей; материализованное состояние
является источником восстановления. При рассогласовании readiness закрывается,
ни Event, ни State автоматически не выбираются победителем (ST-01, ST-04).

### Проверяемая стоимость snapshot

Каждая команда переписывает весь canonical document. Поэтому стоимость commit
растёт с полным Event Log, `Run.Seen`, Inbox и Outbox; для журнала из `E` событий
последовательные snapshot commits переписывают суммарный объём порядка `O(E²)`.
Это известная цена ST-01 и текущего backend, согласованная с ограничением роста
`Run.Seen` в ROADMAP P2. P0-2 не вводит усечение событий, TTL или pruning
дедупликации; такие действия требуют отдельного решения и доказательства replay
безопасности (ADR-0005 ST-01, ST-07).

До выхода за пределы snapshot backend нужно измерить фактический размер
canonical document и commit latency на состоянии с `N` событиями, где `N` —
размер acceptance fixture. Результат должен фиксировать `N`, размер файла и
время commit; порог production здесь не задаётся, он остаётся частью P2
Transactional database и production acceptance.

## 4. Run-only → CanonicalState migration

### Текущее → целевое

Текущий файл — version 1 `checkpoint.Snapshot{Runs}`. CanonicalState получает
новую schema version в отдельном файле/namespace; старый `runs.json`
автоматически не мигрируется и не открывается на запись (R51).

- Если старый snapshot содержит хотя бы один незавершённый Run, daemon остаётся
  `NOT_READY` с явной диагностикой legacy state. TaskID из `Run.Task`, внешних
  номеров или других полей не выводится; canonical writes запрещены.
- Если старый snapshot содержит только завершённые Runs, он не открывается на
  запись и не считается canonical state нового runtime.
- P0-2 не импортирует Runs, события или участников `internal/rooms` и не
  преобразует room participants в Runs.
- Мигратор существующего Run-only state — отдельный будущий инструмент. Если он
  будет одобрен, он потребует backup по ST-05 и явную mapping table
  `RunID → TaskID` (TM-03, TM-04, TID-03); без mapping запрещено создавать
  Task или восстанавливать ownership. R55 снимает отдельный выбор политики
  legacy `COORDINATOR`: возможный будущий импорт требует явной таблицы
  соответствия, но P0-2 его не выполняет.

Новая CanonicalState не зависит от наличия старого файла и загружается только из
своего namespace. Обнаружение legacy Runs влияет на readiness по правилам выше,
но не делает старый snapshot альтернативным источником истины.

### Seen, Inbox и retry

`Run.Seen` сегодня дедуплицирует входящие сообщения внутри одного Run до
изменения FSM; `Run.Outbox` содержит отправки до подтверждённой доставки
(`coordinator.go:280-335`, `386-461`). Ни один сохранённый message ID не может
быть потерян при migration: старый повтор не должен повторно применить переход,
а незавершённый Outbox должен отправляться с прежней идентичностью по текущей
семантике.

P0-2 сохраняет `Run.Seen` для дедупликации legacy-протокола. `Inbox` используется
для новых daemon commands. Их namespace и семантика остаются раздельными до
сведения в P0-3; обе стороны сохраняются без TTL/pruning (R54). Старый Outbox
переносится в единственный Outbox canonical state только в рамках отдельного
одобренного импорта; в P0-2 нет автоматической миграции и второй записи Outbox.

## 5. Task-wide Fence и ownership

- `Task.Fence` — authoritative; `Run.Fence` — снимок эпохи при выдаче конкретного
  Run (RT-06, TM-20).
- Для любого входящего agent write Coord разрешает Run → Task, сверяет RunID,
  Attempt, generation и входной fence с текущим `Task.Fence` и выполняет проверку
  вместе с переходом в одной command transaction. Устаревшая эпоха не может
  быть принята потому, что совпала с локальным `Run.Fence` другого Run.
- Первое назначение использует первую положительную эпоху; recovery сохраняет
  RunID/Attempt и после supervisor stop proof повышает Task.Fence, записывая
  новый snapshot в тот же Run; rework создаёт новый Run/Attempt и повышает
  Task.Fence согласно таблице TASK-MODEL. Переходы остаются сериализованы одной
  canonical transaction (TM-17…TM-20, RT-06, ST-06).
- Assign, Tick, recovery, rework и startup validation проверяют не более одного
  active writer на Task. При обнаружении нескольких владельцев запись и
  readiness закрываются fail-closed (RT-03, RT-06, ST-04).
- `Task.Fence` не заменяет stop proof. Новый writer не назначается, пока
  supervisor не committed подтверждение остановки прежнего writer (CM-01, CM-07,
  TM-13, TM-18, TM-20).
- Существующие run-local fence проверки и supervisor claim tests остаются
  регрессионными проверками протокола; новые проверки дополнительно доказывают
  сравнение с Task.Fence и CAS внутри одной canonical state transaction.

## 6. Startup invariants и будущие тесты

`checkTaskInvariants(state)` дополняет существующий `checkpoint.validateRuns`;
он не заменяется им. Проверки должны возвращать контекстную ошибку до readiness,
без repair и выбора «победителя» неявно.

| Проверка | Основание | Будущий тест |
|---|---|---|
| Каждый `Task.ID` каноничен; ключ совпадает с ID; сохранённые ID уникальны | TM-01, TM-22; TID-01, TID-06; ST-07 | `TestCanonicalStateRejectsInvalidOrDuplicateTaskID` |
| Каждая новая Run имеет непустой TaskID на существующую Task; legacy Task остаётся отдельным полем | TM-02…TM-04 | `TestCanonicalStateRejectsRunWithoutMappedTask`; `TestMigrationPreservesLegacyRunTask` |
| Каждая Run принадлежит ровно одной Task; связь не выводится из Task string | TM-02, TM-03 | `TestCanonicalStateRejectsConflictingRunTaskMapping` |
| Каждый Task Event имеет непрерывный `EventSeq`, начиная с 1; `Task.LastEventSeq` равен sequence последнего события | TM-26 (R56) | `TestCanonicalStateRejectsEventSeqGap` |
| Task fields, Revision, StagePlan, Status и CurrentStage удовлетворяют принятым правилам | TM-05…TM-12 | `TestCanonicalStateRejectsInvalidTaskAggregate` |
| Run Status допустим; Attempt и RunID корректны; SUPERSEDED — только после принятого перехода | TM-16…TM-22 | `TestCanonicalStateRejectsInvalidRunAggregate` |
| Run fence snapshot не выше Task.Fence; текущий writer соответствует Task ownership; Fence не уменьшается при команде | RT-06, ST-06, TM-18, TM-20 | `TestTaskFenceIsAuthoritativeAndMonotonic` |
| На Task не больше одного active writer; нарушение блокирует startup и мутации | RT-03 шаг 5, RT-06, ST-04, TM-18, TM-20 | `TestCanonicalStateRejectsMultipleActiveWritersPerTask` |
| Каждая Task имеет Domain Event создания; у каждого Event есть TaskID существующей Task и, если задан RunID, этот Run принадлежит той же Task; для каждой Task Revision последнего события мутации совпадает с `Task.Revision`; Fence события последней передачи ownership совпадает с `Task.Fence`; Outbox record ссылается на Event той же атомарной команды | CM-01; TM-06; ST-01, ST-04 | `TestCanonicalStateRejectsDanglingOrInconsistentEvent`; `TestCanonicalStateChecksLatestTaskRevisionAndFenceEvents` |
| Outbox и Inbox receipts имеют допустимые ссылки/идентичность; transition, Event, Outbox и обязательный Inbox receipt согласованы | CM-01; ST-01, RT-04 | `TestCanonicalStateRejectsDanglingInboxOrOutbox`; `TestCanonicalTransitionCommitsStateEventOutboxAtomically` |

Проверки change-time, которым нужно сравнить прежнее и новое состояние
(Revision/Fence/Run ownership), выполняются внутри command transaction; простой
read-only startup validator не может доказать, что значение никогда не убывало
между коммитами (TM-06, RT-06, ST-06).

## 7. Readiness P0-2

P0-1 готовит текущий Run runtime и namespace ownership. P0-2 добавляет до
readiness:

1. Чтение ожидаемой schema version и отказ на неизвестной версии без side effect
   (ST-04).
2. Обнаружение legacy `runs.json`: незавершённый Run удерживает daemon в
   `NOT_READY` с диагностикой; только завершённые Runs не открываются на запись
   и не считаются каноническими. При одних только завершённых legacy Runs daemon
   может стать ready на новом canonical namespace, если пройдены остальные
   readiness checks (R51).
3. Полную structural/reference/FSM validation `CanonicalState`, включая
   TaskID↔Run.TaskID mapping, Event/Outbox/Inbox, Revision и StagePlan (RT-03
   шаг 2; ST-04). Согласованность истории проверяется по явным ссылкам Event →
   Task/Run, последнему событию мутации каждой Task с её Revision, событию
   последней передачи ownership с её Fence и ссылке каждого Outbox record на
   Event той же команды. R56 задаёт непрерывный Task-scoped `EventSeq` и
   `Task.LastEventSeq`; readiness остаётся закрытой, если проверка этих
   соответствий не проходит.
4. Проверку Task.Fence и всех Run fence snapshots; отсутствие конкурирующих
   active writers на Task. Нарушение держит daemon not-ready (RT-03 шаг 5,
   RT-06, ST-04).
5. Восстановление индексов/проекций в памяти из единственного canonical
   snapshot; это не создаёт персистентную копию.
6. Reopen после неопределённой ошибки commit, повторную полную validation и
   закрытие mutations до успешного завершения (RT-03, ST-03).

P0-2 не называет runtime готовым к безопасному запуску нового агента, если для
него ещё нет committed stop proof прежнего writer или не выполнен иной
обязательный шаг RT-03. Outbox replay до обычного обслуживания выполняется по
текущему readiness contract; delivery не считается acceptance.

## 8. Не входит в P0-2

- Структурированные Review/Audit результаты и workflow integrations — следующий
  этап ADR-0001, после Task foundation.
- Discussion Graph и Project Dependency Graph — ADR-0001 CM-11 и ADR-0002.
- External References и синхронизация с provider — ADR-0003.
- Read API, Dashboard, Startup Card и публичный API contract — ADR-0006.
- Перенос агентского Telepathy write path на новую командную границу, новый wire
  `TaskID` и проверка Worktree/Artifact refs — P0-3.
- Production executor/worktree lifecycle, task-specific stop proof integration
  и OS isolation — P0-4/P0-5.

P0-2 обязан оставить совместимость с текущим Run wire protocol до P0-3 и не
объявлять Review/Audit/Discussion/Graph/External References реализованными
только потому, что их места зарезервированы в модели.

## 9. Проверяемые критерии P0-2

Реализация принимается только после тестов на текущей canonical persistence
boundary. Имена ниже — целевые; они ещё не являются существующими доказательствами.

- TaskID создаётся как канонический `task_<ULID>`; коллизия при сохранении
  порождает повтор генерации; идентичность не зависит от external ref
  (`TestCreateTaskGeneratesCanonicalTaskID`,
  `TestTaskIDCollisionRetriesBeforeCommit`).
- `Run.TaskID` является явной связью, а legacy `Run.Task` сохраняется без
  переинтерпретации (`TestRunTaskIDIsExplicit`; `TestMigrationPreservesLegacyRunTask`).
- Task, Run, DomainEvent, Outbox и обязательный Inbox receipt либо все commit,
  либо ни один; после ошибки памяти не публикуется и новые writes закрыты до
  reopen (`TestCanonicalTransitionCommitsStateEventOutboxAtomically`,
  `TestCanonicalCommitFailureDoesNotPublishOrContinue`).
- Task.Fence authoritative, монотонен, сравнивается до дедупликации/ownership
  side effects; recovery/rework не оставляют двух writers
  (`TestTaskFenceIsAuthoritativeAndMonotonic`,
  `TestRecoveryAcrossRunsRejectsOldTaskFence`,
  `TestAssignTickRecoveryFailClosedWithMultipleWriters`).
- Legacy state с незавершённым Run удерживает daemon not-ready, а только
  терминальные legacy Runs не открываются на запись и не считаются canonical
  (`TestCoorddLegacyRunsWithActiveRunKeepsNotReady`,
  `TestCoorddLegacyTerminalRunsAreNotCanonical`).
- Повреждённая/неподдерживаемая CanonicalState, висячая ссылка либо нарушение
  Fence/Writer/Event/Inbox/Outbox consistency блокирует `/readyz` и mutation
  (`TestCoorddReadinessRejectsInvalidCanonicalState`).
- После restart сохраняются TaskID, Task/Run relation, Fence, Event Log,
  dedup receipts и незавершённый Outbox; доставленный transport ACK не
  подменяет Domain acceptance
  (`TestCoorddRestartRestoresCanonicalTaskStateAndOutbox`).
- Все ранее принятые P0-1 process acceptance и существующие Run retry/recovery
  tests остаются зелёными; тесты текущего per-Run Fence продолжают защищать
  wire-совместимость до P0-3.

## 10. Решения владельца 2026-10-08 (R51–R56)

Решения закрывают открытые вопросы дизайна. Они не расширяют P0-2 до
операторского write path, импортера или полного workflow runtime.

- **R51 — новый canonical namespace без автоматической миграции.** Старый
  `runs.json` не открывается новым runtime на запись. Любой незавершённый Run в
  legacy state удерживает daemon в `NOT_READY` с явной диагностикой; TaskID из
  старых данных не выводится. Только завершённые Runs также не открываются на
  запись и не считаются каноническим состоянием. Мигратор — отдельный будущий
  инструмент; если он будет одобрен, ему нужны backup по ST-05 и явная mapping
  table `RunID → TaskID`.
- **R52 — Task creation только внутренний.** P0-2 не предоставляет операторской
  команды или control endpoint. Внутренний daemon API используется реализацией и
  тестами; внешний путь появляется вместе с ADR-0006/P0-3.
- **R53 — полная грамматика, ограниченное исполнение.** StagePlan хранится и
  валидируется по TM-08; P0-2 запускает только `EXECUTION`. Остальные стадии не
  запускаются; план с этапом, который runtime не умеет исполнять, отклоняется
  fail-closed.
- **R54 — два dedup namespace до P0-3.** `Run.Seen` сохраняется для
  legacy-протокола; Inbox обслуживает новые команды. Сведение выполняется в
  P0-3. TTL и pruning не вводятся.
- **R55 — legacy `COORDINATOR` не импортируется в P0-2.** Если будущий импорт
  будет одобрен, для старых назначений нужна отдельная явная таблица соответствия;
  автоматическое назначение одной из целевых ролей запрещено.
- **R56 — непрерывная последовательность событий внутри Task.** Каждый
  `DomainEvent` имеет `EventSeq`, монотонный, уникальный и непрерывный с 1 в
  пределах Task. `Task.LastEventSeq` хранит sequence последнего события и
  присваивается в той же фиксации, что Task, Run, Event и Outbox. Timestamp не
  задаёт порядок событий.

## Связанные документы

- [ADR-0001: Canonical Coordination Model](../../ADR-0001-CANONICAL-COORDINATION-MODEL.md)
- [ADR-0003: External Reference Model](../../ADR-0003-EXTERNAL-REFERENCE-MODEL.md)
- [ADR-0004: Runtime and Daemon Model](../../ADR-0004-RUNTIME-AND-DAEMON-MODEL.md)
- [ADR-0005: Storage Model](../../ADR-0005-STORAGE-MODEL.md)
- [Task Model v1](../../TASK-MODEL.md)
- [Roadmap](../../ROADMAP.md)
