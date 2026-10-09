# Review/Audit и Discussion Graph — design

- **Статус:** Draft
- **OWNER decision:** R108 (2026-10-08), зафиксировано в этом draft
- **Этап:** после P0-2, ADR-0001 шаги 8–9, CM-03, CM-08, CM-11
- **Нормативные источники:** Accepted ADR-0001, TASK-MODEL; durable boundary —
  ADR-0004 RT-04 и ADR-0005 ST-01/ST-04
- **Назначение:** описать целевую форму структурированных результатов Review и
  Audit и event-backed Discussion Graph до реализации. Этот draft не меняет
  принятые нормы и не подключает production agent/Owner write path.

## 1. Область

Этап добавляет к canonical Task:

- структурированные `ReviewResult` и `AuditResult`;
- фиксацию требующего арбитража конфликта Review/Audit до Owner escalation;
- типизированные Discussion facts и edges, восстанавливаемые из канонических
  Domain Events;
- проверку независимости Reviewer и Auditor по стабильному `agent_id` и истории
  Executor Runs этой Task.

Внутри этапа разрешён только внутренний Task command path: модель, persisted
схема, startup validation и атомарные операции через существующую границу Store
(например, `ApplyInboxCommand`/`UpdateCanonical`) с fake clock/transport в
сценарных тестах. Это проверяет инварианты до P0-3, не создавая внешнего
production writer. Production-команды агентов и Owner, аутентификация и
capability остаются в P0-3.

Это не вводит вторую FSM, чат, отдельную БД для обсуждения или состояние Graph
для Dashboard. `Task.Stage`, `Task.Status`, `Run.Status`, `Attempt` и `Task.Fence`
остаются в границах, заданных ADR-0001 и TASK-MODEL.

## 2. Фактическая база

P0-2 добавил versioned `CanonicalState` с `Tasks`, `Runs`, `Events`, `Inbox` и
`Outbox`. `task.DomainEvent` сейчас содержит TaskID, optional RunID, event
sequence, Task revision/fence, type, actor, summary и timestamp. Store проверяет
ссылки, цепочку Task revisions/fence и роль/историю выбора внутри Task-scoped
Run. Типов `ReviewResult`, `AuditResult`, `DiscussionNode` и `DiscussionEdge` в
коде пока нет; текущие event types ограничены `TASK_CREATED` и
`TASK_OWNERSHIP_TRANSFERRED`.

`roles.Selection` уже несёт стабильный `AgentID`, выбранную роль, session и
profile. `fsm.Run` хранит `RoleSelection` и append-only `RoleHistory`. Текущий
`Coordinator.selectRole` и `TestRolesIndependenceAndImmutableAssignment`
проверяют конфликт Executor/Auditor по AgentID и истории для legacy `Run.Task`.
Это не является реализацией проверки Reviewer/Auditor для canonical TaskID и
workflow; production Task assignment отсутствует.

## 3. Структурированные результаты

### 3.1. ReviewResult и AuditResult

Accepted ADR-0001 требует для обоих результатов структурированные `verdict`,
`findings`, `summary` и ссылки на входные/выходные artifacts. TASK-MODEL
определяет используемые переходы:

- Reviewer `APPROVED` — продолжить на `AUDIT`, если он задан в StagePlan, либо
  завершить Task согласно принятой policy, но только после stop proof;
- Reviewer `CHANGES_REQUIRED` — сохранить durable result и перевести Task в
  `BLOCKED/WAITING_FOR_SUPERVISOR_STOP`; новый Executor Run и Attempt не
  создаются, пока stop proof не подтверждён;
- Auditor `PASS` — сохранить durable result и перейти в
  `BLOCKED/WAITING_FOR_SUPERVISOR_STOP`; после stop proof Task завершается либо
  переходит к `OWNER_DECISION` согласно policy;
- Auditor `CHANGES_REQUIRED` (имя закреплено R108; TASK-MODEL:297 говорит
  «Audit требует исправления») — сохранить durable result и перевести Task в
  `BLOCKED/WAITING_FOR_SUPERVISOR_STOP`; rework новым Executor Run и Attempt
  начинается только после stop proof.

Канонические verdict values по R108:

- `ReviewResult.verdict`: `APPROVED` или `CHANGES_REQUIRED`;
- `AuditResult.verdict`: `PASS` или `CHANGES_REQUIRED`.

TASK-MODEL:297 описывает Audit outcome как «Audit требует исправления»; имя
`CHANGES_REQUIRED` для этого исхода впервые закреплено решением OWNER R108.
TASK-MODEL не меняется этим документом. `BLOCKED` не является verdict: когда
Reviewer/Auditor не может вынести verdict, этап не принимает `BLOCKED` вместо
verdict и фиксирует Discussion node типа `BLOCKER`. Конкретные код причины,
Stage/Status и workflow-переход для этого случая нормами не заданы и остаются
UNKNOWN, вне scope этого этапа.

Этот этап реализует фиксацию verdict и переход в ожидание stop proof. Он не
реализует получение/проверку stop proof, снятие блокировки или продолжение
workflow после подтверждения: это остаётся в P0-5 и соответствующем supervisor
design. Внутренние тесты проверяют, что до proof нет следующего Run, Attempt,
ownership transfer или Fence bump, а после перезапуска сохраняются verdict и
`WAITING_FOR_SUPERVISOR_STOP`.

Концептуально результат связан с Task и Run, который его создал, и содержит:

```text
ReviewResult / AuditResult
├─ identity and Task/Run association
├─ verdict
├─ findings
├─ summary
└─ references to input/output artifacts
```

`ReviewResult` и `AuditResult` хранятся typed payload соответствующего
DomainEvent и восстанавливаются из Event history; отдельного хранилища нет
(R108). Каждый Finding содержит `ID` (выдаёт Coord), `Severity` из
`CRITICAL | MAJOR | MINOR`, `Summary`, `Evidence` references и `CreatedAt`.
Дополнительные поля могут появиться позднее по отдельному решению.

Severity Finding носит описательный характер и не влияет на переход Task; переход
определяется только verdict. Невозможность выдать verdict выражается workflow
blocker и Discussion `BLOCKER`, а не новым verdict.

Результат должен быть связан с ролью и Attempt канонически назначенного Run:
ReviewResult создаётся Reviewer Run в Stage `REVIEW`, AuditResult — Auditor
Run в Stage `AUDIT`; Reviewer и Auditor Runs того же workflow pass используют
Attempt Executor этого pass (TM-17). Входные evidence/artifact references
проверяются на существование и принадлежность разрешённой области Task. Проверка
содержимого Artifact остаётся вне этого этапа.

Результат не восстанавливается разбором текста. Текстовые поля проходят
входную политику CM-09: quarantine/redaction до durable записи, а history export
и диагностические логи имеют отдельные проверки. Внутренние сценарные тесты
должны упражнять эту границу; подключение внешнего production ingest остаётся
P0-3. Свободный текст нельзя считать безопасным по умолчанию.

### 3.2. Каноническое представление

Review/Audit — канонические доменные результаты, а не Dashboard projections.
Внутренний command path этапа записывает результат, соответствующий Domain Event
и необходимые Outbox/Inbox records одной транзакцией по CM-01, RT-04 и ST-01;
реальная daemon-facing команда агента появится в P0-3. Transport delivery не
означает принятие результата.

Typed Review/Audit payload находится в соответствующем DomainEvent; Event history
является единственным источником восстановления результата (решение OWNER R108).
Отдельной canonical result collection или хранилища нет. Startup validation
проверяет соответствие payload типу события, Task/Run/role и ревизии.

## 4. Конфликт Review/Audit и Owner escalation

CM-08 требует записать требующий арбитража конфликт как явный DomainEvent и
связанный Discussion fact до перехода Task в `NEEDS_OWNER_DECISION`. Ответ
Owner также становится событием и ссылается на этот конфликт. Workflow
представляет ожидание как `CurrentStage=OWNER_DECISION` и `Status=BLOCKED`, а не
как новый Run status.

Минимально требуемая причинная цепочка:

```text
ReviewResult ─┐
              ├─ conflict DomainEvent + linked Discussion fact
AuditResult ──┘                         │
                                       ▼
                              OWNER_DECISION event
                              (references conflict)
```

Конфликт, связанный факт Discussion и эскалация к OWNER фиксируются атомарно в
одной транзакции (решение OWNER R137, вариант A). Порядок внутри commit: Conflict
Event → связанный Discussion Fact → OWNER Escalation Event → Task переходит в
предусмотренное состояние ожидания решения → при необходимости формируется Outbox →
один durable commit. Conflict Event и Discussion Fact получают меньшие порядковые
номера, чем OWNER Escalation Event; все записи относятся к одной Task и одной новой
ревизии перехода; эскалация ссылается на ConflictID, а Discussion Fact — на тот же
ConflictID и на исходные Review/Audit results. Новое состояние публикуется в памяти
только после успешного durable commit, а при ошибке commit не видны ни конфликт, ни
Discussion Fact, ни эскалация. Повтор команды после успешного commit возвращает
прежний receipt и не создаёт вторую эскалацию. Вариант с двумя commit отклонён: он
создаёт промежуточное состояние и требует отдельной идемпотентной recovery-машины без
доказанной необходимости, поэтому отдельный recovery для состояния «конфликт записан,
эскалации ещё нет» не требуется — такого промежуточного канонического состояния не
возникает. Норма зафиксирована в ADR-0001 CM-08b.

Ни ADR-0001, ни TASK-MODEL не определяют алгоритм обнаружения конфликта: разные
verdicts, противоречащие findings или отдельный explicit arbitration request.
Поэтому нельзя автоматически эскалировать любое несовпадение или молча выбрать
победившую роль. Критерий конфликта и допустимые Owner options рассматриваются
в §7. Решение Owner не отменяет hard policy (CM-07),
независимость AUDITOR или требования fencing/stop proof.

## 5. Discussion Graph из Domain Events

Graph состоит из канонических фактов, а не транспортных сообщений. Разрешённые
node types по ADR-0001: `QUESTION`, `PROPOSAL`, `FACT`, `FINDING`, `DECISION`,
`ACTION`, `RESULT`, `BLOCKER`. Edge types: `ANSWERS`, `SUPPORTS`, `CAUSED_BY`,
`RESULT_OF`, `BLOCKS`, `REFERS_TO`.

По R108 создание node и edge фиксируется отдельными типизированными DomainEvents;
общего free-form message/event для графа нет. Node ID и Edge ID выдаёт Coord.
Payload каждого события структурирован согласно типу узла/ребра.

CM-11 устанавливает единственный источник истины: создание/изменение node и edge
представлено типизированным DomainEvent с устойчивым node/edge ID и payload.
Graph View может быть материализован для чтения, но восстанавливается из этих
событий и не имеет независимого write path. Event history и граф должны
сохранять TaskID, ссылки на Run/actor, sequence и причинные references.

Во внутреннем пути этапа actor и role agent-authored result/Discussion event
берутся Coord из `Run.RoleSelection` канонического Run (`AgentID`, роль); значения
actor/role из command payload не являются источником истины и отклоняются либо
игнорируются. P0-3 добавит проверку, что аутентифицированный отправитель совпадает
с `AgentID` этого Run. Owner events не выводят actor из Run и требуют отдельной
Owner authority в P0-3. Evidence ссылается на artifact или проверяемый источник;
обязательные payload-поля для каждого node/edge type заданы решениями R138 и R139 и
разбираются в §5.1; R108 определяет Finding внутри ReviewResult/AuditResult, но не
поля Discussion node/edge.

Ни ACK, HEARTBEAT, delivery, transport retry, Outbox retry, ни произвольные
реплики не становятся Discussion node автоматически. Отдельной FSM Discussion
нет. Существующая Project Dependency Graph (ADR-0002) остаётся отдельной моделью
relations между проектными узлами; её рёбра нельзя использовать как Discussion
edges.

Параметры событий, ID, правки и восстановления перечислены в §7. Любой
утверждённый вариант обязан обеспечить детерминированное восстановление Graph
только из canonical Events.

### 5.1. Схема Discussion (R138, R139)

Решения OWNER R138 и R139, вариант B: Discussion Graph типизирован, тип узла — не
свободная метка. Вариант A отклонён. Вариант C отложен: `ArtifactID`, `Digest` и локатор
цитаты зависят от R135 и от design проверки содержимого; позже B расширяется этими полями
без замены базовой модели. Ниже каждое поле и каждая допустимая пара указаны с
источником — R137, R138, R139, CM-08, CM-10a или CM-11.

**1. Восемь типов узлов и поля сверх общего минимума**

Общий минимум для любого узла (R138): `NodeID`, `Type`, `TaskID`, `Actor`, `Role`,
`CreatedAt`, `Summary`, `Refs[]`. `Actor` берётся из проверенного `AgentID` или OWNER
identity (CM-10a, R136).

| Тип узла | Поля сверх общего минимума | Источник |
|---|---|---|
| `QUESTION` | только общий минимум, новых полей нет | R139 |
| `PROPOSAL` | только общий минимум, новых полей нет | R139 |
| `FACT` | только общий минимум; `Refs[]` не пуст; при наличии `ConflictID` обязательны `ReviewResultRef` и `AuditResultRef` (R137, п.5) | R139; `Refs[]` — ADR-0001, раздел Startup Card; дополнение по R137 |
| `FINDING` | `ReviewResultRef` или `AuditResultRef`; `Refs[]` не пуст | R138, пример OWNER; `Refs[]` — R139, ADR-0001, раздел Startup Card |
| `DECISION` | `OwnerDecisionRef` или `ConflictID` | R138, пример OWNER |
| `ACTION` | только общий минимум, новых полей нет | R139 |
| `RESULT` | `ResultRef` | R138, пример OWNER |
| `BLOCKER` | `ReasonCode` | R138, пример OWNER |

Ни один тип узла не влияет на Task или workflow (CM-10a, R136): изменение workflow
выполняется только отдельным каноническим переходом. Поэтому отдельный вопрос OWNER о
влиянии типов не возникает.

**2. Шесть типов связей и матрица допустимых пар**

Общий минимум для любой связи (R138): `EdgeID`, `Type`, `FromNodeID`, `ToNodeID`,
`TaskID`, `Actor`, `CreatedAt`.

R138 вводит матрицу как требование, а R139 задаёт её полностью: шесть типов связей и все
допустимые пары (From type → To type) приведены ниже.

| Тип связи | Допустимый `From type` | Допустимый `To type` | Источник |
|---|---|---|---|
| `ANSWERS` | `FACT`, `PROPOSAL`, `DECISION` | `QUESTION` | R139 |
| `SUPPORTS` | `FACT`, `FINDING`, `RESULT` | `PROPOSAL`, `DECISION`, `FINDING` | R139 |
| `CAUSED_BY` | `DECISION` | `FINDING` | R139 |
| `CAUSED_BY` | `ACTION` | `DECISION` | R139 |
| `CAUSED_BY` | `BLOCKER` | `FACT`, `FINDING` | R139 |
| `RESULT_OF` | `RESULT` | `ACTION` | R139 |
| `BLOCKS` | `BLOCKER` | `ACTION`, `DECISION`, `PROPOSAL` | R139 |
| `REFERS_TO` | любой | любой | R139 |

Любая другая пара отклоняется fail-closed. Мягкий вариант, где ограничены только
`ANSWERS` и `RESULT_OF`, отклонён (R139).

Цепочка ADR-0001 `FINDING → DECISION → ACTION → RESULT` (раздел «Discussion Graph»)
описывает связи графа и не образует отдельную FSM Discussion; из неё нельзя вывести
ни одну пару сверх приведённой. Для `BLOCKS` учтено, что Discussion не может снять
blocker (CM-10a, R136), а для `RESULT_OF` — что Discussion не принимает Result.

**3. Ссылочная целостность**

Все ссылки указывают на существующие объекты той же Task (R138). Ссылка на
несуществующий объект и ссылка в чужую Task отклоняются fail-closed; dangling links не
скрываются в проекции (R108, строка «Dangling/cross-Task references»). Отдельная FSM
Discussion не заводится, целостность проверяется на переходе (CM-11).

**4. Неизвестные типы и поля**

Неизвестный тип узла или связи отклоняется fail-closed. Неизвестное поле и лишнее поле
отклоняются fail-closed: набор полей узла и связи фиксирован, «свободные сообщения без
схемы типа не допускаются» (R138). Свободный текст допустим только внутри `Summary`,
а приведение к free-form вместо типизированных полей не допускается.

**5. Conflict Discussion Fact (R137)**

Conflict Discussion Fact — узел типа `FACT` (CM-08, R137) с обязательными
`ConflictID`, `ReviewResultRef` и `AuditResultRef` (R137). `ConflictID` ссылается на
Conflict Event, записанный в том же durable commit (CM-08b, R137); `ReviewResultRef` и
`AuditResultRef` указывают на исходные результаты этой Task.

**6. Отрицательные тесты и мутации**

Строки матрицы §9: по одной на группу правил (1)–(5) плюс существующие строки.

**Кандидатов не осталось: R138 и R139 полностью задают схему.**

## 6. Независимость Reviewer и Auditor

### Reviewer

TM-23 требует до назначения Reviewer сравнить его стабильный `agent_id` со всей
историей Executor Runs этой Task. Проверка включает текущих и прежних
исполнителей, несколько session адресов одного agent и recovery/rework. При
конфликте Coord fail-closed оставляет Task заблокированной на `REVIEW` с
`NO_INDEPENDENT_REVIEWER`; нельзя заменить Reviewer на Executor или разрешить
исключение Owner.

### Auditor

CM-03 и CM-07 требуют независимость AUDITOR по стабильному `agent_id` и истории
исполнителей, а не по model name, profile или session address. Canonical
проверка должна сравнить AgentID выбранного Auditor со всеми RoleSelection и
RoleHistory ролей Executor во всех Runs данного Task, включая завершённые и
предыдущие Attempts. Один и тот же AgentID с новой session остаётся тем же
субъектом и конфликтует; одинаковая модель у разных AgentID сама по себе не
является конфликтом.

Проверка выполняется до создания/передачи Reviewer или Auditor Run и до любых
ownership side effects. При отсутствии независимого агента переход закрывается
fail-closed, а причина должна быть видна как blocker; точный тип/код блокера
для Auditor отдельно не задан и рассматривается в §7. Политика
не может быть ослаблена Owner decision (CM-07).

## 7. Решения OWNER и зафиксированные правила

| Решение | Зафиксированное правило | Статус |
|---|---|---|---|
| Canonical schema | Дополнять `CanonicalSchemaVersion = 1` по образцу R85 до production writer. Versioned migration/ST-05 для этого изменения не применяется. | Решено R108 |
| Review/Audit result storage | Typed `ReviewResult`/`AuditResult` payload в DomainEvent; восстановление из Event history; отдельного хранилища нет. | Решено R108 |
| Verdict values | Review: `APPROVED`, `CHANGES_REQUIRED`. Audit: `PASS`, `CHANGES_REQUIRED`. `BLOCKED` — не verdict; невозможность вынести verdict фиксируется как Discussion `BLOCKER`. | Решено R108 |
| Finding schema | Coord-generated ID, Severity `CRITICAL`/`MAJOR`/`MINOR`, Summary, Evidence refs, CreatedAt. Severity не влияет на переходы; дополнительные поля — позже. | Решено R108 |
| Artifact references | Проверять существование и Task scope refs; проверку содержимого оставить отдельному design. | Решено R108: рекомендация принята |
| Result amendments | Append correction/superseding event; уже записанный результат не переписывать in-place. | Решено R108: рекомендация принята |
| Conflict criteria | Явный arbitration request или явное противоречие; различие verdict само по себе не эскалировать автоматически и не выбирать победителя. | Решено R108: рекомендация принята |
| Порядок conflict fact и Owner escalation | Один durable commit: Conflict Event → связанный Discussion Fact → OWNER Escalation Event → состояние ожидания решения → при необходимости Outbox. Conflict Event и Discussion Fact имеют меньший EventSeq, чем эскалация; все записи одной Task и одной новой ревизии; эскалация и Discussion Fact ссылаются на ConflictID, Discussion Fact — также на исходные Review/Audit results; новое состояние публикуется только после успешного commit, при ошибке commit не видна ни одна запись; повтор команды возвращает прежний receipt и не создаёт вторую эскалацию. Вариант с двумя commit отклонён: промежуточное состояние и отдельная recovery-машина не нужны. | Решено R137: принят вариант A |
| Discussion events and IDs | Отдельные typed events для node/edge; ID выдаёт Coord; только типизированные payload, без свободных сообщений. | Решено R108 |
| Payload fields per Discussion node/edge type | Общий минимум `ID`, `type`, actor/role из canonical `Run.RoleSelection`, `summary`, `evidence refs`, `CreatedAt`; плюс поля, специфичные для каждого node/edge type; либо единая фиксированная схема | Рекомендовать общий минимум и type-specific поля: сохраняет авторство и временной контекст, не сводя разные типы к free-form тексту | Решено R138 (вариант B) и R139 (поля оставшихся типов, матрица пар) |
| Discussion corrections, edge removal и duplicate command | Append-only compensating events, стабильные IDs и idempotency key; неизменяемые записи не переписывать. | Решено R108: рекомендация принята |
| Dangling/cross-Task references | Отвергать fail-closed; не скрывать dangling links в projection. | Решено R108: рекомендация принята |
| Blocker when no independent Auditor | Отдельный `NO_INDEPENDENT_AUDITOR`, fail-closed до назначения; без ослабления CM-03/CM-07. | Решено R108: рекомендация принята |

Открытых вопросов не осталось: порядок conflict fact и Owner escalation решён R137,
обязательные payload-поля Discussion node/edge — R138 и R139. Остальные строки
зафиксированы R108 и могут использоваться при реализации.

## 8. Schema и durable storage

Текущая CanonicalState имеет `CanonicalSchemaVersion = 1`; R108 выбирает
дополнение этой версии до production writer по образцу R85. Для этого изменения
не вводится versioned migration по ST-05. Startup validation fail-closed
отклоняет невозможные сочетания result/event/role/Task revision до readiness,
без частичной записи.

## 9. Целевые проверки и мутации

Имена ниже — targets для будущей реализации, тесты пока не существуют, если не
указано обратное. Все state-changing сценарии проверяют один durable commit,
сохранность состояния при отказе и корректное reopen/restart.

| Test target | Проверяемое правило | Мутация, которую тест обязан обнаружить |
|---|---|---|
| `TestReviewResultIsStructuredAndTaskScoped` | ReviewResult связан с canonical Task/Reviewer Run, Attempt и проверяемыми artifact refs; verdict/findings/summary не парсятся из свободного текста | принять текстовый статус без структурированного результата или принять ссылку на чужую Task |
| `TestAuditResultIsStructuredAndTaskScoped` | AuditResult связан с canonical Task/Auditor Run и полным набором требуемых полей | принять отсутствующий verdict или результат от не-Auditor Run |
| `TestReviewAndAuditVerdictSetsAreClosed` | Review/Audit принимают только verdict values R108; `BLOCKED` не verdict | принять `BLOCKED` или иной неутверждённый enum как verdict либо применить Severity вместо verdict |
| `TestFindingSchemaAndSeverityDoNotDriveTransitions` | Finding содержит Coord ID, допустимую Severity, Summary, Evidence refs и CreatedAt; переход определяется verdict | принять caller ID/неизвестную Severity либо изменить переход по Severity |
| `TestReviewResultCommitsWithTransitionEvent` | Result, Task transition, DomainEvent и нужные receipts/outbox фиксируются одной транзакцией | записать результат или событие отдельным вторым commit |
| `TestAuditResultCommitsWithTransitionEvent` | То же для AuditResult и перехода по StagePlan | пропустить DomainEvent либо опубликовать память до durable commit |
| `TestReviewVerdictWaitsForSupervisorStopProof` | Verdict durable, Task ждёт в `BLOCKED/WAITING_FOR_SUPERVISOR_STOP`, продолжения и нового writer нет до proof | создать следующий Run или увеличить Fence сразу после verdict |
| `TestAuditVerdictWaitsForSupervisorStopProof` | То же для Audit verdict, включая PASS | завершить Task или начать rework до stop proof |
| `TestReviewAuditConflictIsRecordedBeforeOwnerEscalation` | До `OWNER_DECISION` существует конфликт DomainEvent и связанный Discussion fact; Owner decision указывает conflict ID | сначала перевести Task в OWNER_DECISION, пропустить fact/event или отвязать Owner decision |
| `TestConflictEscalationCommitsAtomically` | Conflict Event, Discussion Fact и эскалация записываются одним durable commit одной ревизии; при ошибке commit нет ни одной из записей ни в памяти, ни на диске; повтор команды после успеха возвращает прежний receipt без второй эскалации | записать conflict/fact отдельным commit, опубликовать память до durable commit или создать вторую эскалацию при повторе |
| `TestDiscussionActorComesFromRunRoleSelection` | Внутренний agent-authored event получает ActorID/role из canonical Run.RoleSelection | принять подменённый ActorID или role из payload команды |
| `TestDiscussionEventsAreTypedAndCoordIDsAreStable` | Разрешённые node/edge types представлены отдельными typed events с Coord-generated IDs и восстанавливаются из Event history | принять неизвестный type, free-form message или caller-controlled node/edge ID |
| `TestDiscussionProjectionRebuildsFromEvents` | Повторное восстановление даёт те же node/edge IDs, данные и связи только из canonical Events | хранить изменяемую независимую копию Graph либо терять node/edge при rebuild |
| `TestDiscussionRejectsDanglingOrCrossTaskEdges` | Node/edge schema валидна, ссылки принадлежат ожидаемой Task и существуют | принять dangling ID или ссылку на другую Task |
| `TestDiscussionDoesNotPromoteTransportEvents` | ACK/heartbeat/delivery/retry и обычные реплики не создают пользовательский node/event | автоматически отображать transport envelope как FACT/RESULT |
| `TestDiscussionNodeTypesCarryRequiredFields` | Узел каждого типа из перечня R138 принимается с общим минимумом и полем по типу: `FINDING` — `ReviewResultRef`/`AuditResultRef`, `DECISION` — `OwnerDecisionRef`/`ConflictID`, `RESULT` — `ResultRef`, `BLOCKER` — `ReasonCode`; для `QUESTION`, `PROPOSAL`, `FACT`, `ACTION` достаточно общего минимума | принять узел без поля по типу, с пустым `Summary`/`Refs` или принять FACT или FINDING с пустым `Refs[]` |
| `TestDiscussionEdgeTypePairIsEnforced` | для каждого из шести типов связей каждая пара из матрицы §5.1 принимается, а каждая пара типов узлов вне матрицы отклоняется, даже если оба узла существуют, — табличная проверка по всем 8×8 парам; `REFERS_TO` принимает любую пару | разрешить пару вне матрицы, например `RESULT_OF: ACTION → RESULT`, или ограничить `REFERS_TO`; принять `ANSWERS` к существующему `FACT` либо связь с несуществующим `ToNodeID` |
| `TestDiscussionRejectsUnknownTypeOrExtraField` | Неизвестный тип узла или связи, неизвестное и лишнее поле отклоняются fail-closed; свободный текст без типизированных полей не принимается | принять `free-form` вместо типизированного поля или добавить поле сверх схемы |
| `TestDiscussionRejectsCrossTaskOrMissingReference` | Ссылка на несуществующий объект и ссылка в чужую Task отклоняются fail-closed; dangling link не скрывается в проекции | принять ссылку на чужую Task или на удалённый объект |
| `TestConflictDiscussionFactCarriesR137Refs` | Conflict Discussion Fact — узел типа `FACT` с `ConflictID`, `ReviewResultRef` и `AuditResultRef`; ссылки указывают на Conflict Event и исходные результаты той же Task, записанные в том же commit | принять Fact без `AuditResultRef` или со ссылкой на результат другой Task |
| `TestReviewerRejectsExecutorAgentIDAcrossHistory` | TM-23 держит alias session и прежнего Executor Attempt вне назначения Reviewer | сравнивать Session вместо AgentID или игнорировать прежний Run |
| `TestAuditorRejectsExecutorAgentIDAcrossTaskRuns` | CM-03 запрещает Auditor с AgentID любого Executor из истории canonical Task | убрать cross-Run сравнение, смотреть только активный Run или сравнивать model name |
| `TestIndependentAuditorMayUseSameModel` | Разные stable AgentID не конфликтуют только из-за совпавшей модели | ошибочно блокировать по Profile/model вместо identity |
| `TestNoIndependentReviewerOrAuditorFailsClosed` | Отсутствие независимого reviewer/auditor не создаёт Run и не передаёт Fence | выбрать Executor как fallback или выполнить ownership transfer до проверки |

Входной quarantine/redaction CM-09 получает отдельные тесты на приём текста,
history export и diagnostic log; его нельзя засчитать тестами выдачи результата
или Graph projection.

## 10. Вне этапа и зависимости

- Production Owner/Agent authentication, capability, command endpoint и
  production write path — P0-3; этот этап предоставляет внутренний атомарный
  Task command path для реализации и тестирования модели до подключения внешних
  writers. Он не разрешает обходить daemon.
- Полная Task FSM, production Stage scheduler, проверка Supervisor stop proof,
  продолжение workflow после proof, Task-wide входной Fence enforcement и writer
  process lifecycle не реализуются этим design-срезом; соблюдать TM-13…TM-23 и
  P0-3/P0-4/P0-5.
- Точные строковые имена EventType и детали wire/schema versioning этих typed
  событий определяются при реализации и не меняют зафиксированную R108 семантику.
- Project Dependency Graph, External References, API/Startup Card, Dashboard,
  Timeline и UX — отдельные этапы. Discussion Graph не заменяет Project Map.
- Импорт Rooms, перенос сообщений и автоматическое превращение участников или
  транспортных реплик в Runs, DomainEvents либо Discussion nodes запрещены.
- Содержимое artifacts и их content-addressed storage/migration остаются
  отдельным storage/operations design.

## 11. Критерий готовности design

Решения OWNER R108 зафиксировали schema path, result representation, verdict и
finding schema, а также typed Discussion events/IDs; R137 зафиксировал порядок
conflict/escalation одним durable commit (вариант A), R138 и R139 — типизированную схему
Discussion Graph (вариант B, §5.1). Открытых вопросов в §7 не осталось. Этап реализует
только внутреннюю
canonical Task command boundary: State + DomainEvent + Outbox/Inbox атомарны,
отказы не публикуют память, startup validation восстанавливает состояние, а
Discussion Graph детерминированно строится из Event history. Verdict сохраняется
с ожиданием stop proof; переход дальше остаётся закрытым до P0-5.

Имя Audit verdict `CHANGES_REQUIRED` добавлено по решению R108; формулировка
TASK-MODEL:297 «Audit требует исправления» остаётся без изменений.
