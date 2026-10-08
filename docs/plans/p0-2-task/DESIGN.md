# P0-2: Canonical Task State Design

- **Статус:** Accepted for implementation
- **Область:** каноническая Task-модель, связь Task ↔ Run и durable state boundary
- **Нормативные источники:** ADR-0001 (CM-01, CM-06…CM-10), ADR-0003
  (TID-01…TID-06), ADR-0004 (RT-01…RT-06), ADR-0005 (ST-01…ST-07),
  `TASK-MODEL.md` (TM-01…TM-26), `ROADMAP.md` (P0-2)
- **Назначение:** implementation design для P0-2. Этот документ ссылается на
  принятые нормы и не вводит новых инвариантов.

## 1. Текущая модель → целевая модель

### Исходное состояние до P0-2 (commit 76a5c6a)

- `fsm.Run` (`fsm/fsm.go:36-69`) содержит строковое `Task`, `RunID`, `Attempt`,
  собственный `Fence`, протокольный `Status`, дедупликацию `Seen` и
  `Outbox []envelope.Envelope`. Отдельных `Task`, `TaskStatus`, `StagePlan`,
  `Task.Fence`, `Revision` и `Run.TaskID` нет.
- `store.Persistence` (`store/store.go:18-20`) загружает и сохраняет только
  `map[string]fsm.Run`. `Store.Update` сериализует изменение, сохраняет снимок и
  публикует новую память только после успешного `Save` (`store/store.go:106-133`).
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
    Tasks         map[taskid.ID]task.Task
    Runs          map[string]fsm.Run
    Events        []task.DomainEvent
    Inbox         map[string]store.InboxReceipt
    Outbox        map[string]store.OutboxQueue
}

```

В 22a `Tasks`, `Events`, `Inbox` и верхнеуровневый `Outbox` были пустыми
коллекциями. В текущем срезе `task.Task` и `task.DomainEvent` стали типизированной
каноникой; Outbox typed и хранится на верхнем уровне canonical state, Inbox
пока зарезервирован. `fsm.Run.TaskID` теперь
явное необязательное поле переходного состояния: пустой TaskID означает legacy
Run и никогда не выводится из `Run.Task`. Новые Task-scoped Runs требуют
существующий Task и событие в том же commit.

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
исполняется только `EXECUTION`; остальные стадии не запускаются. Грамматика
StagePlan остаётся полной, но `taskservice.Create` до выделения TaskID и записи
отвергает план, содержащий этап, который runtime не умеет исполнять (R53).

В срезе 22a `CanonicalState.Runs` сохранял текущий `fsm.Run` целиком, включая
его per-Run `Outbox`; верхнеуровневый `CanonicalState.Outbox` был зарезервирован.
В 22b единственная durable очередь перенесена в canonical state: запись по
`RunID` хранит упорядоченные envelopes, retry time/count и optional `EventID` для
новых доменных отправок. Run API гидратирует прежнее поле `Run.Outbox` для
совместимости coordinator, но на диске envelope хранится ровно один раз.
`Task`-scoped отправка обязана ссылаться на DomainEvent той же команды;
Run-only envelopes сохраняют исходные RunID и message identity без выдумывания
Task/Event.

В начальной canonical schema v1 есть typed Inbox receipts. Ключ receipt выводится
из стабильного `ActorID` и входного `MessageID`; receipt ссылается на Task и на
DomainEvent принятой команды. `CanonicalStore.ApplyInboxCommand` проверяет
дубликат до handler, проверяет ожидаемую Task Revision против текущей и
зафиксированную Revision против обновлённой Task, а новую команду фиксирует с её
state/Event/Outbox одним snapshot commit. Повтор возвращает прежний receipt без
повторного вызова handler или записи. Аутентификация, capability и получение
ожидаемой Revision из авторизованного запроса остаются обязанностью daemon
command boundary; этот storage primitive сам по себе не объявляется
production-ready command path.

Canonical snapshot использует начальную schema v1. Неизвестная версия
отвергается до readiness без изменения файла. Переход на будущую версию схемы
не реализуется в этом срезе и требует отдельного решения; если такой переход
будет принят, процедура должна выполнить backup по ADR-0005 ST-05.

`Session` transport presence, Review/Audit, Discussion, Graph, External
References и Artifacts не получают отдельного storage в этом срезе. Позднейшие
канонические агрегаты расширяют эту же `CanonicalState` и transaction boundary;
вторая независимая persistence для них не вводится (ADR-0001 CM-01, ADR-0005).

### Проверенная граница Owner authorization

Task write command нельзя считать готовым только потому, что у daemon есть
аутентификация:

- `coordd` сейчас принимает один статический `ControlToken` для control handler;
  он не кодирует стабильный human principal, Task scope или session generation.
- `internal/localchat` проверяет локальный `owner.key`, но это отдельный
  localchat endpoint и отдельная доверенная граница; ключ не удостоверяет
  `Task.OwnerID` в canonical state.
- `internal/rooms.SessionAuth` проверяет token вместе с точными SessionID и
  generation, но принадлежит legacy Room snapshot. Его нельзя интерпретировать
  как capability канонической Task или переносить вместе с Room state.
- Текущий внутренний `taskservice.Create` вызывает `CanonicalStore` напрямую и
  принимает `OwnerID` как данные; он не аутентифицирует человека. Это пока только
  не подключённый foundation helper для тестов, не допустимый production command
  path и не доказательство TM-05/RT-04.

До Task mutation command требуется закрыть этот порядок:

1. Определить стабильный Owner principal и сессию с монотонным generation;
   выдать capability, ограниченную конкретным TaskID и разрешённым действием.
2. Daemon command boundary проверяет principal, capability, TaskID и generation
   до вызова Task service; OwnerID берётся из проверенного principal, не из body.
3. Команда сверяет expected Task Revision под общей serialized boundary, строит
   transition и DomainEvent, затем делает один durable commit.
4. Свободный текст не включать в каноническое событие, пока ingest quarantine /
   redaction не определён и не проверен на history export и диагностических логах.

Existing control token и legacy Room capability не являются fallback для этой
проверки. Поэтому CAS-механизм Task Revision может быть реализован отдельно, но
никакой Task write command не должен быть подключён до выполнения пунктов 1–4.

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
  предоставляет внешний CLI или agent write path; перевод production-команд на
  daemon command boundary относится к P0-3.
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

Transport/runtime bookkeeping в Run (доставка, retry, heartbeat и внутренние
probe receipts) также сохраняется через ту же atomic snapshot, но не создаёт
DomainEvent и не увеличивает Task Revision. Изменение доменного состояния Run
(ownership, fence, FSM status, progress, blocker или accepted result) обязано в
том же commit увеличить Task Revision и добавить событие с теми же RunID и новой
Task Revision. `fsm.Run.SameDomainState` отделяет эти изменения от технических.

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

## 4. Run-only state и CanonicalState

### Текущее → целевое

Текущий legacy файл — version 1 `checkpoint.Snapshot{Runs}`. Новый daemon
использует отдельный `canonical.json`; старый `runs.json` автоматически не
импортируется и не открывается на запись (R51). Canonical snapshot начинает с
schema v1; переходы между версиями схемы в этом срезе не реализуются. Будущий
переход требует отдельного решения и процедуры backup по ADR-0005 ST-05.

Формат определяется по envelope и schema discriminator внутри документа, а не
по имени файла: legacy `checkpoint.Snapshot` распознаётся по `Version` и
legacy-форме `Runs`; CanonicalState — по `SchemaVersion` и canonical-форме.
Имя `runs.json` указывает, где проверить legacy state, но само по себе не
определяет формат. Неизвестный или неоднозначный envelope считается ошибкой
схемы и блокирует readiness; ни один распознаватель не пробует записать файл.

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

Пока production Run writers не переведены на daemon boundary в P0-3,
`coordd` удерживает compatibility lock для `runs.json` и canonical store lock
вместе с namespace lock на всём сроке владения. Порядок захвата: namespace lock,
`runs.json.lock`, `canonical.json.lock`; RoleRegistry открывается после этих
locks. Compatibility lock принадлежит process composition `coordd`, а
`Persistence.Lock()` canonical store удерживает `canonical.json.lock`. Ошибка
любого шага освобождает уже взятые locks в обратном порядке до начала
обслуживания запросов, кроме диагностического startup failure, описанного в §7.
Compatibility lock — это тот же
`runs.json.lock`, который берут текущие `localchat` и standalone Run hosts через
`checkpoint.File.Lock`. Lock берётся
до проверки legacy snapshot и не освобождается при успешном старте, иначе
legacy writer мог бы запуститься параллельно уже после readiness. Если lock
занят, `coordd` не читает legacy файл и остаётся диагностически `NOT_READY` с
причиной `legacy writer owns namespace`; transport, Tick, outbox delivery и
mutations не стартуют. После остановки legacy writer требуется явный restart.
Когда все production writers переведены на daemon command path, compatibility
lock снимается отдельным изменением, а не условной настройкой.

`examples/agent`, `integration/telepathy` и demo/test hosts остаются Run-only
путями на `runs.json` только в собственных временных state directories; их
фикстуры не используют production namespace.

### Seen, Inbox и retry

`Run.Seen` сегодня дедуплицирует входящие сообщения внутри одного Run до
изменения FSM; `Run.Outbox` содержит отправки до подтверждённой доставки
(`coordinator.go:280-335`, `386-461`). Ни одна сохранённая message identity не
должна теряться: повтор не должен повторно применить переход, а незавершённый
Outbox после restart отправляется с прежней идентичностью.

P0-2 сохраняет `Run.Seen` для дедупликации legacy-протокола. `Inbox` используется
для новых daemon commands. Их namespace и семантика остаются раздельными до
сведения в P0-3; обе стороны сохраняются без TTL/pruning (R54). Outbox уже
хранится на верхнем уровне начальной canonical schema v1. Старый `runs.json` не
преобразуется и остаётся под правилами R51.

## 5. Task-wide Fence и ownership

- `Task.Fence` — authoritative; `Run.Fence` — снимок эпохи при выдаче конкретного
  Run (RT-06, TM-20).
- Для любого входящего agent write Coord разрешает Run → Task, сверяет RunID,
  Attempt, generation и входной fence с текущим `Task.Fence` и выполняет проверку
  вместе с переходом в одной command transaction. Устаревшая эпоха не может
  быть принята потому, что совпала с локальным `Run.Fence` другого Run.
- Canonical storage transition проверяет форму transfer: новый active Run или
  смена owner существующего Run требует `Task.Fence+1`, совпадающий `Run.Fence`
  и same-commit `TASK_OWNERSHIP_TRANSFERRED`. Recovery требует предыдущий
  `STALLED + RecoveryNeeded + RecoveryClaim`, сохраняет RunID/Attempt и очищает
  claim. Новый Run допускается, только если прежний writer был terminal в
  предыдущем snapshot; terminalization и replacement в одном commit отклоняются.
  Это не подтверждает остановку процесса и не выдаёт transfer authorization.
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

Таблица ниже вводится в 22b вместе с Task aggregate. В 22a отдельный Task
validator ещё не применяется: canonical document обязан содержать только
валидируемые текущими правилами Runs, а `Tasks`, `Events` и верхнеуровневый
`Outbox` должны быть пусты. Это не позволяет принять Run-only snapshot за
полностью проверенную Task-модель.

| Проверка | Основание | Целевое имя теста |
|---|---|---|
| Каждый `Task.ID` каноничен; ключ совпадает с ID; сохранённые ID уникальны | TM-01, TM-22; TID-01, TID-06; ST-07 | `TestCanonicalStateRejectsInvalidOrDuplicateTaskID` (целевой) |
| Каждая новая Run имеет непустой TaskID на существующую Task; legacy Task остаётся отдельным полем | TM-02…TM-04 | `TestCanonicalStateRejectsRunWithoutMappedTask` (целевой); `TestCanonicalStateValidatesTaskEventAndRunRelations` |
| Каждая Run принадлежит ровно одной Task; связь не выводится из Task string | TM-02, TM-03 | `TestCanonicalStateRejectsConflictingRunTaskMapping` (целевой) |
| Каждый Task Event имеет непрерывный `EventSeq`, начиная с 1; `Task.LastEventSeq` равен sequence последнего события | TM-26 (R56) | `TestCanonicalStateRejectsEventSeqGap` (целевой); `TestCanonicalStateRejectsEventSequenceGapAfterFirstEvent`; `TestCanonicalStateRequiresLastEventSequenceToMatchHistoryLength` |
| Task fields, Revision, StagePlan, Status и CurrentStage удовлетворяют структурным правилам; `OwnerID` здесь проверяется только как непустая ссылка, не как доказанная human identity или capability | TM-06…TM-12 | `TestCanonicalStateRejectsInvalidTaskAggregate` (целевой); Owner authorization проверяется отдельной command-boundary моделью до P0-3 |
| Run Status допустим; Attempt и RunID корректны; SUPERSEDED — только после принятого перехода | TM-16…TM-22 | `TestCanonicalStateRejectsInvalidRunAggregate` (целевой) |
| Run fence snapshot не выше Task.Fence; ownership transition требует Task.Fence+1, matching Run.Fence и same-commit transfer event; `Run.Fence` существующего Task-scoped Run неизменен, кроме recovery transfer того же Run; Task.Fence не меняется без transfer | RT-06, ST-06, TM-18, TM-20 | `TestCanonicalTransitionFirstAssignmentIncrementsTaskFence`; `TestCanonicalTransitionRecoveryPreservesAttemptAndIncrementsTaskFence`; отрицательные `TestCanonicalTransitionRejectsOwnerChangeWithoutTaskFenceBump`, `TestCanonicalTransitionRejectsFenceBumpWithoutOwnershipTransfer`, `TestCanonicalTransitionRejectsRunFenceChangeWithoutRecovery`, `TestCanonicalTransitionRejectsFenceJump`, `TestCanonicalTransitionRequiresOwnershipTransferEvent`, `TestCanonicalTransitionRequiresRunFenceToMatchTaskFence`, `TestCanonicalTransitionRejectsFenceOverflow`, `TestCanonicalTransitionRejectsTerminalRunFenceChangeWithoutRecovery`, `TestCanonicalTransitionRejectsActiveRunFenceChangeWithoutRecovery`; `TestCanonicalStateRejectsTerminalRunFenceAboveTaskFence` |
| На Task не больше одного active writer; новый Run требует, чтобы прежний writer уже был terminal в previous snapshot | RT-03 шаг 5, RT-06, ST-04, TM-18, TM-20 | `TestCanonicalTransitionRejectsSecondActiveWriter`; `TestCanonicalStateRejectsMultipleActiveRunsPerTask` |
| Смена OwnerSession возможна только при nonterminal recovery transfer; terminal Run не меняет owner и не реактивируется | RT-06, TM-20, TM-22 | `TestCanonicalTransitionRejectsOwnerChangeWhenRunBecomesTerminal`; `TestCanonicalTransitionRejectsTerminalRunReactivation` |
| Роль Task-scoped Run неизменна; RoleSelection.Profile.Role согласован с RequiredRole.Role и выбранная Session совпадает с Run.OwnerSession; смена RoleSelection сопровождается той же записью в конце RoleHistory | TM-22, RT-06, AGENTS.md | `TestCanonicalTransitionRejectsTaskRunRoleMutation`; `TestCanonicalStateRejectsMismatchedSelectedRole`; `TestCanonicalStateRejectsSelectedRoleSessionMismatch`; `TestCanonicalTransitionRequiresRoleSelectionHistory` |
| Сохранённый префикс RoleHistory неизменяем по `agent_id`, роли и порядку; допускается только append | AGENTS.md: независимость AUDITOR по стабильному agent_id и истории исполнителей | `TestCanonicalTransitionRoleHistoryIsAppendOnly` |
| В commit передачи ровно один `TASK_OWNERSHIP_TRANSFERRED`, он ссылается на переданный Run; marker без передачи запрещён | CM-01, ST-01, RT-06 | `TestCanonicalTransitionRequiresOwnershipTransferEvent`; `TestCanonicalTransitionRejectsTransferMarkerWithoutTransfer`; `TestCanonicalTransitionRejectsTransferMarkerForWrongRun`; `TestCanonicalTransitionRejectsExtraTransferMarker` |
| Recovery сохраняет RunID/Attempt и очищает RecoveryNeeded/RecoveryClaim; incomplete recovery отклоняется | TM-20, RT-06 | `TestCanonicalTransitionRecoveryPreservesAttemptAndIncrementsTaskFence`; `TestCanonicalTransitionRejectsRecoveryWithUnclearedState` |
| Каждая Task имеет ровно один `TASK_CREATED` на revision 1 с Fence 0; все события revision несут один snapshot `TaskFence`; transfer marker единственной записью повышает Fence на 1 и содержит RunID; финальный Fence совпадает с Task.Fence; каждый Task-scoped Run связан с последним transfer marker своего RunID и совпадающим `Run.Fence`; Event.RunID принадлежит той же Task; Outbox record ссылается на Event той же атомарной команды | CM-01; TM-06; ST-01, ST-04, RT-06 | `TestCanonicalStartupRejectsWrongFenceInEarlyRevision`, `TestCanonicalStartupRejectsWrongFenceInLatestRevision`, `TestCanonicalStartupRejectsMissingFenceBumpOnTransfer`, `TestCanonicalStartupRejectsExtraFenceBumpWithoutTransfer`, `TestCanonicalStartupRejectsTwoTransferMarkersInOneRevision`, `TestCanonicalStartupRejectsSecondEventInCreationRevision`, `TestCanonicalStartupRejectsTransferMarkerWithoutRunID`, `TestCanonicalStartupRejectsFenceOverflow`, `TestCanonicalStartupRejectsFinalFenceDifferentFromTask`, `TestCanonicalStartupRejectsSecondTaskCreated`, `TestCanonicalStartupRejectsLateTaskCreated`, `TestCanonicalStartupRejectsEventRunIDOwnedByAnotherTask`, `TestCanonicalStartupRejectsRunFenceDifferentFromLatestTransfer`, `TestCanonicalStartupRejectsTaskScopedRunWithoutTransferMarker`, `TestCanonicalStartupAcceptsFenceSnapshotsAcrossTransferAndRecovery` |
| Outbox и Inbox receipts имеют допустимые ссылки/идентичность; transition, Event, Outbox и обязательный Inbox receipt согласованы; stale expected Revision отклоняется до handler | CM-01; ST-01, RT-04 | `TestCanonicalStateRejectsInvalidInboxOutboxAndSchema`; `TestCanonicalTaskOutboxRequiresEventFromSameCommit`; `TestApplyInboxCommandCommitsReceiptWithTaskEventAndDeduplicates`; `TestApplyInboxCommandRejectsStaleExpectedRevision` |

Проверки change-time, которым нужно сравнить прежнее и новое состояние
(Revision/Fence/Run ownership), выполняются внутри command transaction; простой
read-only startup validator не может доказать, что значение никогда не убывало
между коммитами (TM-06, RT-06, ST-06).

## 7. Readiness P0-2

P0-1 готовит текущий Run runtime и namespace ownership. P0-2 добавляет до
readiness поэтапно.

1. **22a:** чтение ожидаемой schema version и отказ на неизвестной версии без state side effect
   (ST-04).
2. **22a:** обнаружение legacy `runs.json`: незавершённый Run удерживает daemon в
   `NOT_READY` с диагностикой; только завершённые Runs не открываются на запись
   и не считаются каноническими. При одних только завершённых legacy Runs daemon
   может стать ready на новом canonical namespace, если пройдены остальные
   readiness checks (R51).
3. **22a:** проверка текущих Run-инвариантов и того, что `Tasks`, `Events` и
   верхнеуровневый `Outbox` пусты. Такая readiness означает только готовность
   текущего Run runtime и не заявляет полную RT-03 readiness.
4. **22b:** полная structural/reference/FSM validation `CanonicalState`, включая
   TaskID↔Run.TaskID mapping, Event/Outbox/Inbox, Revision и StagePlan (RT-03
   шаг 2; ST-04). Согласованность истории проверяется по явным ссылкам Event →
   Task/Run, последнему событию мутации каждой Task с её Revision, событию
   последней передачи ownership с её Fence и ссылке каждого Outbox record на
   Event той же команды. R56 задаёт непрерывный Task-scoped `EventSeq` и
   `Task.LastEventSeq`; readiness остаётся закрытой, если проверка этих
   соответствий не проходит.
5. **22b:** проверка Task.Fence и всех Run fence snapshots; отсутствие конкурирующих
   active writers на Task. Нарушение держит daemon not-ready (RT-03 шаг 5,
   RT-06, ST-04).
6. Отдельных persisted или in-memory индексов canonical модели пока нет.
   `CanonicalStore` загружает единственный snapshot, а Run compatibility
   projection восстанавливает из canonical `Runs` и top-level Outbox через
   `hydrateRuns`; это не создаёт вторую persisted копию. Проверки:
   `TestCanonicalRunStorePersistsSingleTopLevelOutbox` и
   `TestCoorddRestartRestoresCanonicalTaskStateAndOutbox`.
7. После неопределённой ошибки commit failed handle отклоняет дальнейшие
   mutations; новый `CanonicalStore` загружает фактический snapshot и повторно
   проверяет его до разрешения записи. Проверка:
   `TestCanonicalCommitFailureRequiresReopenAndRevalidatesDurableState` (RT-03,
   ST-03).

Если загрузка canonical state обнаружила неизвестную schema version,
compatibility lock занят legacy writer либо проверка legacy `runs.json`
обнаружила незавершённый Run, `coordd` не завершается и не пытается восстановить
или переписать состояние. P0-2 добавляет
diagnostic-only runtime без `Coordinator`: он связывает настроенные loopback
listeners, `/readyz` возвращает `503` с причиной, та же причина попадает в
stderr, metrics listener отдаёт только startup diagnostics. Namespace lock
остаётся удержанным; если compatibility lock был получен, он также удерживается.
Обычные и recovery mutations, `Tick`, запуск Telepathy transport, receive и
outbox delivery не запускаются. Control endpoints недоступны; доступны только
`/readyz` и диагностические metrics. Если legacy lock уже занят другим writer,
daemon не читает legacy file и остаётся not-ready с причиной `legacy writer owns
namespace`. После исправления причины требуется явный restart и полная повторная
загрузка/проверка. Этот заблокированный startup не публикует успешную readiness
и не создаёт нового writer.

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
- Перенос production CLI и Telepathy write paths на новую командную границу,
  новый wire `TaskID` и проверка Worktree/Artifact refs — P0-3.
- Production executor/worktree lifecycle, task-specific stop proof integration
  и OS isolation — P0-4/P0-5.

P0-2 обязан оставить совместимость с текущим Run wire protocol до P0-3 и не
объявлять Review/Audit/Discussion/Graph/External References реализованными
только потому, что их места зарезервированы в модели.

## 9. Проверяемые критерии P0-2

### Срез 22a: storage boundary

- CanonicalState загружается и сохраняется через одну persistence boundary;
  Save atomic, Run API сохраняет прежнюю семантику, а память публикуется только
  после успешного durable Save (`TestCanonicalCommitFailureDoesNotPublishOrContinue`).
- Ошибка Save закрывает дальнейшие изменения до reopen; legacy Run file никогда
  не открывается для записи.
- Legacy snapshot с активным Run оставляет процесс в диагностическом not-ready;
  терминальный legacy snapshot не становится каноническим и остаётся побайтно
  неизменным (`TestCoorddActiveLegacyRunKeepsNotReadyWithoutStartingTransport`,
  `TestCoorddTerminalLegacyRunIsNotImported`).
- Если legacy `runs.json.lock` занят старым writer, daemon остаётся диагностически
  not-ready, не читает legacy snapshot и не начинает side effects; если daemon
  захватил compatibility lock первым, legacy writer не может открыть Store до
  освобождения daemon namespace (`TestCoorddLegacyWriterLockKeepsDaemonNotReady`,
  `TestCoorddSecondProcessCannotOpenOwnedNamespace`).
- Неизвестная схема, активный legacy Run и занятый compatibility lock удерживают
  процесс в diagnostic-only runtime: `/readyz`=503 с причиной, доступна только
  диагностика, процесс живёт до SIGTERM и удерживает уже взятые ownership locks.
- Неизвестная schema version оставляет процесс в диагностическом not-ready без
  state mutation, Tick, transport receive или outbox delivery
  (`TestCoorddUnknownCanonicalSchemaKeepsNotReady`).
- P0-1 process acceptance и текущие Run retry/recovery тесты проходят с теми же
  смысловыми assertions; фикстуры создают состояние через persistence boundary.

### Срез 22b: Task aggregate foundation (в работе)

- Типизированные `task.Task`, `StagePlan`, `TaskStatus` и `DomainEvent` валидируют
  TaskID, Stage/Status, версию policy, терминальность, EventSeq и связи.
- Внутренняя `taskservice` создаёт Task с `task_<ULID>` и `TASK_CREATED`; Task и
  событие сохраняются одним `CanonicalStore.UpdateCanonical`. Коллизия ID
  повторяет генерацию под той же serialized commit boundary
  (`TestCreateTaskGeneratesCanonicalTaskID`,
  `TestTaskIDCollisionRetriesBeforeCommit`). Операторский/public write path не
  добавлен (R52).
- `fsm.Run.TaskID` отделён от неизменённого `fsm.Run.Task`; canonical validation
  отвергает висячую связь, несколько active Runs на Task, Fence выше Task.Fence и
  несогласованную историю и некорректные persisted Run FSM комбинации
  (`TestCanonicalStateValidatesTaskEventAndRunRelations`,
  `TestCanonicalStateRejectsMultipleActiveRunsPerTask`,
  `TestRunValidateRejectsPersistedFSMInvariantViolations`).
- `CanonicalStore.UpdateCanonical` проверяет Revision/Fence monotonicity,
  append-only Events и обязательное same-commit событие на новой Task Revision
  для доменных Run mutations до единственного snapshot Save. Transport-only Run
  bookkeeping сохраняется без DomainEvent; отказ Save не публикует память
  (`TestCanonicalTransitionRequiresTaskRevisionAndEventForRunDomainChange`,
  `TestCanonicalTransitionAllowsTransportOnlyRunUpdateWithoutDomainEvent`,
  `TestCanonicalTransitionRejectsTaskMutationWithoutEvent`,
  `TestCanonicalStoreRejectsTaskMutationWithoutEvent`). Task event history
  проверяет ревизию последнего события, непрерывность EventSeq и соответствие
  `LastEventSeq` длине истории (`TestCanonicalStateRejectsTaskRevisionAheadOfEventHistory`,
  `TestCanonicalStateRejectsEventSequenceGapAfterFirstEvent`,
  `TestCanonicalStateRequiresLastEventSequenceToMatchHistoryLength`).
- Canonical schema v1 содержит typed Inbox receipts и дедуплицирующую
  `ApplyInboxCommand`, которая коммитит receipt с Task/Run, DomainEvent и Outbox;
  неизвестная версия отвергается без изменения snapshot
  (`TestCanonicalFileRejectsUnknownSchemaWithoutModifyingFile`,
  `TestCanonicalRunStorePersistsSingleTopLevelOutbox`,
  `TestCanonicalTaskOutboxRequiresEventFromSameCommit`,
  `TestApplyInboxCommandCommitsReceiptWithTaskEventAndDeduplicates`,
  `TestApplyInboxCommandRejectsStaleExpectedRevision`,
  `TestApplyInboxCommandRequiresSameCommitEventAndPreservesStateOnFailure`).
- R75b добавляет `task_fence` в DomainEvent schema v1 и startup-проверку полной
  цепочки по revision. Старый creation-only v1 snapshot без поля допускается:
  запись сохраняет schema v1 и сериализует поле. История с transfer marker без
  сохранённого Fence отклоняется fail-closed и не переписывается; legacy
  backfill не выполняется. Тесты совместимости: `TestCanonicalFileReadsAndRewritesCreationOnlyV1WithoutFenceField` и
  `TestCanonicalFileRejectsTransferredV1WithoutFenceAndPreservesSource`. Основание:
  решение OWNER R85 — поле вводится в schema v1 до первой production-записи
  DomainEvent; production-путей записи transfer events нет (`taskservice` пишет
  только `TASK_CREATED` и не используется `cmd/`, `ApplyInboxCommand` вызывается
  только из тестов), поэтому persisted истории с transfer marker без
  `task_fence` вне тестов не существует (R93).
- R75a закрывает storage transition gap: смена active writer требует повышения
  Task.Fence, matching Run.Fence и transfer event; recovery требует claim, а
  replacement Run требует terminal predecessor из предыдущего snapshot. Это
  структурная проверка, не supervisor stop proof и не разрешение на transfer.
  Task-scoped Run сохраняет назначенную роль, профиль выбранной роли обязан
  совпадать с RequiredRole, Session — с OwnerSession; изменение RoleSelection
  требует дописать ту же Selection в конец RoleHistory, а сохранённый префикс нельзя удалить или переписать
  по `agent_id`, роли и порядку. Это защищает основание проверки независимости
  AUDITOR из AGENTS.md, не задавая новую политику выбора ролей. Transfer commit
  содержит ровно один `TASK_OWNERSHIP_TRANSFERRED` для нового writer; terminal
  Run нельзя переактивировать или переписать его owner; recovery обязан очистить
  RecoveryNeeded и RecoveryClaim.
  Production Task command path и проверка входного message Fence против
  `Task.Fence` остаются открытыми.
- Storage validation P0-2 включает fail-closed startup-проверки: schema и
  обязательные коллекции; Task/Run identity и значения; ссылки Run→Task,
  Event→Task/Run, Outbox→Run/Event и Inbox→Task/Run/Event; единственный активный
  writer и согласованные Task/Run Fence; непрерывные EventSeq/TaskRevision,
  создание Task, transfer marker и цепочку Fence; Inbox receipt с EventIDs
  только из `CommittedRevision`; RoleSelection, совпадающий с последней записью
  RoleHistory. Прямые проверки последних двух правил —
  `TestCanonicalStartupRejectsInboxEventOutsideCommittedRevision` и
  `TestCanonicalStartupRejectsRoleSelectionNotMatchingLatestHistory`.
- Полнота Profile и реестр допустимых EventType остаются открытыми вопросами
  контракта и не являются критерием завершения P0-2. Owner authorization,
  production commands, проверка входного message Fence до dedup/side effects,
  Fence в Outbox envelope относительно Task.Fence и production Assign/Tick/
  recovery гарантия одного writer перенесены в P0-3 решением OWNER R100.
  Принятые Owner и Agent command-boundary модели остаются зависимостями P0-3
  (см. ROADMAP и `COMMAND-BOUNDARY.md`).

### Полный P0-2: Task aggregate и транзакции

Реализация принимается только после тестов на полной canonical persistence
boundary.

- TaskID создаётся как канонический `task_<ULID>`; коллизия при сохранении
  порождает повтор генерации; идентичность не зависит от external ref
  (`TestCreateTaskGeneratesCanonicalTaskID`,
  `TestTaskIDCollisionRetriesBeforeCommit`).
- `Run.TaskID` является явной связью, а legacy `Run.Task` сохраняется без
  переинтерпретации (`TestCanonicalStateValidatesTaskEventAndRunRelations`).
- Task, Run, DomainEvent, Outbox и обязательный Inbox receipt либо все commit,
  либо ни один; после ошибки памяти не публикуется и новые writes закрыты до
  reopen (`TestCanonicalTransitionCommitsStateEventOutboxAtomically`,
  `TestCanonicalCommitFailureDoesNotPublishOrContinue`). Успешный тест проверяет
  один durable Save и повторное чтение всех пяти частей из canonical snapshot.
- Task.Fence — авторитетное монотонное поле canonical storage; transition
  проверяет согласованность Fence, transfer event и отсутствие нескольких
  активных Task-scoped writers (`TestCanonicalTransitionRejectsSecondActiveWriter`,
  `TestCanonicalTransitionRecoveryPreservesAttemptAndIncrementsTaskFence`).
  Сравнение входного message Fence до dedup/ownership side effects,
  `TestTaskFenceIsAuthoritativeAndMonotonic`,
  `TestRecoveryAcrossRunsRejectsOldTaskFence`,
  `TestAssignTickRecoveryFailClosedWithMultipleWriters` и проверка Fence Outbox
  envelope относительно Task.Fence перенесены в P0-3 решением OWNER R100.
- Legacy state с незавершённым Run удерживает daemon not-ready, а только
  терминальные legacy Runs не открываются на запись и не считаются canonical
  (`TestCoorddActiveLegacyRunKeepsNotReadyWithoutStartingTransport`,
  `TestCoorddTerminalLegacyRunIsNotImported`).
- Неизвестная schema version удерживает процесс в диагностическом not-ready
  режиме без state mutation, Tick, transport receive или outbox delivery
  (`TestCoorddUnknownCanonicalSchemaKeepsNotReady`).
- Повреждённая/неподдерживаемая CanonicalState, висячая ссылка либо нарушение
  Fence/Writer/Event/Inbox/Outbox consistency блокирует `/readyz` и mutation
  (`TestCoorddReadinessRejectsInvalidCanonicalState`). Тест сверяет сообщение
  причины, отсутствие запуска transport и неизменность исходного snapshot.
- После restart сохраняются TaskID, Task/Run relation, Fence, Event Log,
  dedup receipts и незавершённый Outbox; доставленный transport ACK не
  подменяет Domain acceptance (`TestCoorddRestartRestoresCanonicalTaskStateAndOutbox`).
  Проверка использует terminal Task-scoped Run: Outbox сохраняет EventID после
  первой неудачной доставки и replay не меняет Task Revision, Events или Inbox.
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
  запускаются; `taskservice.Create` с конкретной причиной отклоняет план,
  содержащий этап, который runtime не умеет исполнять, до выделения TaskID и
  любого durable write. Проверки: `TestTaskCreateRejectsUnsupportedStageWithoutCommit`,
  `TestTaskCreateRejectsRequiredOwnerDecisionWithoutCommit`.
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
