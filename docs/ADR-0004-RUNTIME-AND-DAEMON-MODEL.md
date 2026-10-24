# ADR-0004: Runtime and Daemon Model

- **Статус:** Accepted
- **Дата:** 2026-10-07
- **Редакционная пометка:** 2026-10-10: заменён устаревший inline-блок реализации ссылкой на COMPONENT-STATUS.yaml; исправлены статусы RT-02, RT-03, RT-04, RT-06 — sync с принятым кодом; уточнена readiness; добавлен маркер ADR-0009 в Process Ownership; диаграмма RT-04 в code block; терминология Task.Fence/Run.Fence унифицирована — FD, принятые нормы не затронуты
- **Область:** runtime ownership и процессный контракт production daemon Coord
- **Связанные решения:** ADR-0001 Canonical Coordination Model; ADR-0002 Project Dependency Graph; ADR-0003 External Reference Model

## Контекст

ADR-0001 определяет Coord как единственный источник канонического состояния.
Production daemon и единый runtime write path реализованы не полностью.
Storage должен выбираться с учётом требований daemon к ownership, fencing,
durable transitions, recovery и готовности API.

Этот ADR задаёт целевую нормативную модель.

Текущее состояние реализации, доказательства и незакрытые части компонентов
ведутся в:

- `docs/architecture/COMPONENT-STATUS.yaml`;
- `docs/ARCHITECTURE.md`;
- `docs/TARGET-ARCHITECTURE.md`.

Наличие canonical core не означает подключения production caller path.

Решением OWNER R119 (2026-10-08) был обновлён только статус реализации;
нормативные требования ADR не менялись.

Нельзя допустить, чтобы CLI, Dashboard, Telepathy adapter или второй daemon
получили независимые пути записи в одно состояние.

## Решение

Coord работает как production daemon — единственный runtime-владелец канонического
состояния. Все writer-компоненты используют его команды и переходы. Storage
является durable persistence boundary, а не самостоятельным координатором
workflow.

### RT-01 — Single Owner

В каждый момент времени только один daemon имеет право изменять конкретный
runtime state. Второй daemon, открывающий тот же state, получает отказ до
обслуживания запросов. Ownership обеспечивается механизмом, детали которого
определяются в ADR-0005 и deployment design.

Это требование относится к одному state namespace. Распределённый multi-host
leader election, шардинг задач и active-active режим этим ADR не вводятся.

Проверка: `TestCoorddSecondProcessCannotOpenOwnedNamespace`, `TestCoorddTakesNamespaceLockBeforeStores`

### RT-02 — Authority Boundary

Daemon авторитетен для:

- Tasks, Runs, Task.Fence и ownership transfer;
- workflow transitions и scheduling;
- Domain Events, Discussion, Review/Audit и Artifacts metadata;
- Project Dependency Graph и External References;
- Inbox/Outbox и восстановления доставки.

CLI, Dashboard, API handlers, agent adapters и Telepathy не владеют отдельными
копиями изменяемого канонического состояния. Внешний supervisor отвечает за
жизненный цикл процесса daemon и подтверждение остановки старого writer; он не
становится владельцем Task workflow.

Проверка: частично — `TestCoorddSecondProcessCannotOpenOwnedNamespace`, `TestCanonicalFileRoundTripsTaskEventAndExplicitRunRelation`; не реализовано: единый production write path, Project Dependency Graph, External References, scheduler, Artifacts model.

### RT-03 — Recovery и readiness

После запуска daemon не принимает записи и не объявляет себя ready, пока не
выполнит последовательно:

1. Получит exclusive ownership state.
2. Загрузит и проверит durable state, версии схемы и инварианты.
3. Восстановит runtime индексы и вычисляемые представления.
4. Восстановит durable Outbox и незавершённые delivery/recovery операции.
5. Проверит отсутствие конфликтующих активных writer Runs на Task и
   согласованность Task.Fence с Run fence snapshots.
6. Завершит безопасное recovery, требующее внешнего доказательства остановки
   старого writer, через supervisor.

Если целостность или durability не подтверждены, daemon остаётся not-ready и
отказывает в обычных изменяющих операциях. До инициализации transport receive
обычные изменяющие endpoints закрыты. При состоянии not-ready обычные workflow
mutations остаются закрыты; минимальные supervisor recovery endpoints MAY быть
доступны только после инициализации их authentication и transport boundary,
поскольку они нужны для получения stop proof и завершения recovery. Доступность
recovery endpoints не означает общую readiness daemon. Неопределённая ошибка
durable commit требует остановить дальнейшие записи и reopen/recovery согласно
ADR-0001.

Crash/restart сохраняет каноническое состояние: daemon восстанавливает его из
durable storage и продолжает только после повторной проверки ownership и
инвариантов. Временная память daemon не является источником истины.

Проверка: частично — `TestDaemonReadinessKeepsMutationsClosedDuringRecovery`, `TestCoorddProcessRestartRestoresAndReplaysOutboxBeforeReady`; не реализовано: полная проверка схемы и Task-инвариантов (шаг 2), полный Task.Fence startup check (шаг 5), production recovery, supervisor stop proof, readiness охватывающая весь набор RT-03.

### RT-04 — Write Path

```text
CLI / Agent / Integration
          │ command
          ▼
     Coord Daemon
          │ validate + transition
          ▼
        Storage
```

Все изменения Task, Run, Fence, Events, Graph, Review/Audit, External References
и Outbox проходят через daemon. Вызов storage напрямую из CLI, Dashboard,
агентского процесса или transport adapter запрещён.

Daemon проверяет подпись/аутентификацию, субъект, capability, TaskID, Run,
Attempt, Session generation, Task.Fence и допустимость перехода до durable
фиксации. Transport delivery не является подтверждением принятия Task или
результата.

В одной логической транзакции фиксируются изменяемое каноническое состояние,
Domain Event и необходимые Outbox records согласно ADR-0001. Если обработанная
входящая команда требует durable Inbox receipt для дедупликации, receipt входит
в ту же транзакцию. Публикация нового состояния в памяти происходит только после
durable commit.

Canonical command boundary реализована на store model и проверяет: authenticated
signed frame; AgentSession, generation и ключ; Run assignment; Task.Fence и Attempt;
role и Stage; replay и ExpectedRevision; атомарную запись transition, DomainEvent,
Inbox receipt и Outbox. `ApplyAgentCommand` пока вызывается только из тестов.
OwnerCredential bootstrap, lifecycle и подписанный `task.create` endpoint работают
на отдельной atomic store boundary; agent command production endpoint и Executor
RESULT path остаются вне реализации.
Проверка: частично — `TestApplyInboxCommandCommitsReceiptWithTaskEventAndDeduplicates`, `TestApplyInboxCommandRejectsStaleExpectedRevision`, `TestControlOwnerCommandEndpointPerformsSignedCanonicalTaskCreate`, `TestOwnerCredentialSignedRevokeIsAtomicAndExactRetryReturnsReceipt`; не реализовано: production CLI/WebAuthn caller, agent command endpoint, Executor RESULT path, единый write path для всех legacy writers.

### RT-05 — Read Path

> **RT-05 задаёт целевую read boundary.** Read-only Task API, Startup Card и
> Dashboard API не являются текущими product capabilities до принятия ADR-0006
> и их реализации. Существующий recovery-oriented control endpoint не является
> реализацией RT-05.

CLI, Dashboard, API и диагностические клиенты читают каноническое состояние и
проекции через read interface daemon. Они не читают и не модифицируют storage
напрямую во время работы daemon.

Dashboard использует read-only API. Управляющие команды CLI и агентов идут через
отдельный авторизованный command interface daemon. Контракт read API, его bind и
транспорт локального IPC/HTTP определяются в ADR-0006. Аутентификация Owner
определяется проектом границы команд до начала P0-3; граница команд агентов
определяется отдельным проектом до P0-3.

Изменено решением OWNER R105 (2026-10-08): аутентификация Owner вынесена из
ADR-0006 в проект границы команд.

Проверка: не реализовано

### RT-06 — Task-wide Fencing

`Task.Fence` — authoritative ownership epoch для записи в Task. `Run.Fence` —
только snapshot epoch, выданный конкретному Run. Все agent writes и supervisor
recovery CAS сверяют значение с актуальным `Task.Fence`.

Assign/rework не допускает два активных writer Runs на Task. Tick/recovery
проверяют инвариант fail-closed. При передаче владения Fence повышается; recovery
не передаёт владение, пока внешний supervisor не подтвердил остановку старого
writer. Детальные состояния и переходы зафиксированы ADR-0001.

Canonical `ApplyAgentCommand` проверяет Task.Fence, Run.Fence и current writer до
durable transition. Полный список evidence — component `task-wide-fencing` в
`docs/architecture/COMPONENT-STATUS.yaml`.
Проверка: частично — `TestFenceValidationPrecedesOwnerAndDeduplication`, `TestRecoveryCASAndOldOwnerRejected`; не реализовано: production caller path, production supervisor recovery CAS, stop proof, ownership transfer через canonical daemon command path.

## Process Ownership

Этот ADR задаёт разделение ответственности daemon и внешнего supervisor. Конкретные
Session Registry, process identity, admission и termination-plan контракты
определяются ADR-0009. До принятия и реализации ADR-0009 proposed session/process
states не являются доказательством текущего состояния процесса и не разрешают
process control.

Coord daemon не запускает и не останавливает процессы агентов самостоятельно.
Это обязанность внешнего supervisor. Daemon запрашивает действия supervisor и
учитывает recovery только после подтверждаемого результата остановки.

Supervisor отвечает за:

- запуск и остановку daemon/agent processes в заданном deployment;
- доказательство завершения старого writer перед recovery;
- передачу Coord факта процесса и результата операции.

Supervisor не решает Task workflow, не присваивает Review/Audit verdict и не
меняет Task state в обход daemon.

## Наблюдаемость

Целевой daemon предоставляет состояние готовности и причину not-ready,
связанную с конкретной незавершённой проверкой. Причина должна охватывать весь
набор RT-03, а не только transport и supervisor recovery. Метрики транспорта не
заменяют readiness Coord и не означают принятие агентской работы.

Текущий `/readyz` покрывает только operational readiness существующего Run
coordinator и не является критерием готовности Task-wide fence, конфликтующих
writer Runs, канонических Task схем и полного RT-03 recovery. Состав текущих
проверок и причин not-ready, а также состояние P0 Production coordinator daemon
приведены в [TARGET-ARCHITECTURE.md](TARGET-ARCHITECTURE.md); здесь они не
дублируются.

## Последствия

- Все способы изменения состояния проходят один проверяемый путь.
- После crash можно восстановить ownership только из durable state и проверки
  инвариантов; память предыдущего процесса не требуется.
- Runtime должен обеспечивать exclusive owner, проверку актуального Task.Fence
  вместе с переходом, атомарность State + Event + Outbox и безопасную публикацию
  после commit. Для snapshot эти свойства обеспечиваются process lock и
  сериализованной daemon transaction boundary; storage не заявляет database CAS.
  Transactional backend может дополнительно выполнять fence check через
  conditional update. Детали backend определяет ADR-0005.
- API проектируется после runtime readiness contract и предоставляет данные
  только через daemon.
- Локальный однопроцессный daemon является исходной моделью. Распределённое
  лидерство и active-active требуют отдельного ADR.

## Критерии соответствия

Реализация соответствует ADR, если:

- второй daemon не может писать в тот же state namespace;
- ни один внешний компонент не пишет в storage в обход daemon;
- daemon не принимает изменяющие запросы до recovery/readiness;
- каждый state transition и его Domain Event/Outbox фиксируются атомарно;
- все Run writes и recovery CAS проверяют актуальный Task.Fence;
- при нескольких активных writer Runs Task блокируется fail-closed;
- rework терминально supersedes предыдущий Run до запуска нового writer;
- старый процесс остановлен supervisor до передачи права записи;
- transport delivery не трактуется как task/result acceptance;
- внешний supervisor управляет процессами, а daemon — каноническим workflow.
