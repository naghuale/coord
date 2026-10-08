# ADR-0004: Runtime and Daemon Model

- **Статус:** Accepted
- **Дата:** 2026-10-07
- **Область:** runtime ownership и процессный контракт production daemon Coord
- **Связанные решения:** ADR-0001 Canonical Coordination Model; ADR-0002 Project Dependency Graph; ADR-0003 External Reference Model

## Контекст

ADR-0001 определяет Coord как единственный источник канонического состояния.
Production daemon и единый runtime write path реализованы не полностью.
Storage должен выбираться с учётом требований daemon к ownership, fencing,
durable transitions, recovery и готовности API.

Этот ADR задаёт целевое поведение, а не описание текущей реализации. Состояние
реализации на 2026-10-08:

- каркас daemon реализован: `cmd/coordd`, ownership namespace, readiness и
  recovery (RT-01, RT-03). P0-1 остаётся в работе, его полная приёмка не закрыта
  (ROADMAP, R82);
- Task-wide Fence реализован частично: storage transition и startup validation
  проверяют цепочку Fence (RT-06);
- write path реализован частично: canonical store фиксирует Inbox receipt вместе
  с Task и Domain Event в одном commit (RT-04). Единый API/CLI/agent command
  path через daemon не реализован.

Изменено решением OWNER R119 (2026-10-08): обновлены только сведения о
реализации. Статус и нормы ADR не менялись.

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

Проверка: не реализовано

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
отказывает в обычных изменяющих операциях. Пока transport receive не
инициализирован, control API закрыт; после инициализации supervisor recovery
endpoints остаются доступны при not-ready, поскольку только они могут завершить
ожидание stop proof. Неопределённая ошибка durable commit требует
остановить дальнейшие записи и reopen/recovery согласно ADR-0001.

Crash/restart сохраняет каноническое состояние: daemon восстанавливает его из
durable storage и продолжает только после повторной проверки ownership и
инвариантов. Временная память daemon не является источником истины.

Проверка: частично — `TestDaemonReadinessKeepsMutationsClosedDuringRecovery`, `TestCoorddProcessRestartRestoresAndReplaysOutboxBeforeReady`; не реализовано: проверки схемы и инвариантов Task (шаг 2) и Task.Fence (шаг 5)

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

Проверка: частично — `TestApplyInboxCommandCommitsReceiptWithTaskEventAndDeduplicates`, `TestApplyInboxCommandRejectsStaleExpectedRevision`, `TestApplyInboxCommandRequiresSameCommitEventAndPreservesStateOnFailure`; не реализовано: command interface daemon, проверки подписи, субъекта, capability, Session generation и Task.Fence входящей команды, единый путь записи для всех writer (localchat host пишет runs.json напрямую)

### RT-05 — Read Path

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

Проверка: частично — `TestFenceValidationPrecedesOwnerAndDeduplication`, `TestRecoveryCASAndOldOwnerRejected`, `TestCanonicalTransitionRejectsOwnerChangeWithoutTaskFenceBump`, `TestCanonicalTransitionRejectsFenceBumpWithoutOwnershipTransfer`, `TestCanonicalTransitionRejectsFenceJump`, `TestCanonicalTransitionRequiresOwnershipTransferEvent`, `TestCanonicalTransitionRequiresRunFenceToMatchTaskFence`, `TestCanonicalTransitionRejectsSecondActiveWriter`, `TestCanonicalTransitionRejectsRecoveryWithoutStalledRecoveryClaim`, `TestCanonicalTransitionRejectsFenceOverflow`; не реализовано: incoming write и recovery CAS против текущего `Task.Fence` в production command path, stop proof supervisor до передачи владения

## Process Ownership

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
