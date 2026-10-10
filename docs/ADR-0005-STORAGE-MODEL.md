# ADR-0005: Storage Model

- **Статус:** Accepted
- **Дата:** 2026-10-07
- **Редакционная пометка:** 2026-10-10: заменён устаревший вводный блок, уточнён состав CanonicalState, исправлены статусы ST-02 и ST-07, добавлен migration manifest, уточнены ST-01/ST-03/ST-06/ограничения, перенесены длинные test-списки ST-04 в реестр, исправлены пробелы перед ссылками — FD, принятые нормы ST-01–ST-07 не затронуты
- **Область:** durable storage канонического runtime Coord
- **Связанные решения:** ADR-0001 Canonical Coordination Model; ADR-0004 Runtime and Daemon Model

## Контекст

ADR-0004 требует одного владельца runtime state, атомарного commit состояния,
Domain Event и Outbox, exclusive ownership, fail-closed recovery и публикации
нового состояния только после durable commit.

Canonical snapshot и значительная часть его startup/transition validation реализованы.

Текущее состояние реализации, перечень доказательств и незакрытые части компонентов
ведутся в:

- `docs/architecture/COMPONENT-STATUS.yaml`;
- `docs/ARCHITECTURE.md`;
- `docs/TARGET-ARCHITECTURE.md`.

`checkpoint.validateRuns` относится только к legacy Run-only snapshot и не заменяет
canonical validation. Остаются открытыми production command integration, полный
readiness/recovery контур, schema migrations, backup/restore и TaskID collision handling.

Текущие Runs, role registry и executor checkpoints — разные области состояния.
Их существование не означает, что будущая Task-модель должна продолжать
раздельную запись. Исполнительский checkpoint и транспортные данные остаются
вне канонической транзакции Coord, если только они не являются Coord-owned
outbox/inbox записями.

## Рассмотренные варианты

### Версионированный atomic snapshot

Один полный state document сохраняется заменой temp + sync + rename + directory
sync. Он использует уже проверенную persistence основу и не добавляет runtime
зависимостей. Недостаток — каждый commit и восстановление работают с объёмом
всего state.

### SQLite

Локальная транзакционная БД хорошо подходит single-owner daemon и индексированным
запросам. Она добавляет driver/dependency и требует определить миграции, WAL,
backup и поведение после неясного commit результата. Выбор SQL сам по себе не
доказывает требуемые fencing и process ownership.

### PostgreSQL

Даёт сетевое совместное хранилище и более широкий deployment, но вводит отдельную
службу и эксплуатационную поверхность, не требуемую текущим single-owner local
runtime. Multi-host/active-active этим ADR не вводится.

## Решение

Для первого production daemon использовать **один версионированный atomic
snapshot** как backend полной канонической модели Coord. Ввести backend-neutral
Persistence boundary, принимающую и возвращающую полную canonical State, чтобы
позднее добавить SQLite без изменения доменных переходов. Run-only `store.Persistence` принимает только `map[string]fsm.Run` и остаётся
недостаточным контрактом для этого state boundary. `store.CanonicalPersistence`
(интерфейс в `store/canonical.go`) является отдельным контрактом рядом с legacy
Run-only `store.Persistence`; Run-only контракт не является canonical transaction
boundary.

Требуемый state document и backend-neutral transaction boundary реализованы как
`CanonicalState`, `store.CanonicalPersistence` и canonical snapshot backend.
Canonical schema уже охватывает реализованные Tasks, Runs, Fence, Domain Events,
Inbox, Outbox, AgentSession, Review/Audit и Discussion Graph data. Canonical state
включает как минимум:

- Tasks, Runs, Task.Fence и ownership/recovery records;
- AgentSession projections и append-only AgentSessionEvents;
- Domain Events и структурированные Results;
- ReviewResult и AuditResult;
- Discussion Graph;
- durable Inbox receipts и Outbox.

Project Relations, External References и отдельные ещё не реализованные canonical
slices добавляются в тот же versioned document без создания второй commit boundary.

`AgentSession` registration и history — это canonical state. Session presence и
transport reachability остаются отдельными runtime-наблюдениями с определённым
сроком жизни; их нельзя считать Task lifecycle или включать в канонический Event
Log без доменного основания.

## Инварианты хранения

### ST-01 — Единый commit document

Одна каноническая команда строит новое состояние, добавляет соответствующий
Domain Event и необходимые Outbox records, затем фиксирует их одной атомарной
заменой state document:

```text
Canonical State + Domain Event + Outbox [+ Inbox receipt when required] = One Durable Commit
```

Здесь и далее «durable commit» означает storage commit, а не Git commit.
События не записываются отдельным JSONL append и не могут коммититься независимо
от состояния, к которому относятся. В snapshot backend materialized state и
append-only Domain Events являются частями одного canonical document и одной durable
commit boundary. «Несогласованность state и event history» означает логическое
нарушение инвариантов внутри загруженного document, а не рассогласование независимых
storage-файлов. Materialized canonical State является источником recovery; Event Log
служит append-only журналом аудита. Если daemon обнаруживает нарушение инвариантов
при загрузке, он остаётся fail-closed до readiness и не пересобирает одну сторону
из другой. Входящий Inbox receipt включается в тот же
commit, если он нужен для durable deduplication обработанной команды/сообщения.

Проверка: частично — `TestCanonicalStoreRejectsTaskMutationWithoutEvent`, `TestCanonicalTaskOutboxRequiresEventFromSameCommit`, `TestCanonicalCommitFailureDoesNotPublishOrContinue`; не реализовано: production daemon command boundary.

### ST-02 — Единственный writer

Daemon удерживает exclusive process lock всего state namespace до остановки и
закрытия store. Kernel `flock` реализован для Linux, macOS, DragonFly BSD,
FreeBSD, NetBSD, OpenBSD и illumos; он автоматически освобождается ОС при
закрытии файла или завершении процесса, а сам lock-файл может оставаться на
диске. На Windows, Solaris, AIX и остальных неподдержанных платформах
`checkpoint.File.Lock` возвращает `ErrUnavailable`, а `store.New` отказывает в
открытии вместо работы без ownership. Второй daemon с тем же state получает
отказ до readiness. Все изменения проходят через daemon command path.

Snapshot backend не предоставляет database CAS. Межпроцессную эксклюзивность
обеспечивает process lock, а сериализацию команд внутри владельца — mutex/command
loop daemon. Правило единственного активного writer и его проверку задают
[ADR-0001](ADR-0001-CANONICAL-COORDINATION-MODEL.md) и
[TASK-MODEL.md](TASK-MODEL.md); storage не определяет его смысл и обязан
проверять его fail-closed до выдачи владения. Наличие process lock само по себе
этого правила не обеспечивает. После падения daemon новый процесс может получить
lock и продолжить только после проверки durable state.

Проверка: частично — `TestCoorddSecondProcessCannotOpenOwnedNamespace`, `TestRefusedOwnershipFailsBeforeLoadingState`, `TestLockExclusivityOrFailClosed`; не реализовано: единый production write path (legacy writers не переведены на daemon command path), производственная topology при переходе canonical ↔ legacy snapshot.

### ST-03 — Commit-or-reopen

Состояние в памяти публикуется только после успешного durable storage commit. Если
storage не может доказать, что прежний snapshot остаётся авторитетным и новый
snapshot не опубликован, результат считается неопределённым. После неопределённого
результата store блокирует следующие mutations до reopen и повторной validation.
Оптимизация, позволяющая продолжить после доказанно безопасной pre-publication ошибки,
требует отдельного проверенного error-classification contract. Daemon должен reopen
и заново проверить snapshot до возобновления записи.

Проверка: частично — `TestFailedCommitRequiresReopenBeforeFurtherWrites`; не реализовано: reopen и повторная проверка snapshot daemon'ом

### ST-04 — Загрузка до readiness

Canonical snapshot содержит schema version; `CanonicalFile.Load` отвергает
неизвестную версию и вызывает structural validation до того, как состояние
передаётся runtime. Валидатор проверяет идентичности и ссылки, FSM-инварианты,
единственный `TASK_CREATED`, непрерывность EventSeq/TaskRevision и полной цепочки
`TaskFence` по transfer marker, соответствие `Run.Fence` последнему marker,
отсутствие нескольких active writers и согласованность Event/Outbox/Inbox.
Это не доказывает аутентификацию субъекта или process stop proof: production
command path и полный readiness lifecycle остаются целевыми требованиями.
Существующий `checkpoint.validateRuns` относится только к legacy Run snapshot и
не является canonical validator.

Проверка: частично — `TestCanonicalStartupRejectsMissingFenceBumpOnTransfer`, `TestCanonicalStartupAcceptsFenceSnapshotsAcrossTransferAndRecovery`; полный список evidence — component `canonical-persistence` в `docs/architecture/COMPONENT-STATUS.yaml`; не реализовано: полная readiness integration и production command path.

### ST-05 — Миграции

Миграция схемы выполняется детерминированно до обслуживания команд, проверяет
результат и сохраняет новую версию тем же atomic commit. До первой записи новой
схемы мигратор создаёт и проверяет защищённую резервную копию исходного snapshot
(каталог 0700, файл 0600, вне Git/runtime export). Копия синхронизируется на диск
до замены исходного файла; без подтверждённой копии миграция не начинается.
После неопределённого результата commit daemon остаётся not-ready, а исходная
копия сохраняется для восстановления. Исходный snapshot нельзя считать
сохранённым одним только `rename`: atomic replacement заменяет файл по прежнему
пути. Автоматическая миграция не импортирует `internal/rooms` в канонические
Tasks и не выводит TaskID из исторического `Run.Task`. Для преобразования Run →
Task требуется явная проверенная mapping table; если она отсутствует или
неоднозначна, импорт не создаёт каноническую Task и daemon остаётся not-ready
для этого набора данных.

Каждая миграция создаёт migration manifest, содержащий минимум: исходный canonical
path; backup path; исходную и целевую schema version; digest и размер backup; время
создания; migration identifier; результат sync и verification. Существующий backup
другой миграции не перезаписывается. Точный формат manifest определяется migration
design.

Проверка: не реализовано

### ST-06 — Fencing и concurrency

В первом single-daemon backend конкурентные команды сериализуются на command
boundary. Проверка актуального `Task.Fence` и запись перехода находятся в одной
критической секции daemon и одном durable commit под эксклюзивным process lock.
Это логический CAS внутри единственного владельца, а не CAS, предоставляемый
snapshot storage. При добавлении SQLite fencing/CAS переносится в транзакционное
conditional update; он не вычисляется из `max(Run.Fence)`.

Daemon command boundary сериализует canonical mutations внутри единственного runtime
owner. Canonical store mutex является защитой реализации persistence boundary, но не
заменяет command authorization, scheduling или workflow ownership. Snapshot storage
обеспечивает durable commit, а daemon обеспечивает логический CAS и порядок команд.
Проверка: частично — `TestCanonicalTransitionFirstAssignmentIncrementsTaskFence`, `TestCanonicalTransitionReworkIncrementsAttemptAndFence`; полный список evidence — component `task-wide-fencing` в `docs/architecture/COMPONENT-STATUS.yaml`; не реализовано: production command path, supervisor stop proof до передачи.

### ST-07 — Уникальность TaskID

Transaction boundary обеспечивает уникальный ключ `TaskID`. Snapshot backend
проверяет идентификатор под daemon exclusive lock до commit; transactional
backend дополнительно задаёт native unique constraint. Если создание встречает
коллизию ULID, Coord генерирует другой ID и повторяет транзакцию; запись события
и Outbox для неуспешной попытки не сохраняется.

Проверка: частично — `TestGeneratorConcurrentCallsAreUnique`, `TestGeneratorProducesCanonicalRoundTrippableID`; не реализовано: проверка коллизии TaskID при canonical Task creation под transaction boundary, повтор генерации при столкновении, отсутствие Event/Outbox от неуспешной попытки, propagation ошибки entropy exhaustion в Task creation.

## Ограничения и переход к БД

Atomic snapshot выбран как минимальный первый backend для локального single-owner
runtime. Статус Accepted фиксирует выбор backend и не означает разрешение на
production rollout без измерений. Snapshot сохраняет всю каноническую транзакцию
в одном документе, но имеет стоимость O(размер state) на commit и load. При
append-only event log из E событий последовательные полные записи журнала
суммарно переписывают O(E²) объёма данных; каждый следующий commit также растёт
с полной историей. Доменные события не усекаются и не удаляются ради снижения
стоимости: это повредило бы каноническую аудиторскую историю. При переходе на
транзакционный backend полный журнал переносится без усечения и остаётся в той
же транзакционной границе с State и Outbox. Компакция допустима только по
отдельному ADR, который сохраняет проверяемую полную историю и атомарность.
Durable Inbox receipts и `Run.Seen` также участвуют в дедупликации и могут расти
вместе с числом принятых сообщений. Их нельзя удалять или ограничивать простым
TTL/окном без доказательства, что replay старого сообщения не повторит переход или
внешний результат. `Run.Seen` растёт монотонно в пределах одного Run; canonical
Inbox receipts могут жить дольше Run и имеют отдельный lifecycle и retention policy.
Рост `Run.Seen` связан с известным P2-ограничением «Рост дедупликации `Run.Seen`»
из ROADMAP. Canonical Inbox receipts создают сходное ограничение, но их scope,
retention и recovery semantics должны оцениваться отдельно. До принятия отдельной
политики дедупликации рост обоих остаётся измеряемым ограничением snapshot; перенос
на SQLite сам по себе не разрешает pruning ни одной структуры.

До production rollout должны быть заданы и измерены пределы размера state,
commit latency и времени восстановления; транзакционный backend требуется до
выхода за эти цели. Любой отдельный journal, коммитящийся вне state document,
требует пересмотра ST-01 и отдельного ADR, поскольку иначе появится вторая
commit-граница. Это ограничение нельзя скрывать кэшем, который становится
вторым источником истины.

SQLite следует принять отдельным решением, когда измерения daemon покажут, что
размер снимка, время commit/load, конкуренция чтения или требуемые индексы не
укладываются в согласованные эксплуатационные цели. До выбора SQLite нужно
проверить доступные Go drivers, целевые ОС, CGO/stdlib требования, WAL durability,
backup/restore и неопределённый commit. PostgreSQL требует отдельного решения о
сетевом deployment и не является автоматической следующей ступенью.

## Критерии соответствия

Реализация соответствует этому ADR, если:

- вся каноническая Task state, Event и Outbox запись атомарна;
- в каноническом write path нет отдельных файлов, создающих вторую commit границу;
- второй daemon не может открыть тот же state для записи;
- переход между storage-схемами удерживает compatibility lock, исключающий
  параллельные legacy writers того же namespace, пока они не переведены на
  daemon command path (R61, решение OWNER 2026-10-08);
- ошибки durability блокируют дальнейшие изменения до reopen/recovery;
- readiness объявляется только после проверки и восстановления state;
- версия snapshot явно проверяется, а неизвестная версия закрывает readiness;
- fencing проверяется на текущей Task в той же транзакции, что и запись;
- transaction boundary проверяет уникальность сохранённых TaskID; snapshot делает
  это под single-writer lock, transactional backend также использует native
  constraint;
- domain logic не зависит от JSON layout или конкретного backend.

## Открытые вопросы

- Поддержка process locking на платформах вне перечня ST-02, то есть на
  Windows, Solaris и AIX, и список целевых ОС production runtime. Runtime-поведение
  там fail-closed: `Lock` возвращает `ErrUnavailable` до загрузки состояния,
  что следует из build tag в `internal/filelock`, а не подтверждается CI. Проверяется
  только Windows, и только на этапе компиляции: `make cross-vet` выполняет
  `GOOS=windows go vet`. Сборка на Solaris, AIX и остальных ОС вне перечня ST-02
  не проверяется. Отказ до `Load` закреплён тестами
  `store.TestRefusedOwnershipFailsBeforeLoadingState` и
  `checkpoint.TestLockOwnsSnapshotOrRefusesWithoutRelease` на хостовой платформе.
- Целевые пределы state size, commit latency и времени восстановления.
- Формат backup/restore и требования к шифрованию at rest.
- Будет ли SQLite первым production backend вместо snapshot после оценки нагрузки.
