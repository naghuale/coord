# Review/Audit и Discussion Graph — design

- **Статус:** Draft
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
- Audit, требующий исправления, — сохранить durable result и перевести Task в
  `BLOCKED/WAITING_FOR_SUPERVISOR_STOP`; rework новым Executor Run и Attempt
  начинается только после stop proof.

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

Это перечень смысла полей, а не утверждённая Go/JSON-схема. Решения о точном
наборе полей сведены в таблицу вопросов OWNER в §7. До их принятия не вводить
дополнительные enum values, severity levels или lifecycle.

Результат должен быть связан с ролью и Attempt канонически назначенного Run:
ReviewResult создаётся Reviewer Run в Stage `REVIEW`, AuditResult — Auditor
Run в Stage `AUDIT`; Reviewer и Auditor Runs того же workflow pass используют
Attempt Executor этого pass (TM-17). Входные evidence/artifact references
проверяются по правилам, которые будут утверждены в §7; artifact content и
ownership contract остаются вне этого этапа.

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

Точный способ хранения результата относительно DomainEvent не задан Accepted
ADR: возможен typed result payload в событии с восстановлением результата из
журнала либо структурированная canonical запись, однозначно связанная с
событием. Нельзя одновременно делать две независимо изменяемые копии результата.
Рекомендация для OWNER: typed payload в DomainEvent и восстановление результата
из event history; это соответствует CM-11 и устраняет отдельную mutable truth.
Решение и startup validation зафиксировать в §7 до persisted schema implementation.

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

Конфликт и его Discussion fact должны стать durable до Owner escalation; каждое
изменение Task/Event сохраняет атомарность ST-01. При варианте с двумя commit,
первый фиксирует conflict Event и связанный FACT, второй — `OWNER_DECISION` со
ссылкой на conflict ID. Состояние между ними валидно: startup принимает
зафиксированный конфликт без escalation, а recovery находит его по ID и
идемпотентно применяет escalation; повтор не создаёт второе решение или
дубликат события. Это сохраняет буквальное «до» CM-08 и позволяет восстановиться
после сбоя. Один commit также может удовлетворить CM-08, если conflict/fact
события имеют меньший EventSeq, чем `OWNER_DECISION`, а ссылка проверяется в
той же транзакции. Обе формы сохраняют требуемый порядок; выбор между ними
остаётся OWNER без рекомендации.

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
точные обязательные поля и правила evidence определяются в §7.

Ни ACK, HEARTBEAT, delivery, transport retry, Outbox retry, ни произвольные
реплики не становятся Discussion node автоматически. Отдельной FSM Discussion
нет. Существующая Project Dependency Graph (ADR-0002) остаётся отдельной моделью
relations между проектными узлами; её рёбра нельзя использовать как Discussion
edges.

Параметры событий, ID, правки и восстановления перечислены в §7. Любой
утверждённый вариант обязан обеспечить детерминированное восстановление Graph
только из canonical Events.

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

## 7. Вопросы OWNER и рекомендации

| Вопрос | Варианты | Рекомендация и основание | Блокирует старт кода? |
|---|---|---|---|
| Persisted schema path | Добавить поля в schema v1 до production writer (как R85); либо versioned migration + backup (ADR-0005 ST-05) | R85-путь, если до P0-3 не появятся production записи: не нужна миграция несуществующей истории; иначе ST-05 | Да, persisted model/schema; независимые внутренние прототипы можно начать |
| Где хранить ReviewResult/AuditResult | Typed DomainEvent payload; либо canonical запись с обязательной ссылкой на событие | Typed payload и восстановление из Event history: CM-11 требует один источник истины и event-backed facts | Да, до реализации durable result |
| Минимальные verdict values | Текущие нормативные `APPROVED`, `CHANGES_REQUIRED`, `PASS` плюс отдельный исход Audit «нужны исправления»; расширить набор отдельно | Не добавлять значения без решения; назвать audit rework verdict явно, чтобы transition был однозначен и соответствовал TASK-MODEL | Да, до кодирования enum и переходов |
| Finding/evidence/artifact поля | Минимальный finding с summary и evidence refs; расширяемая схема с severity/classification; разные правила ссылок для input/output | Начать с минимального summary и проверяемых refs; severity не добавлять без нормы. Проверять существование и Task scope ссылок, содержимое Artifact оставить отдельному design | Да, для persisted finding и reference schema; проверку содержимого Artifact можно отложить |
| Можно ли менять уже записанный результат | In-place update; append correction/superseding event; запрет изменений | Append correction/superseding event: сохраняет историю решений и повторное восстановление; конкретный формат определить до amendment feature | Нет для первой неизменяемой записи; да для amendment |
| Что считается конфликтом Review/Audit | Несовпадение verdict; противоречащие findings; явный arbitration request; комбинация правил | Явный arbitration request/явное обнаруженное противоречие, не всякое различие verdict; не принимать автоматическое решение между ролями (CM-08) | Да, для автоматической conflict detection; можно начать фиксацию explicit conflict |
| Порядок conflict fact и Owner escalation | Один commit с меньшим EventSeq у conflict/fact; либо два commit с durable conflict/fact перед escalation | Обе формы удовлетворяют CM-08 при проверке ссылки и порядка. Для двух commit startup принимает conflict без escalation, а recovery идемпотентно завершает escalation по conflict ID. Выбор — OWNER, без предпочтения | Нет для модели; да для выбранного recovery/escalation поведения |
| Discussion event и ID representation | Отдельные typed events для node/edge; один универсальный event; ID генерирует caller или Coord | Отдельные typed events и ID от Coord: проще валидировать типы/автора и воспроизводить граф без второй копии (CM-11) | Да, для event schema/projection |
| Discussion corrections, edge removal и duplicate command | In-place mutation; compensating events; отказ от этих операций в MVP | Append-only compensating events, стабильные IDs и idempotency key; это сохраняет историю, но точную форму надо принять до реализации этих команд | Нет для create-only MVP; да для correction/removal semantics |
| Dangling/cross-Task references | Отклонять fail-closed; допускать временно и фильтровать в projection | Отклонять fail-closed: CM-11 требует валидировать references, а граф должен полностью восстанавливаться из Task events | Нет для result core; да для graph commands/projection |
| Код blocker при отсутствии Auditor | Вывести из общего blocker; отдельный код `NO_INDEPENDENT_AUDITOR`; не блокировать Task | Отдельная причина по аналогии с `NO_INDEPENDENT_REVIEWER`, сохраняя fail-closed и видимость причины (CM-03/07) | Нет для запрета назначения; да для стабильного API/UX-кода |

Решения с отметкой «да» должны быть закрыты до реализации соответствующего
persisted contract. Они не препятствуют внутренней подготовительной работе,
которая не фиксирует ещё не выбранную схему.

## 8. Schema и durable storage

Текущая CanonicalState имеет `CanonicalSchemaVersion = 1`, `task.DomainEvent`
пока не имеет typed result/node/edge payload. Добавление полей или коллекций
меняет persisted contract; вопросы и рекомендуемый выбор собраны в §7.

При любом schema path startup validation должна fail-closed отклонять
невозможные сочетания result/event/role/Task revision до readiness, без частичной
записи. Если выбран versioned migration, старые snapshot должны быть корректно
мигрированы с backup по ST-05 либо отклонены без изменения файла.

## 9. Целевые проверки и мутации

Имена ниже — targets для будущей реализации, тесты пока не существуют, если не
указано обратное. Все state-changing сценарии проверяют один durable commit,
сохранность состояния при отказе и корректное reopen/restart.

| Test target | Проверяемое правило | Мутация, которую тест обязан обнаружить |
|---|---|---|
| `TestReviewResultIsStructuredAndTaskScoped` | ReviewResult связан с canonical Task/Reviewer Run, Attempt и проверяемыми artifact refs; verdict/findings/summary не парсятся из свободного текста | принять текстовый статус без структурированного результата или принять ссылку на чужую Task |
| `TestAuditResultIsStructuredAndTaskScoped` | AuditResult связан с canonical Task/Auditor Run и полным набором требуемых полей | принять отсутствующий verdict или результат от не-Auditor Run |
| `TestReviewResultCommitsWithTransitionEvent` | Result, Task transition, DomainEvent и нужные receipts/outbox фиксируются одной транзакцией | записать результат или событие отдельным вторым commit |
| `TestAuditResultCommitsWithTransitionEvent` | То же для AuditResult и перехода по StagePlan | пропустить DomainEvent либо опубликовать память до durable commit |
| `TestReviewVerdictWaitsForSupervisorStopProof` | Verdict durable, Task ждёт в `BLOCKED/WAITING_FOR_SUPERVISOR_STOP`, продолжения и нового writer нет до proof | создать следующий Run или увеличить Fence сразу после verdict |
| `TestAuditVerdictWaitsForSupervisorStopProof` | То же для Audit verdict, включая PASS | завершить Task или начать rework до stop proof |
| `TestReviewAuditConflictIsRecordedBeforeOwnerEscalation` | До `OWNER_DECISION` существует конфликт DomainEvent и связанный Discussion fact; Owner decision указывает conflict ID | сначала перевести Task в OWNER_DECISION, пропустить fact/event или отвязать Owner decision |
| `TestConflictRecoveryEscalatesOnceAfterRestart` | При двух-commit варианте startup принимает durable conflict без escalation; recovery после restart применяет её один раз по conflict ID | отклонить допустимое промежуточное состояние, не возобновить escalation или создать дубликат Owner decision/event |
| `TestDiscussionActorComesFromRunRoleSelection` | Внутренний agent-authored event получает ActorID/role из canonical Run.RoleSelection | принять подменённый ActorID или role из payload команды |
| `TestDiscussionProjectionRebuildsFromEvents` | Повторное восстановление даёт те же node/edge IDs, данные и связи только из canonical Events | хранить изменяемую независимую копию Graph либо терять node/edge при rebuild |
| `TestDiscussionRejectsDanglingOrCrossTaskEdges` | Node/edge schema валидна, ссылки принадлежат ожидаемой Task и существуют | принять dangling ID или ссылку на другую Task |
| `TestDiscussionDoesNotPromoteTransportEvents` | ACK/heartbeat/delivery/retry и обычные реплики не создают пользовательский node/event | автоматически отображать transport envelope как FACT/RESULT |
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
- EventType registry, окончательная verdict taxonomy, finding/evidence schema,
  result amendment policy и conflict-detection rule требуют решений из разделов
  выше.
- Project Dependency Graph, External References, API/Startup Card, Dashboard,
  Timeline и UX — отдельные этапы. Discussion Graph не заменяет Project Map.
- Импорт Rooms, перенос сообщений и автоматическое превращение участников или
  транспортных реплик в Runs, DomainEvents либо Discussion nodes запрещены.
- Содержимое artifacts и их content-addressed storage/migration остаются
  отдельным storage/operations design.

## 11. Критерий готовности design

Код этапа может начаться после решений OWNER, помеченных в §7 как блокирующие
persisted contract: schema path, result representation, минимальные verdict и
finding поля, а также representation Discussion events/IDs. Остальные решения
могут быть закрыты до соответствующих функций. Этап реализует только внутреннюю
canonical Task command boundary: State + DomainEvent + Outbox/Inbox атомарны,
отказы не публикуют память, startup validation восстанавливает состояние, а
Discussion Graph детерминированно строится из Event history. Verdict сохраняется
с ожиданием stop proof; переход дальше остаётся закрытым до P0-5.
