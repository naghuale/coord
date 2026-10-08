# ADR-0001: Canonical Coordination Model

- **Статус:** Accepted
- **Дата:** 2026-10-07
- **Редакционная пометка:** 2026-10-08: добавлены идентификаторы правил, редакционно, без изменения смысла
- **Область:** целевая архитектура Coord и миграция к orchestration platform
- **Связанные решения:** ADR-0002 Project Dependency Graph; ADR-0003 External Reference Model; ADR-0004 Runtime and Daemon Model

## Контекст

Coord уже содержит Run FSM, Attempt, fencing, transport adapters, durable
outbox/checkpoints и recovery. Появились требования к Task lifecycle, нескольким
ролям на задачу, Review/Audit, истории работы, структурированному обсуждению и
Dashboard.

Текущая реализация ещё не соответствует этой целевой модели. `store` сохраняет
Runs в snapshot, а `internal/rooms` отдельно хранит комнаты, участников,
регистрации, историю общения и собственный outbox. Production coordinator daemon,
каноническая Task-модель, единый журнал доменных событий и Dashboard API ещё
предстоит реализовать. Текущий `coord tui` остаётся локальным интерфейсом rooms.

Главный риск — появление второй модели состояния, которая будет расходиться с
фактическим исполнением Coord.

## Решение

- **CM-01 — Coord — единственный владелец.** Coord является единственным владельцем канонического состояния задач и их
  исполнения. Dashboard и другие читатели получают read-only проекции Coord.
  Telepathy остаётся транспортом. Жизненным циклом процессов и подтверждением
  остановки writer управляет внешний supervisor.
  Проверка: не реализовано

Целевые канонические сущности:

- `Task`
- `Run`
- `Session`
- `AgentSession` (R114)
- `OwnerCredential` (R110)
- `Review`
- `Audit`
- `DiscussionNode`
- `DiscussionEdge`
- `DomainEvent`
- `Artifact`

- **CM-02 — Dashboard без FSM и БД.** Хранилище и runtime принадлежат production daemon Coord. Dashboard не получает
  собственную БД, FSM или workflow-состояние.
  Проверка: не реализовано

## Модель и инварианты

### Task и идентичность

`TaskID` — отдельная стабильная идентичность Task. Его нельзя автоматически
выводить из исторического поля `Run.Task`: значение этого поля может быть
описанием, названием, внешним идентификатором или одноразовым заданием.

Точная схема Task identity и связь Task ↔ Run оформляются отдельным design
документом до миграции данных. Task владеет metadata, Stage Plan и workflow
state. Его состояние изменяется через переходы Coord, а не выводится Dashboard
из последнего Run.

Task хранит две независимые оси: `CurrentStage` и lifecycle `Status`.
Workflow stages: `EXECUTION`, `REVIEW`, `AUDIT`, `OWNER_DECISION`; Stage Plan
задаёт обязательные и необязательные этапы. Task statuses: `PENDING`,
`IN_PROGRESS`, `BLOCKED`, `FAILED`, `COMPLETED`. Терминальное завершение —
`Status=COMPLETED` без текущего Stage.

`BLOCKED` сохраняет причину и точку продолжения, не стирая текущий Stage.
`NEEDS_OWNER_DECISION` — это `CurrentStage=OWNER_DECISION` и `Status=BLOCKED`,
а не отдельное значение Status. `REWORK_REQUIRED` — исход Review/Audit, а не
сохраняемый Stage/Status: `CHANGES_REQUIRED` или finding, требующий исправления,
возвращает Stage к `EXECUTION`, начинает новый Attempt и создаёт новый Executor
Run. `ReviewResult.APPROVED` переводит workflow в AUDIT либо завершает Task,
если Audit не предусмотрен Stage Plan. Успешный Audit завершает Task
автоматически, если human decision не требуется по policy. Owner `APPROVED`
продолжает разрешённый policy путь; `REJECTED` может вернуть задачу на новый
Executor Attempt. Coord валидирует переходы и фиксирует их с DomainEvent и
необходимым Outbox. Детальная связь TaskID, Runs, Stage и Status дана в
`docs/TASK-MODEL.md`.

### Run, Attempt и Fence

`Task.Fence` — единственное авторитетное, монотонно возрастающее значение
эпохи владения. `Run.Fence` хранит только снимок Task.Fence, выданный этому Run;
он не является источником истины. Каждая входящая запись сверяет fence Run с
текущим `Task.Fence`. Новый writer, rework и recovery повышают `Task.Fence`; у
нового Run сохраняется новый снимок. Recovery claim и его CAS выполняются по
Task.Fence, чтобы конкурирующие recovery на разных Runs не могли пройти
независимые проверки.

На Task допускается не более одного активного writer Run. Это требование
обеспечивается в нескольких точках: Assign/rework атомарно отклоняет новый
активный Run при уже существующем; Tick и recovery проверяют тот же инвариант и
при нарушении закрывают запись fail-closed, не выбирая победителя неявно.
Перед созданием Run для rework предыдущий Run явно переводится в терминальный
`SUPERSEDED`, его право записи отзывается повышением Task.Fence, а новый Run,
событие и Outbox фиксируются в той же durable transaction. Параллельные writer
потребуют отдельно спроектированных независимых fencing scopes.

Авторитетная протокольная FSM текущего Run содержит `PENDING`, `ACCEPTED`, `STARTED`, `BLOCKED`, `STALLED`, `RESULT_READY`,
`DONE`, `FAILED`; целевая FSM добавляет терминальный `SUPERSEDED`. Переходы
обеспечивают delivery, recovery и commit инварианты.

Для Dashboard/API можно вычислять понятный операционный статус
`ASSIGNED`, `READY`, `WORKING`, `BLOCKED`, `FAILED`, `COMPLETED`. Это проекция,
а не вторая FSM. Она обязана сохранять различие между просроченным прогрессом
(`STALLED`), локально подготовленным результатом (`RESULT_READY`) и результатом,
durable committed в Coord (`DONE`), например отдельными health/result-receipt
полями и пояснением. `COMPLETED` не показывается до терминального Run и
committed Coord ResultRecord. Исходящий `RESULT_ACK` сообщает агенту о принятии
результата, но его доставка не является условием принятия и не доказывает
остановку процесса; это разные факты, связанные в `docs/TASK-MODEL.md`.

- Повторная доставка не создаёт новый Attempt.
- Retry сохраняет Attempt. Recovery сохраняет Attempt и повышает Task Fence.
- Передача владения после recovery требует подтверждения остановки старого writer
  внешним supervisor; сам Fence не останавливает процесс или запись во внешние
  ресурсы.
- Повторная доставка и recovery продолжают тот же Run/Attempt; recovery повышает
  Task Fence после подтверждённой остановки старого writer; claim/CAS проверяет
  именно Task.Fence, а Run получает новый снимок.
- Rework начинает отдельный Run с новой Attempt и повышенной Task Fence. Это
  новая business attempt, а не transport retry или recovery. Предыдущий Run
  становится `SUPERSEDED`. Правило нумерации Attempt между Runs фиксируется в
  Task/workflow design.
- Reviewer не проверяет собственный результат. Coord проверяет конфликт по
  стабильному `agent_id` и всей истории Executor Runs задачи; без независимого
  Reviewer переход закрыт fail-closed. Проверка AUDITOR задана CM-03. Текущая
  реализация проверяет независимость Reviewer и Auditor по истории Executor Runs
  (`store/canonical_independence.go`, RA-2, commit `2d0cf60`). Это не вводит
  симметричного запрета на назначение бывшего Reviewer или Auditor исполнителем:
  такое ограничение не установлено и остаётся открытым до отдельного решения
  OWNER.
- **CM-03 — Auditor независим по agent_id и истории.** Независимость AUDITOR проверяется по стабильному `agent_id` и истории
  исполнителей, а не по имени модели или адресу сессии.
  Проверка: `TestRolesIndependenceAndImmutableAssignment`

### Ролевая модель

Целевая модель различает человека, системную координацию и работу агентов:

- `OWNER` — человек, задающий цель, приоритеты и acceptance policy. У него нет
  агентского Run или Session. Может создать и закрыть Task, менять направление
  и принимать редкие human-only решения; не исполняет Run, Review или Audit и
  не утверждает каждый Task вручную.
- `ORCHESTRATOR` — функция Coord, которая планирует этапы, назначает Runs и
  контролирует переходы workflow, blockers и производные Attention. Она не
  является executor process.
- `EXECUTOR` — изменяет только разрешённую границу Task, производит artifacts и
  передаёт проверяемый результат; не принимает и не проверяет собственную работу.
- `REVIEWER` — проверяет результат относительно Task Contract и фиксирует
  `ReviewResult`; не изменяет результат исполнителя и не принимает решение Owner.
- `AUDITOR` — независимо проверяет процесс, границы, контракт и доказательства,
  фиксируя `AuditResult`; не изменяет результат Executor или Review.

Stage Plan определяет требуемые роли и порядок этапов. Фактические назначения и
их история фиксируются Coord. В текущем коде есть `COORDINATOR` profile,
назначаемый агентской сессии. Существующие назначения этой роли требуют
явного compatibility решения до миграции.

В целевой модели `ORCHESTRATOR` — только функция Coord; агентская роль
`COORDINATOR` выводится из целевого набора ролей. Перед миграцией должна быть
определена совместимость существующих назначений этой роли.

- **CM-04 — DONE назначения требует committed result.** Состояние назначения роли (`WAITING`, `ACTIVE`, `DONE`) — проекция Stage Plan,
  Task state, Runs и их результатов. `DONE` требует терминального Run и
  подтверждённого Coord результата, не только локального сообщения о завершении.
  Проверка: не реализовано

- **CM-05 — Назначение роли — проекция, отдельной FSM нет.** Отдельная FSM назначения не хранится.
  Проверка: не реализовано

OWNER принимает эскалации через
авторизованный интерфейс Coord; Dashboard v1 остаётся read-only и не отправляет
такие команды.

OWNER подключается, когда нужна человеческая способность: предоставить секрет
или MFA, оценить визуальный/субъективный результат, выбрать между допустимыми
бизнес-вариантами, явно изменить полномочия либо разрешить неразрешимый policy
конфликт Review/Audit. Для обычной задачи Coord стремится пройти Stage Plan до
автоматического завершения.

- **CM-06 — Эскалация фиксирует вопрос и варианты.** Эскалация `NEEDS_OWNER_DECISION` содержит причину, основание, точный вопрос,
  разрешённые варианты и связанные evidence. Пока ответ не записан как
  `OWNER_DECISION`, workflow ждёт. Решение Owner не отменяет hard policy. Изменение
  полномочий требует отдельного авторизованного изменения scope/capability и его
  аудита. Секреты и OTP передаются только по отдельному защищённому каналу и не
  попадают в Task, Startup Card, Discussion Graph, DomainEvent или логи; в журнале
  фиксируется только факт и результат защищённого handoff без значения секрета.
  Проверка: не реализовано

- Запись Owner decision авторизуется Task-scoped capability и не требует
  агентского Run. Это первый тип полномочия, который действует непосредственно на
  Task. Coord проверяет его область, разрешённый вариант решения, актуальную
  версию Task и hard policy, затем атомарно записывает decision event и переход.
  Dashboard остаётся read-only; решение подаётся через отдельный авторизованный
  интерфейс Coord. (норма: TASK-MODEL TM-24)

- **CM-07 — Hard policy не обходится решением Owner.** Hard policy нельзя отменить решением Owner. К ней относятся: независимость
  AUDITOR по стабильному `agent_id` и истории исполнителей; повышение Task Fence и
  подтверждение остановки старого writer до передачи владения; отсутствие
  эквивалентности между доставкой и принятием; запрет записи от устаревшего
  Session generation или Task Fence; ограничения роли и capability назначения.
  Изменение scope требует аутентифицированного действия Owner с явной capability,
  проверяемой Coord по той же модели разрешений, что и назначение роли. Текущий
  shortcut rooms, где пустой `as` автоматически трактуется как owner, не является
  целевой авторизацией и не переносится в новую модель.
  Проверка: не реализовано

- **CM-08 — Конфликт Review/Audit — DomainEvent.** Конфликт Review/Audit, требующий арбитража, записывается как явный DomainEvent
  и связанный Discussion fact до перехода в `NEEDS_OWNER_DECISION`. Ответ Owner
  ссылается на этот конфликт и также становится событием; история не должна
  сводить арбитраж к одному текущему статусу.
  Проверка: не реализовано

- **CM-09 — Секреты блокируются на входе.** Запрет на секреты обеспечивается на входе, а не только при выдаче Startup Card.
  Секретные значения передаются по защищённому каналу напрямую получателю и не
  проходят через Coord; в Coord сохраняются только ссылка на handoff и его
  результат. Приём свободного текста для событий и Discussion применяет политику
  quarantine/redaction до durable записи. History export и диагностические логи
  проверяются отдельно: экспорт не сохраняет необработанный текст, а диагностика
  не содержит секреты. Текущая очистка runtime routes и connection identifiers
  в `rooms/history.go` служит прецедентом санитарной проекции, но не заменяет
  проверку текста.
  Проверка: не реализовано

### Session и transport presence

`Session` в Coord представляет прикладную сессию агента и её связь с Runs.
Транспортная сессия Telepathy — отдельное понятие. Transport heartbeat,
доступность peer или успешная доставка сами по себе не подтверждают готовность
агента или принятие задачи. Связь `CurrentRun` является производной от канонического
владения Run, если отдельное поле не требуется для целостности.
- **CM-10 — Устаревшее generation не пишет.** Переподключение имеет generation; устаревшее поколение не может писать в Coord. API и проекции
  показывают generation, freshness, transport attachment/reachability и readiness
  раздельно, когда эти данные доступны. Их нельзя сводить к одному признаку
  «online» или выводить readiness только из transport heartbeat.
  Проверка: частично — `TestSessionRecoveryFencesAndScopesContext`, `TestAvailabilitySeparatesStaleGeneration`; не реализовано: generation канонических Session Coord (проверены только legacy rooms)

### Review, Audit и Artifact

Review и Audit сохраняются как структурированные результаты с verdict, findings,
summary и ссылками на входные/выходные artifacts. Состояние не восстанавливается
разбором произвольного текста. Проверка результата и независимость AUDITOR
остаются в Coord policy.

Artifact имеет канонические метаданные и проверяемую идентичность. Место хранения
самого содержимого и его миграция уточняются отдельным storage/operations
решением.

### Discussion Graph

Discussion Graph фиксирует значимые факты и связи для аудита решений; он не
является чатом или потоком транспортных сообщений.

Типы узлов:

`QUESTION`, `PROPOSAL`, `FACT`, `FINDING`, `DECISION`, `ACTION`, `RESULT`,
`BLOCKER`.

Типы связей:

`ANSWERS`, `SUPPORTS`, `CAUSED_BY`, `RESULT_OF`, `BLOCKS`, `REFERS_TO`.

- **CM-11 — Discussion Graph не вторая истина.** Узлы и связи принадлежат Task, проходят проверку схемы и ссылочной целостности.
  Их авторитетная запись представляется типизированным доменным событием с
  устойчивым node/edge ID и payload. Graph nodes/edges могут материализоваться для
  запросов, но такая проекция должна восстанавливаться из канонической записи и не
  становиться второй изменяемой истиной. Пользовательская Timeline также является
  проекцией.
  Проверка: не реализовано

- **CM-12 — Owner identity подтверждается OwnerCredential (R110).** `OwnerCredential`
  — каноническая сущность Coord с собственным потоком событий. Каждая Owner-команда
  связывается с credential, проверенной Coord; trusted identity извлекается из
  проверенной credential и не принимается из тела команды. Команда проверяется
  по challenge, подписанному активной credential соответствующего principal.
  Task-scoped capability в этой модели — подписанный challenge для одной команды
  одной Task (`docs/plans/p0-2-task/COMMAND-BOUNDARY.md`, §3.2; R110).
  Проверка: не реализовано

- **CM-13 — AgentSession связывает доверенную identity и транспортную сессию (R114).**
  Оператор регистрирует канонический `AgentSession`, связывающий стабильный
  `AgentID`, адрес сессии, поколение и публичный ключ или его отпечаток. При
  назначении поколение записывается в Run; входящая команда сверяет сессию и
  поколение Run с актуальным `AgentSession` и сверяет `AgentSession.AgentID` с
  `Run.RoleSelection.AgentID`. Автор берётся из сохранённого назначения после
  проверки этой привязки, а не из тела команды.
  Смена адреса сессии означает отзыв прежней `AgentSession` и регистрацию новой
  привязки; поколение меняется только при ротации ключа в пределах той же
  привязки, поэтому смена адреса под ротацию не маскируется (R122). Прежняя
  привязка отзывается отдельным событием и не авторизует ни одной команды, и
  команда, пришедшая через неё, отклоняется до replay. История хранит адрес, ключ,
  время действия и причину отзыва.
  Старый canonical state, в котором активный command-capable Run не имеет
  действующей `AgentSession`, в штатной работе не допускается: такое состояние
  блокирует readiness fail-closed с понятной диагностической причиной;
  автоматического backfill и новой recovery-процедуры сейчас нет, файл
  автоматически не переписывается, а `AgentID`, поколение, ключ и адрес не
  заполняются задним числом (R123). До подключения канонического agent command
  path такие состояния не должны создаваться штатным путём; Owner recovery
  command (stop proof, завершение старого Run, новое назначение из свежей
  `AgentSession`) остаётся запланированным продолжением P0-3 и требует
  отдельного проекта и решения.
  Ротация ключа или отзыв `AgentSession` при активном Run разрешены. Такой Run
  огорожен: его команды отклоняются до replay, readiness не блокируется, а
  переназначение идёт только через recovery transfer на текущую активную
  привязку (R125). R125 относится к Run, у которого привязка и поколение были
  записаны при назначении, а затем устарели или привязка была отозвана; R123 — к
  Run без записанной привязки. Отклонение команд до replay относится к части 4 и
  пока не реализовано.
  Отозванный адрес `AgentSession` не регистрируется повторно никогда; вернувшийся
  агент регистрируется на новом адресе с прежним `AgentID`. Повторная регистрация с
  поколения 1 совпала бы с огороженным Run — тем же адресом и поколением — и сняла
  бы ограждение R125, то есть вернула бы ABA (R126).
  Проверка: не реализовано

- **CM-14 — AgentID обязателен для агентского Run (R116).** Любой Task-scoped Run,
  принимающий агентские команды, включая Executor Run, имеет непустой устойчивый `AgentID`.
  Агентская команда не может сама объявить свой `AgentID`; Coord получает его из
  проверенного `AgentSession` и назначения Run.
  Проверка: не реализовано

- **CM-15 — Проверки агентской команды упорядочены (R121).** До replay Coord
  аутентифицирует сессию, сверяет её с актуальными `AgentSession` и generation,
  получает trusted `AgentID`, проверяет актуальность Run и `Task.Fence`, затем
  проверяет соответствие роли и Stage команде. После этого выполняется replay и
  дедупликация. Только для новой команды Coord проверяет допустимость перехода и
  CAS по `Task.Revision`, затем фиксирует изменение durable. R121 не задаёт
  внутренний порядок проверки перехода и CAS.
  Первые два шага детализированы: transport проверяет подпись и формирует
  `AuthenticatedFrame`, затем Coord ищет `AgentSession` по зарегистрированной
  привязке и отклоняет команду, если привязка отозвана, адрес, отпечаток ключа или
  поколение не совпадают; только после этого команда получает trusted `AgentID` и
  продолжает проверки Run и `Task.Fence`. Это детализация CM-15, а не новый
  порядок (R122).
  Проверка: не реализовано

Цепочки `FINDING → DECISION → ACTION → RESULT` описывают связи графа и не
образуют отдельную FSM Discussion.

Обычный временный обмен репликами остаётся в транспортном канале и не становится
автоматически Discussion node или DomainEvent. В канонический Graph попадают
типизированные факты, предложения, решения, действия, результаты и блокеры.

### Domain Events и транзакция

Coord хранит журнал доменных событий. События описывают работу и решения, а не
транспортный протокол. Изменение состояния, DomainEvent и соответствующая запись
Outbox фиксируются одной durable transaction:

```text
State Change + Domain Event + Outbox [+ Inbox receipt when required] = Single Durable Transaction
```

Нельзя подтвердить внешнюю доставку до durable commit. Доставка TASK не означает
TaskAccepted; отправка RESULT_ACK не должна предшествовать фиксации принятого
результата Coord. Retry outbox не меняет Attempt.

Каноническим источником восстановления является материализованное состояние
Coord в durable snapshot. Event Log — append-only журнал аудита и причинности,
а не самостоятельный источник replay/recovery. Выбор авторитетности сделан в
ADR-0005 ST-01, включая требование fail-closed при рассогласовании state и
журнала. При загрузке Coord проверяет согласованность состояния, событий и
Outbox; если проверка не проходит, readiness закрыта. Автоматически выбирать
между расходящимися копиями или восстанавливать состояние из неполного журнала
запрещено.

Входящий durable Inbox receipt, если он нужен для дедупликации команды или
сообщения, фиксируется в той же transaction boundary. Он не заменяет проверку
идентичности, Fence и допустимости перехода.

Доменные события включают факты назначения и принятия Task, начала/прогресса/
блокировки/завершения работы, результаты Review/Audit, решения владельца и
создание/изменение Discussion nodes и edges. Сырые ACK, HEARTBEAT, delivery,
transport retry и outbox retry не показываются как пользовательская история.
Операционная диагностика может храниться отдельно от доменного журнала.

### Производные представления

Следующее не является самостоятельным каноническим состоянием:

- Attention и Attention Queue;
- Health;
- Room;
- Dashboard State;
- Graph View;
- Timeline View;
- Errors;
- Recent Decisions;
- Sessions status, который можно однозначно вывести из канонических фактов.

Attention строится из lifecycle Task, Runs, Review/Audit results и ожидаемых
действий. Health строится из статуса Run, принятого heartbeat, deadlines и
последнего прогресса. Dashboard не сохраняет эти статусы отдельно и не принимает
workflow-решений. Attention item не имеет отдельного lifecycle; его ключ может
детерминированно вычисляться из исходного факта для дедупликации отображения.
Errors выводятся из канонических failed/blocked фактов и эксплуатационных
сигналов; Recent Decisions строятся из Discussion DECISION facts.

### Agent Startup Card

Для начала или продолжения назначенного Run Coord формирует Startup Card из
одного согласованного снимка канонического Task, Stage, Run, Session и связанных
artifacts/events. Карточка должна сообщать:

- кто агент, его роль, session и актуальное generation;
- Task, текущий Stage, Run, Attempt и Fence;
- цель назначения, входные ссылки и ожидаемый структурированный результат;
- разрешённые и запрещённые операции, критерии завершения;
- активные blockers, последнюю значимую информацию и следующего участника;
- revision/freshness карточки, чтобы обнаружить устаревшее назначение.

Карточка заменяет необходимость читать транспортный чат или переписку для
восстановления контекста. Она не заменяет указанные Task Contract и artifacts:
агент обязан открыть их как входы работы. Карточка не является источником
полномочий: все чтение/запись и допустимые типы Discussion node проверяются
Coord по session capability, роли, Task, generation и текущему Fence. Токены и
секреты в карточку не включаются. Если реализация выдаёт capability token,
он привязывается к Session, generation, Run, роли и Fence; Startup Card сама
по себе токеном или полномочием не является. Resume/recovery отзывает старую
привязку и требует новой проверки актуального поколения и Fence.

Discussion facts содержат автора и роль, присвоенные Coord из аутентифицированной
сессии в момент записи, summary и ссылки на evidence. Исторические author/role
не меняются при замене участника. Автор и role не принимаются как самодекларация
агента. Требуемые поля и allowed node types задаются схемой по типу узла и
полномочиям; evidence ссылается на artifact или проверяемый источник.

Для Owner формируется отдельная decision card только при `NEEDS_OWNER_DECISION`.
Она сообщает причину, evidence, вопрос и разрешённые варианты. Для остальных
задач Owner не получает обязательную карточку подтверждения.

## Архитектурные границы

### Production Daemon

Production daemon — единственный владелец runtime state и процесса Coord. Он
обеспечивает single-owner lock, загрузку и проверку состояния, durable writes,
восстановление outbox и координацию API/CLI/transport. Внешний supervisor остаётся
владельцем запуска и остановки executor processes и подтверждения fencing.

Контракт daemon и storage boundary должны быть спроектированы до API. API и
Dashboard не должны открывать второй независимый writable runtime или загружать
собственную копию состояния, расходящуюся с daemon.

Проверяемая readiness daemon требует, чтобы:

1. Экземпляр удерживал exclusive lock canonical state; второй экземпляр с тем же
   state отказывался запускаться.
2. Загруженное состояние прошло валидацию до перехода в ready.
3. Durable outbox был восстановлен до обслуживания API и новых назначений.
4. После неопределённой ошибки durable записи память не публиковалась, записи
   прекращались, а продолжение требовало reopen.
5. Канонический write path принимал агентские команды и результаты через проверку
   подписи, Task/Run/Attempt/Fence, session generation и разрешённого перехода.

Read-only API публикуется после прохождения этих критериев.

### Storage

Выбор между file snapshots, SQLite и PostgreSQL не входит в этот ADR. Сначала
фиксируется backend-neutral transaction boundary и проверяется семантика текущего
`store.Persistence`; затем отдельный storage ADR выбирает backend до реализации
нового canonical aggregate store. Этот ADR также определит миграции схемы,
locking, восстановление, импорт существующего state и политику backup.

Хранилище реализуется за persistence boundary Coord. Его интерфейс может
эволюционировать от текущего `store.Persistence`, но backend обязан сохранять
семантику: эксклюзивное владение состоянием, публикация памяти только после
durable commit и fail-closed/reopen после неопределённой ошибки записи. SQL не
является обязательным выбором этого ADR.

Dashboard не получает отдельную БД. SQL-хранилище, если оно будет выбрано,
принадлежит Coord.

### Telepathy и агенты

Telepathy отвечает за доставку, маршрутизацию и transport-level session
наблюдения. Coord владеет Task, Run, Session, Event, Discussion, Review, Audit,
FSM и workflow. Ни одно решение о состоянии задачи не существует только внутри
Telepathy или прямого обмена Executor ↔ Reviewer.

Канонический путь:

```text
Executor → Coord → Reviewer → Coord → Auditor
```

Реальные API transport adapter должны проверяться по их существующему контракту.
Transport delivery не считается подтверждением приёма задания или результата.

Существующие transport adapters и standalone Telepathy host уже реализованы.
Целевой этап Telepathy — проверка и подключение к новому Task/Run workflow, а не
переписывание транспорта. До Read-only API должен существовать канонический
write path для команд и результатов агента: Coord проверяет подпись и полную
идентичность Task/Run/Attempt/Fence, применяет разрешённый переход FSM и
атомарно сохраняет факт. Иначе API будет показывать неполные или пустые данные.

## Read-only API и Dashboard

Dashboard получает данные через API Coord и остаётся read-only. MVP API
развивается в следующем порядке:

1. `GET /api/tasks`
2. `GET /api/tasks/{id}`
3. `GET /api/tasks/{id}/runs`
4. `GET /api/tasks/{id}/events`
5. `GET /api/sessions`
6. `GET /api/tasks/{id}/reviews`
7. `GET /api/tasks/{id}/audits`
8. `GET /api/tasks/{id}/graph`
9. `GET /api/dashboard` — одна агрегированная проекция для главного экрана.
10. Контекст сессии включает Startup Card для текущего назначения; отдельный
    endpoint выбирается в API design.

Параллельные endpoint-ы списка задач с разным смыслом не вводятся. Projection
поля в ответах — вычисляемые данные Coord, а не новая модель. Транспортные
frames и протокольные ACK не входят в пользовательские ответы.

Dashboard показывает Attention, Errors, Tasks, Recent Decisions и Sessions.
Экран Task показывает summary, Runs, Review/Audit, Timeline, Discussion Graph и
Attention. Экран Graph раскрывает цепочки фактов, findings, proposals, decisions,
actions и results. Сам Dashboard не запускает задачи, не изменяет FSM и не
управляет агентами.

Project Map и зависимости между Tasks/ADR/Milestones определяются отдельно в
ADR-0002. `GET /api/roadmap` и `/api/tasks/{id}/dependencies` не являются частью
Conversation Graph API и добавляются в API по отдельному плану реализации.

Перед публикацией API отдельное решение должно определить адрес bind, доступ и
аутентификацию для данных задач и обсуждений. Read-only не означает публичный
доступ.

## Миграция от `internal/rooms`

`internal/rooms` и snapshot `<state>/rooms.json` объявляются legacy. Отдельный
каталог `state/rooms` этим решением не предполагается. Допустимы bugfix,
stability и необходимая compatibility работа. Новые workflow-функции,
состояния и сущности в rooms не добавляются.

Миграция идёт от канонической Task-модели к compatibility projection для старых
потребителей. Legacy room данные не становятся Task/Runs автоматически:

- participant не превращается в Run без явного соответствия;
- room message не превращается в DomainEvent;
- room state не считается Task lifecycle;
- отдельные outbox и room credentials не переносятся как канонические Task facts.

Shadow Validation разрешена только для фактов с однозначной семантической
связью. Она не создаёт второй authoritative store и не выполняет двойную запись.
Импорт старых данных — отдельная необязательная операция с backup, dry-run,
проверкой, явной mapping policy и идемпотентностью. Удаление legacy state и кода
требует работающей замены, сохранности необходимых данных и отдельного одобрения
владельца.

До удаления должны быть перенесены все используемые операции rooms, включая
assign, resume, say или его структурированный Discussion эквивалент, checkpoint,
ack, ready/regulation и block/unblock. Замена должна сохранить capability scope,
generation checks и идемпотентность. Переноса только чтения недостаточно.

## Порядок реализации

1. Зафиксировать ADR-0001; заморозить развитие `internal/rooms`.
2. До Task API и персистентной Task-модели определить формат и генерацию
   `TaskID`, Task lifecycle и связь Task ↔ Run.
   Формат `task_<ULID>` и генератор выделены в ADR-0003 и пакете `taskid`;
   Task lifecycle и семантика связи Task ↔ Run определены в
   `docs/TASK-MODEL.md`. Этот design-шаг закрыт; реализация Task aggregate ещё
   впереди.
3. Спроектировать Production Daemon по ADR-0004: single owner, command/read
   boundaries, supervisor relationship и recovery/readiness model. Дизайн
   принят. Библиотечный host для текущего Run coordinator реализован, но
   самостоятельного production executable, process-kill recovery и canonical
   Task runtime пока нет; это не закрывает P0 из ROADMAP.
4. Зафиксировать backend-neutral transaction boundary; проверить текущую
   `store.Persistence` и доказать семантику commit-or-reopen на snapshot.
   Текущие Run-only persistence и commit-or-reopen не образуют ещё полной
   канонической transaction boundary; её реализация остаётся открытой.
5. Принять ADR-0005 Storage Model: первый single-daemon backend — единый
   версионированный atomic snapshot. Дизайн миграций, import, locking и backup
   принят; безопасное pre-migration backup и новая canonical State ещё должны
   быть реализованы. Переход на SQLite зависит от измерений согласованных целей.
6. Довести библиотечный host до production daemon skeleton: standalone
   executable, process-level recovery, полная readiness и единый command path.
   Никакие новые канонические writer paths не должны обходить daemon.
7. Через daemon ввести Task aggregate, Task-wide Fence и атомарные переходы
   State + DomainEvent + Outbox.
8. Добавить структурированные ReviewResult и AuditResult.
9. Добавить Discussion Graph как типизированные факты и связи в каноническом
   event log; не вести вторую авторитетную копию nodes/edges.
10. Провести ограниченную Shadow Validation.
11. Проверить существующий Telepathy adapter и подключить агентский write path
    к Task/Run; transport не переписывать без необходимости.
12. Реализовать ADR-0006 API Contract, read-only API и Startup Card после
    проверки daemon readiness и канонического write path.
13. Реализовать Dashboard и Project Map поверх API.
14. Перевести нужные сценарии с rooms, сохранив capability scope, generations и
    идемпотентность всех используемых операций.
15. При необходимости выполнить отдельный проверяемый импорт legacy данных.
16. Удалить legacy только после критериев миграции и отдельного одобрения.

## Последствия

Положительные:

- один источник истины для задач, исполнения и решений;
- атомарная история, пригодная для аудита и объяснения решений;
- Dashboard и Graph не формируют собственную FSM;
- транспортные детали отделены от пользовательской картины работы;
- переход от rooms можно выполнять постепенно без ложного импорта фактов.

Стоимость и ограничения:

- production daemon, Task model, event journal, SQL/storage choice и Dashboard API
  являются будущей работой, а не завершёнными возможностями;
- миграция требует отдельной политики совместимости и переноса данных;
- структурированные Review/Audit и Discussion Graph расширяют доменную схему и
  требуют версионирования;
- транзакционный storage и event journal требуют тестирования crash/recovery,
  миграций и single-owner поведения.

## Критерии соответствия

Реализация соответствует ADR, если:

- у Dashboard нет собственной БД, FSM и изменяемых статусов;
- TaskID не выводится неявно из `Run.Task`;
- State Change, Domain Event и Outbox фиксируются атомарно;
- после неопределённой ошибки durable записи состояние не публикуется в память,
  а процесс прекращает записи и требует reopen;
- production daemon удерживает эксклюзивное владение state; второй экземпляр на
  том же state получает отказ;
- daemon валидирует загруженное состояние до readiness и восстанавливает durable
  outbox до обслуживания API (ADR-0004 RT-03; проверки до readiness и целевой
  состав — ADR-0005 ST-04);
- транспортная доставка не означает принятия задачи или результата;
- retry сохраняет Attempt, recovery сохраняет Attempt и повышает Fence;
- Task.Fence является авторитетным; входящие сообщения и supervisor CAS
  проверяют Task.Fence, а Run.Fence является только выданным Run снимком;
- Assign/rework не допускает двух активных writer Runs одной Task; Tick и
  recovery проверяют инвариант fail-closed;
- rework атомарно переводит прежний Run в `SUPERSEDED`, повышает Task.Fence и
  создаёт новый Run с новой Attempt;
- старый writer остановлен до передачи права записи новому owner;
- Owner decision проходит через Task-scoped capability без агентского Run;
- Review/Audit представлены структурированными результатами;
- Discussion Graph содержит типизированные факты и ссылки, а не копию чата;
- rooms не импортируется в каноническую модель без явного semantic mapping;
- Telepathy остаётся transport adapter, а все workflow decisions проходят через
  Coord.
