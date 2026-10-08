# Task Model v1

- **Статус:** Accepted
- **Область:** идентичность Task, связь Task ↔ Run, lifecycle и attempt semantics
- **Основание:** ADR-0001 Canonical Coordination Model; ADR-0003 External Reference Model; ADR-0004 Runtime and Daemon Model; ADR-0005 Storage Model

## Цель

Определить границу Task aggregate до появления Task API и канонической
персистентной модели. Этот документ не меняет текущий wire protocol и не
переопределяет сохранённые `fsm.Run.Task` значения.

## Task identity и связь с существующим Run

- **TM-01 — TaskID принадлежит Coord и использует ULID.** `Task.ID` имеет тип `taskid.ID` и формат `task_<ULID>`. Он выдаётся Coord и
  не зависит от `Run.Task`, issue number, имени репозитория или других внешних
  идентификаторов.
  Проверка: частично — `TestCreateTaskGeneratesCanonicalTaskID`, `TestTaskIDCollisionRetriesBeforeCommit`; не реализовано: public/operator command (R52).

- **TM-02 — Run.TaskID — явная связь с Task.** Каждая новая каноническая Run содержит отдельное поле `Run.TaskID`, которое
  ссылается на `Task.ID`. Это отношение — каноническая связь Task ↔ Run.
  `Task` не хранит дублирующий список Run IDs: участники и история Runs находятся
  через индекс/поиск по `Run.TaskID`.
  Проверка: частично — `TestCanonicalStateValidatesTaskEventAndRunRelations`; не реализовано: production assignment не пишет Task-scoped Run.

- **TM-03 — Legacy Run.Task не переинтерпретируется.** Историческое `Run.Task` сохраняется byte-for-byte как legacy execution/protocol
  identity. Его смысл может быть описанием, названием, внешним ключом или
  одноразовой строкой. Запрещается неявно копировать его в `Task.ID` или `Run.TaskID`.
  В новом wire contract `TaskID` передаётся отдельным типизированным полем; старое
  поле `Task` остаётся до отдельного protocol migration и не служит заменой
  TaskID.
  Проверка: частично — `TestCanonicalStateValidatesTaskEventAndRunRelations`; не реализовано: wire migration отдельного поля TaskID.

- **TM-04 — Импорт требует однозначной mapping table.** Миграция существующего Run требует явной таблицы соответствий и проверки
  коллизий; строки без однозначного соответствия остаются legacy и не импортируются
  автоматически.
  Проверка: не реализовано

## Aggregate

```text
Task
├─ ID, Title, Description, OwnerID, Revision
├─ StagePlan + policy version
├─ CurrentStage
├─ Status
├─ Fence
├─ CreatedAt, UpdatedAt
└─ ExternalReferences

Run
├─ RunID
├─ TaskID ───────────────► Task.ID
├─ Role, SessionID
├─ Attempt
├─ Fence (snapshot)
├─ RunStatus
└─ Inputs, Result, Artifacts, timestamps
```

- **TM-05 — OwnerID и capability привязаны к человеку и generation.** `OwnerID` names a stable human principal, not an `agent_id`, session address or
  transport identity. Owner commands authenticate that principal through a
  session-scoped capability bound to the current generation and TaskID.
  Проверка: не реализовано

- **TM-06 — Revision является CAS-версией Task.** `Revision` increments on every canonical Task mutation and is the compare-and-swap
  version for Owner commands and conditional writes. It is not a separate FSM.
  Task-owned relations and workflow transitions are committed by Coord. Domain
  Events, Review/Audit, Discussion and Outbox records are associated with TaskID and
  written in the same transaction as the state transition. Derived projections do
  not add stored Task status fields.
  Проверка: частично — `TestCanonicalTransitionRejectsTaskMutationWithoutEvent`, `TestCanonicalStoreRejectsTaskMutationWithoutEvent`, `TestCanonicalTransitionRejectsTaskMutationWithoutRevisionIncrement`, `TestCanonicalStateRejectsTaskRevisionAheadOfEventHistory`; не реализовано: authenticated expected-Revision Owner command boundary.
- **TM-26 — EventSeq непрерывен внутри Task.** Каждый Domain Event Task имеет
  `EventSeq`: монотонный, уникальный и непрерывный с 1 в пределах Task.
  `Task.LastEventSeq` равен номеру последнего события и присваивается в той же
  фиксации, что изменение Task/Run, событие и Outbox. Пропуск или повтор номера
  блокирует readiness. Решение owner 2026-10-08 (R56).
  Проверка: частично — `TestCanonicalStateRejectsEventSequenceGapAfterFirstEvent`, `TestCanonicalStateRequiresLastEventSequenceToMatchHistoryLength`; не реализовано: фиксация EventSeq при Task/Run переходе вместе с Outbox.

## Независимые оси Task

- **TM-07 — Stage и Status — разные оси.** `CurrentStage` и `Status` — разные поля и разные значения.
  Проверка: частично — `TestTaskValidationSeparatesStageAndStatus`; не реализовано: переходы workflow, которые меняют Stage отдельно от Status.

### Stage

Stage отвечает, какой этап workflow сейчас выполняется:

- `EXECUTION`
- `REVIEW`
- `AUDIT`
- `OWNER_DECISION`

- **TM-08 — StagePlan имеет фиксированную грамматику.** `StagePlan` задаёт обязательные и разрешённые этапы. Пропуск необязательного
  Review/Audit фиксируется policy и transition event, а не выводится Dashboard.
  Для MVP его грамматика ограничена: `EXECUTION` обязателен и идёт первым;
  `REVIEW` и `AUDIT` могут быть опущены согласно policy, но если оба включены,
  `REVIEW` предшествует `AUDIT`. Требование финального решения Owner задаётся
  отдельным флагом policy. `OWNER_DECISION` — условное прерывание, не этап,
  который можно добавлять или удалять произвольным текстом. До реализации
  workflow handlers P0-2 принимает при создании Task только исполняемый
  `EXECUTION` без обязательного Owner decision; остальные допустимые по грамматике
  планы отвергаются `taskservice.Create` до записи состояния.
  Проверка: частично — `TestTaskValidationRejectsInvalidPlanAndTerminalStage`, `TestTaskCreateRejectsUnsupportedStageWithoutCommit`, `TestTaskCreateRejectsRequiredOwnerDecisionWithoutCommit`; не реализовано: runtime переходы и policy-driven пропуск стадий.

- **TM-09 — StagePlan меняется только Owner-командой с revision.** Task хранит валидированный StagePlan и его `PolicyVersion` как канонические
  данные. Это не скрытая вторая policy: переходы Coord используют только текущий
  снимок StagePlan в Task и его известную версию семантики. Изменить план можно
  только явной Owner-командой с ожидаемой Task Revision; изменение фиксируется
  событием и новой revision. Незаметное изменение внешней project config не меняет
  уже созданные Task.
  Проверка: не реализовано

- **TM-10 — Терминальный Task не имеет CurrentStage.** У терминального Task `CurrentStage` отсутствует; завершение
  представлено `Status=COMPLETED`.
  Проверка: частично — `TestTaskValidationRejectsInvalidPlanAndTerminalStage`; не реализовано: terminal workflow command и сохранение результата.

### Status

Status отвечает, может ли работа продолжаться и завершилась ли она:

- `PENDING` — Task создан, исполнение ещё не принято в работу;
- `IN_PROGRESS` — workflow выполняется или ждёт следующего автоматического Run;
- `BLOCKED` — продвижение невозможно до устранения blocker или внешнего решения;
- `FAILED` — Task завершён неуспешно по явному правилу;
- `COMPLETED` — workflow достиг принятого terminal outcome.

- **TM-11 — NEEDS_OWNER_DECISION не является Task Status.** `NEEDS_OWNER_DECISION` не является Task Status. Это `CurrentStage=OWNER_DECISION`,
  `Status=BLOCKED` и производная Attention для OWNER с причиной, основанием и
  разрешёнными вариантами. Агентский `RunStatus=BLOCKED` не автоматически делает
  Task заблокированной: текущий Run может быть восстановлен в рамках того же
  назначения после устранения причины.
  Проверка: частично — `TestTaskValidationRejectsInvalidPlanAndTerminalStage`; не реализовано: Attention projection и Owner decision workflow.

- **TM-12 — BLOCKED сохраняет текущий Stage.** Если продвижение остановилось, Task получает `Status=BLOCKED` и сохраняет текущий Stage.
  Проверка: не реализовано

Новое writer assignment запрещено, пока предыдущий Run не восстановлен в том же
ownership либо не переведён в терминальный `SUPERSEDED` после подтверждённой
остановки старого writer.

`TaskStatus` и `RunStatus` — разные типы, хотя некоторые строковые значения
совпадают. `TaskStatus.PENDING` означает «Task ещё не взят в работу»;
`RunStatus.PENDING` означает «Run назначен, ждёт ACK».

- **TM-13 — Неподтверждённая остановка блокирует продвижение.** Если Coord committed результат Run, но supervisor ещё не подтвердил остановку
  процесса, Task получает `Status=BLOCKED` и сохраняет текущий Stage. Причина
  выводится как `WAITING_FOR_SUPERVISOR_STOP`; Attention показывает ожидаемый Run,
  session/process generation и то, что Coord ждёт stop proof от supervisor. Это
  вычисляется из committed результата и отсутствующего обязательного proof, а не
  хранится как отдельная очередь. Run может быть `DONE`, пока Task заблокирован на
  границе передачи; интерфейс не должен изображать этот период как активную работу.
  Проверка: не реализовано

- **TM-14 — RunViewStatus только проекция.** `RunViewStatus` — только вычисляемое представление для человека. Оно не
  принимается командами и не сохраняется как второе состояние Run.
  Проверка: не реализовано

- **TM-15 — REWORK_REQUIRED создаёт новый Executor Run.** `REWORK_REQUIRED` — не отдельная Stage/Status. Это результат Review/Audit,
  который атомарно переводит Task обратно в `EXECUTION`, сохраняет событие и
  создаёт новый Executor Run в следующем task attempt.
  Проверка: не реализовано

## Run и Attempt

Run — одно назначение одной роли внутри одного workflow attempt. Run FSM хранит
протокольное исполнение роли; Task Stage хранит положение всей работы.

- **TM-16 — RunStatus канонический, view-статусы не сохраняются.** Авторитетный `RunStatus` — протокольная FSM: `PENDING`, `ACCEPTED`, `STARTED`,
  `BLOCKED`, `STALLED`, `RESULT_READY`, `DONE`, `FAILED`, `SUPERSEDED`.
  `SUPERSEDED` — целевое терминальное состояние, ещё не добавленное в текущий
  `fsm.Status`. `ASSIGNED`, `READY`, `WORKING` и `COMPLETED` могут появляться только
  в неперсистентном `RunViewStatus` для интерфейса: соответственно как проекции
  `PENDING`, `ACCEPTED`, `STARTED` и `DONE` с durable Result record. Они не
  принимаются FSM и не записываются как RunStatus. View также обязан явно сохранять
  `STALLED` и `RESULT_READY`, чтобы не выдавать устаревшую работу или неподтверждённый
  результат за обычный `WORKING`/`COMPLETED`.
  Проверка: частично — `TestRestartRejectsUnknownCanonicalStatus`, `TestCanonicalStateRejectsInvalidRunFSMState`, `TestRunValidateRejectsPersistedFSMInvariantViolations`; не реализовано: RunViewStatus и статус SUPERSEDED

- **TM-17 — Attempt начинается с 1 и общий для Review/Audit прохода.** Начальный Executor Run начинает Attempt `1`.
  Reviewer и Auditor Runs, назначенные в том же проходе workflow, используют тот
  же Attempt.
  Проверка: не реализовано
- **TM-18 — Rework требует подтверждённой остановки writer.** `CHANGES_REQUIRED` от Reviewer/Auditor начинает следующий Attempt: Coord
  терминально переводит старый Executor Run в `SUPERSEDED` и создаёт новый
  Executor Run с `Attempt+1` только после подтверждения supervisor, что предыдущий
  writer остановлен и больше не владеет рабочей границей.
  Проверка: не реализовано
- **TM-19 — Transport retry сохраняет RunID и Attempt.** Transport retry и повторная доставка сохраняют RunID и Attempt.
  Проверка: `TestAckRetryPreservesAttemptAndDeduplicates`, `TestRunInvariantsHoldThroughLifecycle`
- **TM-20 — Recovery сохраняет RunID и Attempt.** Recovery сохраняет RunID и Attempt; после подтверждённой остановки writer
  повышает Task.Fence и присваивает новый Run.Fence snapshot.
  Проверка: частично — `TestRunInvariantsHoldThroughLifecycle`, `TestRecoveryCASAndOldOwnerRejected`; не реализовано: stop proof supervisor до передачи и Task.Fence
- **TM-21 — Attempt восстанавливается из committed истории.** Каждый Run имеет собственный неизменяемый Attempt; Attempt является номером
  прохода Task workflow и копируется в каждый Run этого прохода. Источник
  следующего номера — максимальный committed Attempt в истории Runs данного
  Task плюс один при rework; текущий Task status не используется для угадывания.
  Проверка: не реализовано

- **TM-22 — RunID уникален и не переиспользуется.** Run ID уникален в Coord и никогда не переиспользуется. Смена роли создаёт новый
  Run, но не увеличивает Attempt, пока не начат rework-проход.
  Проверка: не реализовано

- **TM-23 — Reviewer независим по agent_id и истории.** До создания Reviewer Run Coord проверяет стабильный `agent_id` и всю историю
  Executor Runs этого Task. Reviewer с тем же `agent_id`, что любой текущий или
  предыдущий Executor, отклоняется: он не может проверять собственную работу.
  Проверка выполняется до назначения, включая recovery и новые session адреса.
  Если независимого Reviewer нет, Task остаётся blocked на Stage REVIEW с причиной
  `NO_INDEPENDENT_REVIEWER`; Coord не подменяет его Executor, а Owner не может
  отменить hard policy. Аналогичные ограничения AUDITOR задаются ADR-0001.
  Проверка: не реализовано

## Task-wide Fence и ownership

`Task.Fence` — авторитетная монотонная эпоха записи в Task; `Run.Fence` — снимок,
с которым назначен конкретный Run. Входящая агентская запись принимается только
если TaskID, RunID, Attempt, session generation и Run.Fence совпадают с текущим
Task ownership и текущим `Task.Fence`.

На Task допускается не больше одного активного writer Run. Assign, stage
transition, rework, recovery и startup validation проверяют инвариант fail-closed.
Каждый переход, который завершает текущий Run и назначает другого writer,
требует подтверждения внешнего supervisor об остановке прежнего процесса до
фиксации передачи ownership. Supervisor передаёт stop proof через daemon command
path; Coord проверяет привязку к TaskID, RunID, Attempt, Fence, session generation
и process generation, затем durable-коммитит receipt и диагностическое событие.
Последующий переход ссылается на committed proof ID и атомарно сохраняет
смену Task/Run, Domain Event и Outbox. RESULT commit и `RESULT_ACK` подтверждают
разные факты: первый — принятие результата Coord, второй — исходящее подтверждение
агенту; ни одно само по себе не доказывает, что процесс агента остановлен. Без
проверенного stop proof Task получает `Status=BLOCKED`, сохраняет текущий Stage,
а причина `WAITING_FOR_SUPERVISOR_STOP` и Attention вычисляются из отсутствующего
proof; новый writer Run не создаётся.

При передаче agent-write ownership новый Run получает повышенный Fence; старый
writer должен быть остановлен внешним supervisor до разрешения следующему writer.
Первое назначение использует первую положительную эпоху. Rework повышает Fence
один раз при создании следующего Executor Run; не следует
повышать его отдельно за supersede и за создание того же writer.

- **TM-24 — Owner decision требует Task capability.** Owner decision не является агентским Run. Оно проходит через Task-scoped
  capability и CAS по текущей версии Task под daemon command boundary. Owner
  decision обновляет Task и Event, но не выдаёт агенту право записи и само не
  повышает Fence; последующее agent assignment повышает Fence до передачи
  ownership. Hard policy остаётся неизменной.
  Проверка: не реализовано

Эскалация Owner decision фиксирует в Domain Event исходный Stage, вопрос,
evidence и разрешённые варианты. Task Revision и выбор Owner не разрешают
перейти в произвольный Stage: Coord применяет только переход, соответствующий
выбранному варианту и StagePlan. Возврат к исполнению фиксируется как rework с
новым Attempt. Если решение разрешает блокировку `DEPENDS_ON`, связанная команда
изменения графа проверяет ожидаемые Task и Graph revisions и фиксирует
`OWNER_DECISION` вместе с изменениями рёбер одним commit по ADR-0002 DG-07/08.

Разрешение `DEPENDS_ON`, закрывающей старт, является Owner decision над графом,
а не переходом Task в `OWNER_DECISION`: Task до первого старта остаётся
`PENDING`, а при rework — `BLOCKED / EXECUTION`. Owner может оставить задачу
ждать, удалить указанное ребро или атомарно заменить его другим prerequisite.
Команда требует ожидаемые Task и Graph revisions; она записывает `OWNER_DECISION`
и все связанные `PROJECT_EDGE_REMOVED`/`PROJECT_EDGE_ADDED` события одним commit.
Coord повторно проверяет полный набор оставшихся prerequisites и не создаёт
Executor Run, пока каждый из них не `COMPLETED`. Owner не может решением
изменить статус prerequisite или обойти иные hard policy.

- **TM-25 — Секретный handoff не проходит через Coord.** Человеческий ввод секрета/MFA — отдельный защищённый handoff, а не Owner
  decision и не событие с текстовым значением. Значение передаётся напрямую по
  защищённому каналу и не проходит через Coord; Coord может сохранить только
  нечувствительную ссылку на handoff и проверяемый исход. Если после handoff
  продолжается тот же заблокированный Run с тем же владельцем, его RunID, Attempt
  и Fence не меняются. Смена writer всё равно требует stop proof и нового Fence.
  Проверка: не реализовано

## Stage transitions

Все условия ниже валидирует Coord. Каждая запись Task/Run, Domain Event и
необходимый Outbox фиксируется одним Commit.

Переход в Stage, которому StagePlan назначает агентскую роль, создаёт Run этой
роли и передаёт ему право записи только после committed stop proof предыдущего
writer; Fence повышается в той же транзакции. Первое назначение использует
первую положительную эпоху. Переход в `OWNER_DECISION` Run не создаёт.

| Текущее состояние | Условие | Следующее состояние |
|---|---|---|
| `PENDING / EXECUTION` | Есть TASK prerequisite со Status != `COMPLETED` | Остаётся `PENDING / EXECUTION`; Executor Run не создаётся, Attention показывает prerequisite и Owner может решить судьбу связи по ADR-0002 DG-07/08 |
| `PENDING / EXECUTION` | Все `DEPENDS_ON` prerequisites выполнены (ADR-0002 DG-07) | Первый Executor Run назначен тем же commit, что и проверка; `IN_PROGRESS / EXECUTION` |
| `IN_PROGRESS / EXECUTION` | SUCCESS ResultRecord durable-коммитнут | `BLOCKED / EXECUTION`, причина `WAITING_FOR_SUPERVISOR_STOP`; Run terminal, следующая роль ещё не назначена |
| `BLOCKED / EXECUTION` | SUCCESS ResultRecord и committed stop proof Executor имеются | `IN_PROGRESS / REVIEW` либо `AUDIT` согласно StagePlan; создать следующий Run с повышенным Fence |
| `IN_PROGRESS / REVIEW` | Reviewer независим по `agent_id`; verdict `APPROVED` durable-коммитнут | `BLOCKED / REVIEW`, причина `WAITING_FOR_SUPERVISOR_STOP`; Reviewer terminal, следующая роль ещё не назначена |
| `BLOCKED / REVIEW` | APPROVED ReviewResult и committed stop proof Reviewer имеются | `IN_PROGRESS / AUDIT`, `BLOCKED / OWNER_DECISION` либо `COMPLETED` согласно StagePlan и Owner-decision policy |
| `IN_PROGRESS / REVIEW` | `CHANGES_REQUIRED` durable-коммитнут | `BLOCKED / REVIEW`, причина `WAITING_FOR_SUPERVISOR_STOP`; новая Executor Run ещё не назначена |
| `BLOCKED / REVIEW` | `CHANGES_REQUIRED`, committed stop proof Reviewer и предыдущего Executor имеются, все TASK prerequisites `COMPLETED` | Предыдущий Executor Run=`SUPERSEDED`, новый Attempt, повышенный Task.Fence, `IN_PROGRESS / EXECUTION` |
| `BLOCKED / REVIEW` | Условия rework выполнены, но TASK prerequisite не `COMPLETED` | `BLOCKED / EXECUTION`; прежний Executor Run сохраняет terminal status, новый Attempt/Run не создаётся до разрешения зависимости |
| `IN_PROGRESS / AUDIT` | Audit verdict `PASS` durable-коммитнут | `BLOCKED / AUDIT`, причина `WAITING_FOR_SUPERVISOR_STOP`; Auditor terminal, Task ещё не завершён |
| `BLOCKED / AUDIT` | PASS AuditResult и committed stop proof Auditor имеются | `COMPLETED` либо `BLOCKED / OWNER_DECISION` по policy |
| `IN_PROGRESS / AUDIT` | Audit требует исправления, verdict durable-коммитнут | `BLOCKED / AUDIT`, причина `WAITING_FOR_SUPERVISOR_STOP`; новая Executor Run ещё не назначена |
| `BLOCKED / AUDIT` | Требуется исправление; committed stop proof Auditor и предыдущего Executor имеются, все TASK prerequisites `COMPLETED` | Предыдущий Executor Run=`SUPERSEDED`, новый Attempt, повышенный Task.Fence, `IN_PROGRESS / EXECUTION` |
| `BLOCKED / AUDIT` | Условия rework выполнены, но TASK prerequisite не `COMPLETED` | `BLOCKED / EXECUTION`; прежний Executor Run сохраняет terminal status, новый Attempt/Run не создаётся до разрешения зависимости |
| `IN_PROGRESS / любой агентский Stage` | Run завершился с failure result, но committed stop proof ещё отсутствует | `BLOCKED / тот же Stage`, причина `WAITING_FOR_SUPERVISOR_STOP`; failure outcome сохранён, writer не назначен |
| `BLOCKED / любой агентский Stage` | Failure outcome и committed stop proof имеются; policy не задаёт recovery/retry | `FAILED` |
| любой незавершённый | Явная эскалация требует Owner decision; текущий writer остановлен, stop proof committed; автоматический переход StagePlan не разрешает исход | `BLOCKED / OWNER_DECISION` |
| любой незавершённый Stage | Требуется секрет/MFA; защищённый handoff, без смены Stage и ownership | `BLOCKED / тот же Stage`; после handoff тот же Run может продолжить с тем же Fence |
| `BLOCKED / OWNER_DECISION` | Авторизованное Owner решение выбирает один из вариантов запроса, записанного в событии эскалации | Только указанный вариантом следующий разрешённый Stage или `COMPLETED`; разрешение dependency также может атомарно менять Project Graph по ADR-0002 DG-07/08 |
| `BLOCKED / OWNER_DECISION` | Owner решение требует исправления; stop proof текущего writer подтверждён; все TASK prerequisites `COMPLETED` | Новый Attempt и Executor Run, повышенный Task.Fence, `IN_PROGRESS / EXECUTION` |
| `BLOCKED / OWNER_DECISION` | Owner решение требует исправления, но TASK prerequisite не `COMPLETED` | `BLOCKED / EXECUTION`; новый Attempt/Executor Run не создаётся до разрешения зависимости по ADR-0002 DG-07/08 |
| `BLOCKED / любой Stage` | Причина не `WAITING_FOR_SUPERVISOR_STOP`; Run остаётся нетерминальным; blocker устранён, тот же owner/session безопасно продолжает с тем же Fence | `IN_PROGRESS / тот же Stage` |
| `BLOCKED / любой Stage` | Причина не `WAITING_FOR_SUPERVISOR_STOP`; Run нетерминальный; требуется recovery, supervisor подтвердил остановку старого процесса | Тот же RunID и Attempt, новый owner/session и Run.Fence snapshot при повышенном Task.Fence; тот же Stage |
| любой незавершённый | Отказ по policy без Run failure outcome; если writer ещё жив, supervisor stop proof обязателен | `FAILED` |

Таблица задаёт допустимые Task transitions, а не полный операционный протокол.
Явная Owner-эскалация не конкурирует с автоматическим исходом Stage: она
создаётся только отдельным событием требования human-only решения или
применением Owner-decision policy.
Приём ResultRecord, terminal outcome Run, переход Task в ожидание stop proof,
DomainEvent и необходимые Outbox records фиксируются одним Coord commit.
Supervisor stop proof поступает отдельной командой через daemon и также
durable-коммитится; только после этого следующий commit может сослаться на proof
ID, обновить Task/Run и назначить writer по StagePlan. Delivery `RESULT_ACK` —
отдельная Outbox операция; она не является условием принятия результата и не
доказывает остановку процесса. `RunStatus=DONE` означает, что Coord принял и
committed результат, но не утверждает, что процесс уже остановлен.

## Открытые до реализации проверки

- Зафиксировать wire schema version для отдельного `TaskID` без изменения
  значения legacy поля `Task`.
- Определить точный набор структурированных verdicts; StagePlan grammar и
  правила его явной Owner-мутации заданы выше и требуют реализации.
- Уточнить, когда failure является `RunStatus=FAILED`, а когда Task переводится
  в `FAILED` или создаёт новый Run.
- Уточнить schema version Task/Run/Event/Outbox и wire schema для stop proof.
- Переносить только те legacy Runs, для которых owner предоставил однозначный
  TaskID mapping; не выводить Task identity из `Run.Task`.
