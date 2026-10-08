# ADR-0005: Storage Model

- **Статус:** Accepted
- **Дата:** 2026-10-07
- **Область:** durable storage канонического runtime Coord
- **Связанные решения:** ADR-0001 Canonical Coordination Model; ADR-0004 Runtime and Daemon Model

## Контекст

ADR-0004 требует одного владельца runtime state, атомарного commit состояния,
Domain Event и Outbox, exclusive ownership, fail-closed recovery и публикации
нового состояния только после durable commit.

Состояние реализации этого ADR приведено в
[TARGET-ARCHITECTURE.md](TARGET-ARCHITECTURE.md): Run-only слой, канонический
transaction unit, production composition `coordd` и платформы snapshot lock.
Здесь они не дублируются.

Текущие Runs, role registry и executor checkpoints — разные области состояния.
Их существование не означает, что будущая Task-модель должна продолжать
раздельную запись. Исполнительский checkpoint и транспортные данные остаются
вне канонической транзакции Coord, если только они не являются Coord-owned
outbox/inbox записями.

Текущая загрузка snapshot не выполняет целевые readiness-проверки ADR-0004.
`checkpoint.validateRuns` проверяет соответствие map key полю `RunID`, ненулевые
`Attempt` и `Fence`, а также непустой `OwnerSession`; coordinator дополнительно
проверяет часть полей при создании. Проверки Task-level fencing, единственного
активного writer на Task, ссылочной целостности и Event/Outbox ещё не существуют,
поскольку соответствующая каноническая модель пока не реализована.

Актуальный статус реализации зафиксирован в строках «Проверка:» этого ADR и
в [TARGET-ARCHITECTURE.md](TARGET-ARCHITECTURE.md).

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
позднее добавить SQLite без изменения доменных переходов. Run-only
`store.Persistence` принимает только `map[string]fsm.Run` и остаётся недостаточным
контрактом для этого state boundary; требуемый контракт реализован отдельно как
`store.CanonicalPersistence` (интерфейс в `store/canonical.go`) поверх Run-only слоя.

Это решение не означает, что текущий Run-only `checkpoint.File` уже является
реализацией будущего хранилища. До Task API требуется новый state document и
transaction boundary, охватывающий как минимум:

- Tasks, Runs, Task.Fence и ownership/recovery records;
- Domain Events и структурированные Results;
- Discussion Graph, Review и Audit;
- External References и Project Relations;
- durable Inbox/Outbox, необходимые для идемпотентного write path.

Session presence и transport reachability остаются отдельными runtime-наблюдениями
с определённым сроком жизни; их нельзя считать Task lifecycle или включать в
канонический Event Log без доменного основания.

## Инварианты хранения

### ST-01 — Единый commit document

Одна каноническая команда строит новое состояние, добавляет соответствующий
Domain Event и необходимые Outbox records, затем фиксирует их одной атомарной
заменой state document:

```text
Canonical State + Domain Event + Outbox [+ Inbox receipt when required] = One Durable Commit
```

События не записываются отдельным JSONL append и не могут коммититься независимо
от состояния, к которому относятся. Materialized canonical State является
источником recovery; Event Log служит append-only журналом аудита. При
несогласованности snapshot и журнала daemon fail-closed до readiness и не
пересобирает одну сторону из другой. Входящий Inbox receipt включается в тот же
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

Проверка: частично — `TestCoorddSecondProcessCannotOpenOwnedNamespace`, `TestRefusedOwnershipFailsBeforeLoadingState`, `TestLockExclusivityOrFailClosed`; не реализовано: все изменения только через daemon command path (localchat host пишет runs.json напрямую), совместное владение namespace при переходе на canonical snapshot, один активный writer Run на Task

### ST-03 — Commit-or-reopen

Состояние в памяти публикуется только после успешного durable commit. Если запись,
rename или sync вернули ошибку с неопределённым результатом, store прекращает
изменяющие операции. Daemon должен reopen и заново проверить snapshot до
возобновления записи.

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

Проверка: частично — загрузчик проверяет schema version, EventSeq/TaskRevision, единственный начальный `TASK_CREATED`, `TaskFence` по revision, непрерывность transfer chain, итоговый `Task.Fence`, связь `Run.Fence` с последним transfer marker Run и принадлежность Event.RunID своей Task (`TestCanonicalStartupRejectsWrongFenceInEarlyRevision`, `TestCanonicalStartupRejectsWrongFenceInLatestRevision`, `TestCanonicalStartupRejectsMissingFenceBumpOnTransfer`, `TestCanonicalStartupRejectsExtraFenceBumpWithoutTransfer`, `TestCanonicalStartupRejectsTwoTransferMarkersInOneRevision`, `TestCanonicalStartupRejectsSecondEventInCreationRevision`, `TestCanonicalStartupRejectsTransferMarkerWithoutRunID`, `TestCanonicalStartupRejectsFenceOverflow`, `TestCanonicalStartupRejectsFinalFenceDifferentFromTask`, `TestCanonicalStartupRejectsSecondTaskCreated`, `TestCanonicalStartupRejectsLateTaskCreated`, `TestCanonicalStartupRejectsEventRunIDOwnedByAnotherTask`, `TestCanonicalStartupRejectsRunFenceDifferentFromLatestTransfer`, `TestCanonicalStartupRejectsTaskScopedRunWithoutTransferMarker`, `TestCanonicalStartupAcceptsFenceSnapshotsAcrossTransferAndRecovery`, `TestCanonicalFileRejectsTransferredV1WithoutFenceAndPreservesSource`). Остальные проверки формата, FSM и ссылок покрыты перечисленными выше тестами; не реализовано: полная readiness integration и production command path.

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

Проверка: не реализовано

### ST-06 — Fencing и concurrency

В первом single-daemon backend конкурентные команды сериализуются на command
boundary. Проверка актуального `Task.Fence` и запись перехода находятся в одной
критической секции daemon и одном durable commit под эксклюзивным process lock.
Это логический CAS внутри единственного владельца, а не CAS, предоставляемый
snapshot storage. При добавлении SQLite fencing/CAS переносится в транзакционное
conditional update; он не вычисляется из `max(Run.Fence)`.

Проверка: частично — storage transition сериализован store mutex и ownership lock; change-time transfer/recovery проверяется `TestCanonicalTransitionFirstAssignmentIncrementsTaskFence`, `TestCanonicalTransitionRecoveryPreservesAttemptAndIncrementsTaskFence`, `TestCanonicalTransitionRoleHandoffPreservesWorkflowAttempt`, `TestCanonicalTransitionReworkIncrementsAttemptAndFence`, `TestCanonicalTransitionRetryPreservesAttemptAndTaskFence`; startup проверяет цепочку event fence и снимок каждого Run; не реализовано: production command path и supervisor stop proof до передачи.

### ST-07 — Уникальность TaskID

Transaction boundary обеспечивает уникальный ключ `TaskID`. Snapshot backend
проверяет идентификатор под daemon exclusive lock до commit; transactional
backend дополнительно задаёт native unique constraint. Если создание встречает
коллизию ULID, Coord генерирует другой ID и повторяет транзакцию; запись события
и Outbox для неуспешной попытки не сохраняется.

Проверка: не реализовано

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
Durable Inbox receipts и `Run.Seen` также участвуют в дедупликации и могут
расти вместе с числом принятых сообщений. Их нельзя удалять или ограничивать
простым TTL/окном без доказательства, что replay старого сообщения не повторит
переход или внешний результат. До отдельной политики дедупликации рост Inbox и
`Run.Seen` остаётся измеряемым ограничением snapshot; перенос на SQLite сам по
себе не разрешает их pruning. Обе карты растут монотонно вместе с числом принятых
сообщений и очищаются только при recovery, поэтому это то же самое известное
ограничение P2 «Рост дедупликации `Run.Seen`» из ROADMAP, а не отдельная проблема
хранилища: изменение snapshot backend его не снимает.

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
