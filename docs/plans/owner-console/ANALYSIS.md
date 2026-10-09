# Owner Console: анализ по нормам и коду

Issue: [#18](https://github.com/naghuale/coord/issues/18). HEAD проверки: `51275c1`
(ветка `p0-2-docs`, совпадает с `p0-2-task`).

Материалы: `CONCEPT.md` и `mockup.png` (концепт OWNER, 2026-10-09),
`OWNER-1-STRUCTURE.md` (структура и указания OWNER), `OWNER-2-SCREENS-VS-NORMS.md`,
`OWNER-3-SOURCE-MATRIX.md` (матрица источников), `CODEX-SECURITY.md` и
`CODEX-SECURITY-2.md` (срез по безопасности и границе команд), `COORD-NOTES.md`
(первичные находки C1–C9 — не норма).

Это анализ совместимости и источников, а не нормативное решение. Нормы здесь не
правятся и не предлагаются к правке. Каждая ячейка «каноническое», «реализовано»
или «есть» сверена с кодом на `51275c1`: `store/canonical.go`, `task/*.go`,
`fsm/fsm.go`, `agentsession/*.go`, `cmd/coordd`, `.github/workflows/ci.yml`.
Где материал или вводная расходятся с кодом, в ячейке стоит факт по коду и пометка
«расхождение с вводной».

Условные обозначения для каждого элемента интерфейса:

- **Target** — как работает после реализации зависимостей;
- **Current** — что есть в текущем дереве и каким тестом подтверждено, иначе «нет»;
- **Gap** — какого объекта, API, перехода, журнала или решения не хватает.

Плюс карточка источника: класс `Canonical / derived / external / client-local /
unknown`, свежесть, trust boundary, участвует ли в переходах, что показывается при
UNKNOWN или устаревшем значении, зависимость.

## 1. Назначение Owner Console

Рабочее место владельца поверх канонического состояния Coord: ответы на вопросы
«всё ли работает», «есть ли вопрос, который без OWNER нельзя решить», «что
выполняется и какой следующий шаг», «почему задача остановилась», «что
зафиксировано, опубликовано, влито, развёрнуто» (CONCEPT §1).

Норма-основание: ADR-0001, «Порядок реализации», шаг 13 «Реализовать Dashboard и
Project Map поверх API»; шаг 12 ставит ADR-0006 API Contract, read-only API и
Startup Card перед этим. ROADMAP, строка «После ADR-0006 | Dashboard и Project Map»:
«Dashboard — read-only проекция API; Attention/Health вычисляются, не хранятся;
UI не создаёт собственных Task/Room/workflow states».

Консоль не является источником истины: факты берутся из канонического состояния
Coord, кода и тестов (AGENTS.md, «Источники истины»).

При неопределённости показывается одна структурированная карточка, а не текст
свободной формы (CONCEPT §1, §6) — это совпадает с ADR-0001 CM-06: эскалация
содержит причину, основание, точные разрешённые варианты и связанные evidence.

## 2. Границы доверия

### 2.1 Read channel

Строит `OwnerView` и ничего не изменяет.

- Норма: ADR-0004 RT-05 «CLI, Dashboard, API и диагностические клиенты читают
  каноническое состояние и проекции через read interface daemon… Dashboard
  использует read-only API»; ADR-0001 CM-02 «Dashboard без FSM и БД».
- Current: read-only API для Task и OwnerView у coordd нет. Существующий control
  endpoint — `GET /runs` (`control.go:27-35`); он отдаёт только stalled-результаты с
  `RecoveryNeeded` для supervisor recovery, то есть это control interface, а не Task
  read API. Evidence: `TestDaemonReadinessLifecycle` проверяет для `/runs` только код
  200; recovery-семантику задаёт сам `control.go` и проверяет
  `TestHTTPWatchdogControl` (`coordinator_test.go:289`). Для read-модели Task
  доказательств нет.
- Gap: ADR-0006 (контракт, bind, транспорт), read DTO, `SourceRevision`,
  `ProjectionVersion`, `ComputedAt`. Это зависимость среза 1, а не вопрос OWNER.
  Готовность публикуется после критериев порядка реализации ADR-0001 (проверки
  daemon readiness и канонического write path), поэтому Owner Console не строится
  поверх `/readyz` или `GET /runs`: `/readyz` не равен полной готовности модели
  Task-wide (ADR-0004, «Наблюдаемость» — покрывает только operational readiness
  существующего Run coordinator).

### 2.2 Owner command channel

Принимает подписанные OWNER-команды; кнопка «Принять A» — клиент этого канала, а не
запись через Dashboard.

- Норма: ADR-0001 CM-12 (R110) — identity извлекается из проверенной
  `OwnerCredential`, команда проверяется по подписанному challenge; CM-06 — Coord
  атомарно записывает decision event и переход; TASK-MODEL TM-24 — Owner decision
  проходит через Task-scoped capability и CAS по текущей версии Task под daemon
  command boundary. ADR-0004 RT-05: управляющие команды идут через отдельный
  авторизованный command interface.
- Состав challenge по принятому проекту границы команд (COMMAND-BOUNDARY §3.4):
  MessageID, TaskID, действие, payload hash и Task Revision; для команды по графу
  зависимостей — ещё Graph Revision (ADR-0002 DG-07/DG-08). Значит «Принять A»
  подписывает именно показанный вариант конкретной карточки, а не свободный
  ответ; control token — пропуск в канал, не авторизация (R112).
- Current: `OwnerCredential` нормирован — CM-12 в ADR-0001 вводит его как
  каноническую сущность Coord с собственным потоком событий, и он есть в перечне
  целевых канонических сущностей; но в `CanonicalState` и store не реализован,
  verifier challenge и путь `coord decide` отсутствуют (COMMAND-BOUNDARY §3.2 — P0-3).
  Evidence: реализация не существует — искал `OwnerCredential` в `store/canonical.go`,
  `cmd/` и `internal/`, совпадений нет; тестов нет.
- Gap: агрегат `OwnerCredential`, verifier challenge, команда decide, схема события
  решения OWNER (см. раздел 6 и раздел 12), правило массового изменения
  зависимых Task (решение OWNER R130, Q3).

### 2.3 Web read-only boundary

Веб-интерфейс остаётся read-only, пока отдельно не спроектирована безопасная
OWNER-аутентификация для браузера (OWNER-1; правило приёмки 5 из OWNER-3 §14).
Решение OWNER R131 (Q4) фиксирует эту границу: решение принимается через CLI, а
варианты с командами из браузера требуют отдельного security design и до его
принятия не реализуются.

- Норма: ADR-0004 RT-05 (Dashboard — read-only API), ADR-0001 CM-09 (секреты не
  попадают в Task, Startup Card, Discussion Graph, DomainEvent и логи; в журнал
  идёт только факт и результат защищённого handoff).
- Current: веб-интерфейса нет — «нет».
- Gap: отдельный security design для браузера; до него web остаётся проекцией без
  командного канала. Это граница, которую нельзя пройти кнопкой в вебе: OWNER
  identity в браузере не устанавливается (решение OWNER R131, Q4).

## 3. Карта экранов

| UI element | Target | Current | Gap | Source | Data class | Freshness | Trust boundary | Affects Task transitions | UNKNOWN / stale behavior | Current evidence | Required dependency |
|---|---|---|---|---|---|---|---|---|---|---|---|
| Главный (CONCEPT §2) | проекция из canonical state и внешних наблюдений; ничего не пишет | нет — `OwnerView` не определён и не реализован | ADR-0006, read API, `OwnerView` | `OwnerView`: ProjectSummary, SystemReadiness, AttentionItems, TaskSummaries, RecentChanges, PublicationObservations | derived (с внешними наблюдениями) | при чтении snapshot; внешние элементы — по `observed_at` | read channel | нет | неизвестное значение показывать как UNKNOWN с пометкой «источник не найден», не заменять предположением | реализация не существует, прямой тест не найден | ADR-0006 и read API |
| Attention (CONCEPT §3) | производная Attention с причиной, основанием, вариантами | нет — Attention projection в коде отсутствует; TM-11 реализована частично (`TestTaskValidationRejectsInvalidPlanAndTerminalStage`), TM-12 не реализована; `RequireOwnerDecision` в StagePlan есть (`task/model.go:36`), в UI не выводится | TM-12, структурированный blocker, проекция Attention | Task `Status=BLOCKED` + blocker reason (TM-11, TM-12) | derived | при чтении snapshot; при неизвестном blocker карточка не создаётся | read channel; запись решения — command channel | нет для чтения, да для записи решения | неизвестное значение показывать как UNKNOWN с пометкой «источник не найден», не заменять предположением | проекции Attention в дереве нет; TM-11 покрыта частично (`TestTaskValidationRejectsInvalidPlanAndTerminalStage`), TM-12 не реализована | ADR-0006 и read API |
| Список задач (CONCEPT §4) | проекция, статусы OWNER вычисляются | частично: Tasks/Runs/StagePlan в canonical state есть | read API, `TaskSummaries` | Tasks, StagePlan, Runs, Events | derived | при чтении snapshot | read channel | нет | неизвестное значение показывать как UNKNOWN с пометкой «источник не найден», не заменять предположением | проекции списка в дереве нет; `TestCanonicalStateValidatesTaskEventAndRunRelations` доказывает инварианты хранилища, а не проекцию | ADR-0006 и read API |
| «Кратко» (CONCEPT §5) | детерминированная сводка | частично: Task и Runs есть; Task Brief не реализован | ADR-0007 (Proposed), read API | Task, Runs, Results, Brief | derived | при чтении snapshot | read channel | нет | неизвестное значение показывать как UNKNOWN с пометкой «источник не найден», не заменять предположением | Task Brief не реализован (ADR-0007 Proposed), сводки «Кратко» в дереве нет | ADR-0006 и read API |
| «Работа» (CONCEPT §5) | детализация назначений и проверок | частично: `AgentID`, `AgentSessionGeneration`, RoleHistory в Run есть (части AgentSession 1–3) | Agent command boundary (P0-3), результаты Executor | Runs, RoleSelection/RoleHistory, Results | derived из canonical | при чтении snapshot | read channel | нет | неизвестное значение показывать как UNKNOWN с пометкой «источник не найден», не заменять предположением | данные Run есть (`TestCanonicalStartupRequiresGenerationAndBindingForActiveTaskRun`), но вкладки «Работа» в дереве нет | ADR-0006 и read API |
| «Доказательства» (CONCEPT §5) | ссылки на источник для каждого пункта | частично: см. раздел 7 | публикационные факты (решение OWNER R128, Q1) | Canonical state, внешние наблюдения публикации | canonical для фактов, external для публикации | факты — по committed revision; публикация — по `observed_at` | read channel | нет | устаревшее или недоступное наблюдение помечать как устаревшее с `observed_at`; нет источника — UNKNOWN, не «зелёный» | вкладки в дереве нет; состав полей — раздел 7.2 | ADR-0006 для наблюдений; решение OWNER R128 (Q1) |
| Explain (CONCEPT §6) | ближний блокер, кто действует, «не является причиной» | нет — TM-12 не реализована, структуры blocker не существует | TM-12, структурированный blocker, спецификация порядка | StagePlan, Runs, Results, blockers | derived | при чтении snapshot | read channel | нет | неизвестное значение показывать как UNKNOWN с пометкой «источник не найден», не заменять предположением | реализации нет: TM-12 не реализована, структуры blocker нет, спецификации порядка нет | ADR-0006 и read API |
| Changes (CONCEPT §7) | разница с последней точкой | нет — в журнале только четыре типа событий (раздел 5) | разница с последней точкой; решение OWNER R129 (Q2) | Domain Events + cursor | canonical для событий, client-local для cursor | события — по `EventSeq`; устаревший cursor показывать как устаревший | read channel | нет | устаревший cursor показывать как устаревшее с указанием `observed_at`; типы событий, которых нет в журнале, — UNKNOWN, а не «событий не было» | реализации нет: журнал ограничен четырьмя типами, cursor не хранится | решение OWNER R129 (Q2): cursor локальный, client-local, без изменений `CanonicalState` |
| Publication (CONCEPT §8) | внешние наблюдения с источником и временем | частично: git и CI наблюдаемы (`.github/workflows/ci.yml`), в Coord не хранятся | решение OWNER R128 (Q1): факты публикации — внешняя read-only проекция, в `CanonicalState` не входят | Git, CI, deployment | external | по `observed_at` наблюдения | внешний наблюдатель | нет | устаревшее наблюдение — помечать устаревшим с `observed_at`; отсутствие наблюдения — UNKNOWN, не успех | наблюдателя в Coord нет; внешние источники (Git, CI) вне канонического состояния | ADR-0006 для read API; решение OWNER R128 (Q1) по фактам публикации |
| System (CONCEPT §2) | готовность, ownership, schema, очереди | частично: readiness и ownership есть; pending Outbox вычислим, источника для «Inbox pending» нет — `Inbox` хранит обработанные receipt | read API, AgentSession registry в UI | runtime coordd, canonical state | derived; canonical для schema и очередей | при чтении; not-ready — с причиной | read channel | нет | неизвестное значение показывать как UNKNOWN с пометкой «источник не найден», не заменять предположением | частично: `/readyz` (`TestDaemonReadinessLifecycle`) и ownership (`TestCoorddSecondProcessCannotOpenOwnedNamespace`); блока System в дереве нет | ADR-0006 и read API |
| Первый запуск (CONCEPT §10) | init → discover → start | нет | ADR-0006, OwnerCredential, приём COORDINATOR | локальная конфигурация, coordd, OwnerCredential | client-local + external (transport) | локальная конфигурация; transport — по наблюдению | локальный CLI; OWNER identity — только через OwnerCredential | нет | недоступный daemon или отсутствующая привязка — UNKNOWN с причиной, не «зелёный» статус | реализации нет: команд `init`, `discover`, `start` в дереве нет | ADR-0006, `OwnerCredential` и приём COORDINATOR (CM-04) |

## 4. Источники данных каждого элемента

Классы источников по OWNER-3: `Canonical` — факт в canonical state одним durable
commit; `derived` — детерминированная проекция; `external` — наблюдение из Git, CI,
deployment или transport; `client-local` — настройка или cursor интерфейса;
`unknown` — нет нормативного источника. Ни `derived`, ни `external`,
ни `client-local`, ни `unknown` не участвуют в переходах Task.

### 4.1 Оболочка

| UI element | Target | Current | Gap | Source | Data class | Freshness | Trust boundary | Affects Task transitions | UNKNOWN / stale behavior | Current evidence | Required dependency |
|---|---|---|---|---|---|---|---|---|---|---|---|
| Название проекта | показывать активный проект из подключения | нет project boundary в коде | модель проекта (ADR-0002 — граф, не имя) | конфигурация подключения, будущая project model | client-local / unknown | локальная конфигурация | нет | нет | N/A — значение клиентское; staleness внешних данных UI не проверяет | реализация не существует, прямой тест не найден | N/A — значение клиентское |
| Этап P0-x | этап из проекции проекта | документальный факт, в коде нет | project projection | ROADMAP.md, позднее project projection | derived | документ | нет | нет | неизвестное значение показывать как UNKNOWN с пометкой «источник не найден», не заменять предположением | реализация не существует, прямой тест не найден | ADR-0006 и read API |
| Состояние daemon | READY/NOT_READY с причиной not-ready | частично: `/readyz` покрывает operational readiness Run coordinator, полный RT-03 не реализован (TARGET-ARCHITECTURE) | RT-03, read API | readiness coordd, RT-03 | derived из runtime | при каждом чтении | read channel | косвенно: daemon закрывает мутации | неизвестное значение показывать как UNKNOWN с пометкой «источник не найден», не заменять предположением | `TestDaemonReadinessLifecycle` (daemon/daemon_test.go:82) проверяет gate `/readyz`: 503 до разблокировки Receive и 200 после — lifecycle готовности, не полный RT-03 | ADR-0006 и read API |
| Revision (строка главного экрана) | revision согласованного OwnerView | существующие версии: `Task.Revision` (per-Task), `CanonicalState.SchemaVersion`, git SHA как внешнее наблюдение; общей canonical revision нет | общей revision нет: consistency token read-модели должен определить ADR-0006 | `Task.Revision` (per-Task), `CanonicalState.SchemaVersion` (`store/canonical.go:20,69`) | canonical (Task.Revision и schema version); общей canonical revision нет | на чтение snapshot | read channel | нет для строки экрана; CAS по `expectedRevision` относится к Task.Revision в команде, а не к отображению | отсутствующая consistency token read-модели — UNKNOWN, не показывать как 0 или предыдущее значение | для самой проекции прямого теста не найден; тест в Current доказывает инвариант хранилища, а не проекцию | ADR-0006 и read API |
| Время обновления | `ComputedAt` | нет — read API отсутствует | ADR-0006 | ответ read API | derived | время вычисления ответа | нет | нет | неизвестное значение показывать как UNKNOWN с пометкой «источник не найден», не заменять предположением | реализации нет: read API отсутствует, искал в `cmd/coordd/main.go` и `daemon/` — DTO с `ComputedAt` нет | ADR-0006: `ComputedAt` в read-модели |
| Профиль OWNER | показывать активный профиль | `OwnerCredential` не реализован | агрегат и verifier | локальная конфигурация, `OwnerCredential` | client-local + security reference | локально | command channel | только через command channel | N/A — значение клиентское; staleness внешних данных UI не проверяет | реализации нет: `OwnerCredential` в `store/canonical.go` не встречается, тестов нет | ADR-0006 и реализация `OwnerCredential` (CM-12) |
| Поиск, фильтр, тема | фильтрация и оформление | нет | UI-слоя в дереве нет | локальный индекс и настройки | client-local | локально | нет | нет | N/A — UI-слоя нет | реализации нет: UI-слоя в дереве вообще нет, искал в `internal/` и `cmd/` — TUI/CLI Owner Console отсутствует | TUI Owner Console (ADR-0006) и UI-слой |

### 4.2 Сводные карточки

| UI element | Target | Current | Gap | Source | Data class | Freshness | Trust boundary | Affects Task transitions | UNKNOWN / stale behavior | Current evidence | Required dependency |
|---|---|---|---|---|---|---|---|---|---|---|---|
| Всего задач | число Task в области | есть данные, нет проекции | read API | Tasks | derived | при чтении snapshot | read channel | нет | неизвестное значение показывать как UNKNOWN с пометкой «источник не найден», не заменять предположением | реализация не существует, прямой тест не найден | ADR-0006 и read API |
| В работе | активная стадия или Run | данные есть | read API | Task, StagePlan, Runs | derived | при чтении snapshot | read channel | нет | неизвестное значение показывать как UNKNOWN с пометкой «источник не найден», не заменять предположением | проекции счётчика нет; данные есть (`TestCanonicalStateValidatesTaskEventAndRunRelations`) | ADR-0006 и read API |
| На проверке | ожидают Review или Audit | `ReviewResult`/`AuditResult` в canonical state есть (RA-1) | различение Review и Audit в проекции | Stage, Runs, Review/Audit | derived | при чтении snapshot | read channel | нет | неизвестное значение показывать как UNKNOWN с пометкой «источник не найден», не заменять предположением | результаты есть (`TestReviewResultCommitsWithTransitionEvent`), проекции «на проверке» нет | ADR-0006 и read API |
| Требуют решения | открытые OWNER Attention | нет: Attention projection не реализована | TM-11/TM-12 | Task blocker, Attention projection | derived | при чтении snapshot; неизвестный blocker не даёт карточки | read channel | нет | неизвестное значение показывать как UNKNOWN с пометкой «источник не найден», не заменять предположением | реализации нет: Attention projection отсутствует | ADR-0006 и read API |
| Заблокированы | следующий переход невозможен | `Status=BLOCKED` есть, structured blocker — нет | TM-12, структурированный blocker | Status, blockers, readiness | derived | при чтении snapshot | read channel | нет | неизвестное значение показывать как UNKNOWN с пометкой «источник не найден», не заменять предположением | `Status=BLOCKED` валидируется (`TestTaskValidationSeparatesStageAndStatus`), структурированного blocker нет | ADR-0006 и read API |
| Готовы к commit | все обязательные gates выполнены | не реализовано: в Coord нет ни commit path, ни проекции готовности; нормы продукта о готовности к commit нет: ADR-0008 (Proposed) — единственная норма о commit-модели Coord; правило работы координатора-разработчика вынесено в раздел 12.1 | проекция готовности (ADR-0006); для автокоммита — принятие ADR-0008 | Run, Review, Audit, tree, gates | derived | при чтении snapshot | read channel | нет | неизвестное значение показывать как UNKNOWN с пометкой «источник не найден», не заменять предположением | реализация не существует, прямой тест не найден | ADR-0006 и read API |
| Локально зафиксированы | наблюдается локальный commit | git наблюдаем извне, в Coord не хранится | решение OWNER R128 (Q1): внешняя read-only проекция | Git observation | external | по `observed_at` | внешний наблюдатель | нет | нет наблюдения — UNKNOWN, не «не публиковано» | наблюдателя в Coord нет; git как внешний источник вне канонического состояния | решение OWNER R128 (Q1) |
| Ожидают публикации | commit есть, push не подтверждён | наблюдаемо | решение OWNER R128 (Q1): внешняя read-only проекция | Git local/remote | external | по `observed_at` | внешний наблюдатель | нет | нет наблюдения push — UNKNOWN, а не ошибка публикации | наблюдателя в Coord нет: git-факты нигде не собираются; внешняя сторона (репозиторий, CI) вне кода coord | решение OWNER R128 (Q1) |
| CI выполняется | CI сообщает активный run | `.github/workflows/ci.yml` выполняет `make check` на ubuntu-latest и macos-latest (commit `6eb78e3`) | решение OWNER R128 (Q1): внешняя read-only проекция | CI provider | external | по `observed_at` | внешний наблюдатель | нет | нет данных CI — UNKNOWN с источником и временем наблюдения | наблюдателя в Coord нет; файл `.github/workflows/ci.yml` — конфигурация CI, а не чтение его статуса | решение OWNER R128 (Q1) |
| Готовы к merge | push и gates подтверждены | нет | решение OWNER R128 (Q1): внешняя read-only проекция | Git + CI | derived из external | по `observed_at` наблюдений | внешний наблюдатель | нет | нет наблюдения CI или push — UNKNOWN | реализации нет: ни наблюдателя, ни проекции | решение OWNER R128 (Q1) |

Расхождение с вводной OWNER-3: в строке «Приоритет» и «Прогресс %» вводная
правильно относит их к `unknown`; в коде `Task` таких полей нет (проверено по
`task/model.go:73-86`), прогресс существует только у Run (`fsm/fsm.go:52
ProgressPct`) и заполняется из heartbeat, поэтому как «прогресс задачи» он не
каноничен.

### 4.3 Tasks: колонки списка

| UI element | Target | Current | Gap | Source | Data class | Freshness | Trust boundary | Affects Task transitions | UNKNOWN / stale behavior | Current evidence | Required dependency |
|---|---|---|---|---|---|---|---|---|---|---|---|
| Task ID | стабильный ID | есть: `TestCreateTaskGeneratesCanonicalTaskID` (выдача канонического ID) и `TestTaskIDCollisionRetriesBeforeCommit` (повтор при коллизии) | — | `Task.ID` (`taskid.ID`) | canonical | текущий | read channel | да — входит в Task-scoped capability (TM-24) | неизвестное значение показывать как UNKNOWN с пометкой «источник не найден», не заменять предположением | для самой проекции прямого теста не найден; тест в Current доказывает инвариант хранилища, а не проекцию | ADR-0006 и read API |
| Название | не генерировать при чтении | поле существует (`task/model.go`); прямой тест на Title не найден — реализация существует, доказательство не выделено | добавить тест или mutation test | `Task.Title` | canonical | текущий | read channel | нет | неизвестное значение показывать как UNKNOWN с пометкой «источник не найден», не заменять предположением | поля `Title` в `task/model.go`; искал в `task/model_test.go` и по слову Title в `*_test.go` — прямого теста, падающего при поломке Title, нет | тест или mutation test на сохранение поля |
| Цель/описание | показывать без новых фактов | поле существует (`task/model.go`); прямой тест не найден — реализация существует, доказательство не выделено | добавить тест или mutation test | `Task.Description` | canonical | текущий | read channel | нет | неизвестное значение показывать как UNKNOWN с пометкой «источник не найден», не заменять предположением | поля `Description` в `task/model.go`; искал в `task/model_test.go` и по слову Description в `*_test.go` — прямого теста нет | тест или mutation test на сохранение поля |
| Понятное состояние | Owner-facing | данных достаточно | read API | Task, Stage, Runs, blockers | derived | при чтении snapshot | read channel | нет | неизвестное значение показывать как UNKNOWN с пометкой «источник не найден», не заменять предположением | данные для проекции есть (`TestCanonicalStateValidatesTaskEventAndRunRelations`), но самой проекции и её теста в дереве нет | ADR-0006 и read API |
| Current Stage | отдельно от статуса | есть: `OwnerDecision Stage = "OWNER_DECISION"` в `task/model.go:17` | — | StagePlan, `Task.CurrentStage` | canonical | текущий | read channel | да — Stage определяет допустимый переход | неизвестное значение показывать как UNKNOWN с пометкой «источник не найден», не заменять предположением | `TestTaskValidationSeparatesStageAndStatus` и `TestTaskValidationRejectsInvalidPlanAndTerminalStage` (task/model_test.go): проверяют, что Stage и Status валидируются раздельно и терминальный Stage отклоняется | ADR-0006 и read API |
| Исполнитель | identity не из имени сессии | есть для Task-scoped Run: `Run.RoleSelection.AgentID` | обязательность `AgentID` для всех ролей — по R116 норма есть, реализация расширяется | активный Run и `AgentID` | canonical | текущий | read channel | да — actor берётся из persisted Run (CM-06) | неизвестное значение показывать как UNKNOWN с пометкой «источник не найден», не заменять предположением | `TestCanonicalRoleIndependenceFailsClosedWithoutStableAgentID` (store/canonical_independence_test.go): пустой `AgentID` у Review/Audit Run отклоняется на startup | ADR-0006 и read API |
| Роль | последняя запись | есть | — | `Run.RoleHistory` | canonical | текущий | read channel | да — роль определяет допустимые переходы | неизвестное значение показывать как UNKNOWN с пометкой «источник не найден», не заменять предположением | `TestCanonicalTransitionRoleHistoryIsAppendOnly` (store/canonical_test.go): изменение RoleHistory отклоняется; `TestRoleHistoryRejectsRoleChange` — смена роли в истории | ADR-0006 и read API |
| Следующий шаг | не прогноз | StagePlan есть | спецификация проекции | первый непройденный gate | derived | при чтении snapshot | read channel | нет | неизвестное значение показывать как UNKNOWN с пометкой «источник не найден», не заменять предположением | StagePlan валидируется (`TestTaskValidationRejectsInvalidPlanAndTerminalStage`); вычисление «следующего шага» и его теста в дереве нет | ADR-0006 и read API |
| Блокер | не свободный текст | **Gap**: структурированного blocker у Task нет; строковый `Run.Blocker` (`fsm/fsm.go:53`) приходит из heartbeat | структура blocker в норме | structured blocker | canonical / будущее | текущий; при неизвестном blocker — UNKNOWN, не догадка | read channel | нет — UI не влияет на переходы | неизвестная причина блокировки — UNKNOWN с пометкой «источник не найден», не «нет блокера» | реализация не существует, прямой тест не найден | ADR-0006 и read API |
| Последнее изменение | по committed revision | есть, но журнал ограничен | типы событий (раздел 5) | последний Task-scoped Event | canonical | по committed revision | read channel | нет | неизвестное значение показывать как UNKNOWN с пометкой «источник не найден», не заменять предположением | журнал и `EventID` подтверждены (`TestCanonicalStateRejectsEventSequenceGapAfterFirstEvent`), но проекция «последнего изменения» и её тест в дереве нет | ADR-0006 и read API |
| Приоритет, прогресс % | не показывать | нет в `Task` | правило приоритета не принято | — | unknown | — | нет | нет | UNKNOWN — нормативный источник не определён; не показывать | реализация не существует, прямой тест не найден | ADR-0006 и read API |

### 4.4 «Работа»

| UI element | Target | Current | Gap | Source | Data class | Freshness | Trust boundary | Affects Task transitions | UNKNOWN / stale behavior | Current evidence | Required dependency |
|---|---|---|---|---|---|---|---|---|---|---|---|
| Run ID | устойчивый | есть | — | `Run.RunID` | canonical | текущий | read channel | да — селектор Task-scoped команды | неизвестное значение показывать как UNKNOWN с пометкой «источник не найден», не заменять предположением | реализация существует, прямой тест не найден | ADR-0006 и read API |
| Роль | не из prompt | есть | — | `Run.RoleSelection`, `RoleHistory` | canonical | текущий | read channel | да | неизвестное значение показывать как UNKNOWN с пометкой «источник не найден», не заменять предположением | `TestCanonicalTransitionRoleHistoryIsAppendOnly` (store/canonical_test.go): изменение RoleHistory отклоняется; `TestRoleHistoryRejectsRoleChange` — смена роли в истории | ADR-0006 и read API |
| AgentID | обязателен для command-capable Run | есть (проверка `TestCanonicalRoleIndependenceFailsClosedWithoutStableAgentID` требует непустой `AgentID`) | command boundary | `Run.RoleSelection.AgentID` | canonical | текущий | read channel; входная команда — command channel | да — actor берётся из persisted Run | неизвестное значение показывать как UNKNOWN с пометкой «источник не найден», не заменять предположением | `TestCanonicalRoleIndependenceFailsClosedWithoutStableAgentID` (store/canonical_independence_test.go): пустой `AgentID` отклоняется на startup | ADR-0006 и read API |
| Session address | не `AgentID` | есть: привязка `AgentSession` в Run (`store/canonical_agentsession_binding.go`) | — | `Run.OwnerSession` + `AgentSession` | canonical | текущий | read channel; сверка — command channel | да — привязка Run к привязке | неизвестное значение показывать как UNKNOWN с пометкой «источник не найден», не заменять предположением | `TestCanonicalStartupRequiresGenerationAndBindingForActiveTaskRun` (store/canonical_agentsession_binding_test.go): Run без привязки отклоняется на startup | ADR-0006 и read API |
| Session generation | проверяется до replay | есть (часть 3 AgentSession, `TestCanonicalAssignmentTakesCurrentActiveAgentSessionBinding`) | проверка в командах — часть 4 | `Run.AgentSessionGeneration` | canonical | текущий | command channel при сверке | да — сверка поколения | неизвестное значение показывать как UNKNOWN с пометкой «источник не найден», не заменять предположением | `TestCanonicalStartupRequiresGenerationAndBindingForActiveTaskRun` и `TestCanonicalRunGenerationCannotChangeWithoutRecoveryTransfer` (store/canonical_agentsession_binding_test.go) | ADR-0006 и read API |
| Attempt | не generation | есть | — | `Run.Attempt` | canonical | текущий | command channel при сверке | да — сверка попытки | неизвестное значение показывать как UNKNOWN с пометкой «источник не найден», не заменять предположением | `TestCanonicalTransitionRetryPreservesAttemptAndTaskFence` (store/canonical_test.go): изменение `Attempt` отклоняется как «Attempt is immutable» | ADR-0006 и read API |
| Назначен | ссылка на событие | **Gap**: события «назначение Run» в журнале нет | тип события | assignment event | canonical | по событию | read channel | нет | неизвестное значение показывать как UNKNOWN с пометкой «источник не найден», не заменять предположением | реализации нет: события «назначение Run» в типе `DomainEvent` нет | тип события назначения Run в норме журнала |
| Последняя активность | не transport activity | `InboxReceipt.RecordedAt` есть | проекция | последняя принятая команда | canonical | по committed receipt | read channel | нет | неизвестное значение показывать как UNKNOWN с пометкой «источник не найден», не заменять предположением | `TestApplyInboxCommandRejectsStaleExpectedRevision` и другие проверки `ApplyInboxCommand` формируют `InboxReceipt` с `RecordedAt` (store/canonical_test.go:99,128,145); отдельного теста только на `RecordedAt` нет | ADR-0006 и read API |
| Provider availability | UNKNOWN по умолчанию | нет | ADR-0006 для наблюдений; норма наблюдения провайдера не определена | supervisor/OWNER | external / unknown | по наблюдению; без наблюдения — UNKNOWN | внешний наблюдатель | нет | без наблюдения — UNKNOWN по умолчанию, не «доступен» | реализации нет: наблюдения провайдера в canonical state нет | ADR-0006 для наблюдений; норма наблюдения провайдера не определена |
| Result | только после принятия Coord | есть для Review/Audit (RA-1); Executor Result command нет (R117, RT-04) | Executor result path | `task.ReviewResult`/`AuditResult` | canonical | по committed result | read channel | да — принятый результат Coord переводит Task | неизвестное значение показывать как UNKNOWN с пометкой «источник не найден», не заменять предположением | `TestReviewResultCommitsWithTransitionEvent` и `TestAuditResultCommitsWithTransitionEvent` (store/canonical_result_test.go): результат фиксируется одним commit с событием перехода | ADR-0006 и read API |
| Tree hash | не из текущего worktree; `Run.TargetSHA` — цель назначения, а не хеш результата | Gap: в `ResultPayload` (`envelope/envelope.go:78-82`) и `ReviewResult`/`AuditResult` (`task/result.go`) поля tree hash нет; `TargetSHA` попадает в `RESULT_ACK` (`store/canonical_result.go:185`), что подтверждает только цель назначения | поле tree hash в результате либо норма об его источнике | хеш дерева принятого результата, ревью или аудита | canonical / будущее | на момент результата | read channel | нет | неизвестное значение показывать как UNKNOWN с пометкой «источник не найден», не заменять предположением | реализация не существует, прямой тест не найден | ADR-0006 и read API |
| Независимость роли | не OS isolation | есть: `TestReviewerRejectsExecutorAgentIDAcrossHistory` | — | `AgentID` против истории Executor | derived из canonical | при чтении snapshot | read channel | нет | неизвестное значение показывать как UNKNOWN с пометкой «источник не найден», не заменять предположением | `TestReviewerRejectsExecutorAgentIDAcrossHistory` и `TestAuditorRejectsExecutorAgentIDAcrossTaskRuns` (store/canonical_independence_test.go) | ADR-0006 и read API |

## 5. Канонические факты и вычисляемые проекции

Каноническое сейчас (проверено по коду):

- `CanonicalState`: Tasks, Runs, Events, Inbox, Outbox, AgentSessions,
  AgentSessionEvents (`store/canonical.go:68-77`); проверки
  `TestCanonicalStateValidatesTaskEventAndRunRelations`,
  `TestCanonicalStoreRejectsTaskMutationWithoutEvent`.
- `Task`: ID, Title, Description, OwnerID, Revision, LastEventSeq, StagePlan,
  CurrentStage, Status, Fence, CreatedAt, UpdatedAt (`task/model.go:73-86`).
- `Run`: TaskID, RunID, Attempt, OwnerSession, AgentSessionGeneration, Fence,
  Status, RoleSelection, RoleHistory, Result, TargetSHA, ProgressPct, Blocker и
  другие (`fsm/fsm.go:39-72`).
- `AgentSession` проекция и append-only история (`agentsession/session.go`).
- Журнал `DomainEvent` содержит ровно четыре типа: `TASK_CREATED`,
  `TASK_OWNERSHIP_TRANSFERRED`, `REVIEW_RESULT_RECORDED`,
  `AUDIT_RESULT_RECORDED` (`task/event.go:13-16`).

Вычисляемое (не хранится): счётчики, Attention, «кто должен действовать»,
«почему остановилась», готовность к commit, сводка изменений, OwnerView, Task
Brief, pending Outbox, readiness daemon.

О Inbox: `CanonicalState.Inbox` — durable-карта обработанных команд (`InboxReceipt`
с `ActorID`, `MessageID`, `TaskID`, `RunID`, `EventIDs`, `ExpectedRevision`,
`CommittedRevision`, `RecordedAt`; `store/canonical.go:42-50`). Очереди
необработанных входящих команд в каноническом состоянии нет, поэтому метрика
«Inbox pending» источника не имеет. Подтверждаемая величина — «Inbox receipts: N»,
но её полезность для OWNER не обоснована; вероятное место — техническая диагностика,
а не главный экран. ROADMAP требует того же: «Attention и
Health вычисляются, не хранятся».

Внешнее наблюдение (не каноническое): push, CI, merge, deploy, release, branch,
remote; Telepathy reachability; provider availability.

Клиентское: cursor просмотра, фильтры, тема, сортировка, сохранённый вид.

Расхождение с вводной OWNER-3 §9: вводная перечисляет «Назначение Run», «Переход
Task», «Решение OWNER», «Session отозвана» как события журнала. По коду таких типов
событий нет — см. перечень выше. Это Gap, а не факт.

## 6. Attention и coord decide

Целевая модель Attention: производная от `Status=BLOCKED` с причиной, основанием и
разрешёнными вариантами (TASK-MODEL TM-11), при этом `BLOCKED` сохраняет текущий
Stage (TM-12); блокировка по зависимости называет prerequisite и его Status
(ADR-0002 DG-07).

| UI element | Target | Current | Gap | Source | Data class | Freshness | Trust boundary | Affects Task transitions | UNKNOWN / stale behavior | Current evidence | Required dependency |
|---|---|---|---|---|---|---|---|---|---|---|---|
| ID вопроса | устойчивый ID | TaskID есть | AttentionID | TaskID или будущий AttentionID | canonical / будущее | текущий | read channel | нет | неизвестное значение показывать как UNKNOWN с пометкой «источник не найден», не заменять предположением | реализация существует, прямой тест не найден | ADR-0006 и read API |
| Тип | OWNER_DECISION, OWNER_REQUIRED, приёмка координатора | `OWNER_DECISION` и `RequireOwnerDecision` в StagePlan есть (`task/model.go:17,36`) | приёмка координатора как тип | Stage, blocker reason | derived | при чтении snapshot | read channel | нет | неизвестное значение показывать как UNKNOWN с пометкой «источник не найден», не заменять предположением | `TestTaskValidationSeparatesStageAndStatus` и `TestTaskValidationRejectsInvalidPlanAndTerminalStage` (task/model_test.go) | ADR-0006 и модель Attention |
| Причина | почему продолжение невозможно | нет структурированного blocker | структура blocker | blocker | derived | при чтении snapshot; неизвестна — UNKNOWN | read channel | нет | неизвестное значение показывать как UNKNOWN с пометкой «источник не найден», не заменять предположением | реализации нет: структурированного blocker нет | ADR-0006 и read API |
| Основание | ссылки на источник | ссылки возможны на Task и Events | контракт ссылок | Task, ADR, события | derived | при чтении snapshot | read channel | нет | неизвестное значение показывать как UNKNOWN с пометкой «источник не найден», не заменять предположением | ссылки на Task и Events есть (`TestCanonicalStateValidatesTaskEventAndRunRelations`), контракт ссылок Attention не реализован | контракт ссылок в ADR-0006 |
| Что блокируется | dependent Task и Stage | графа в коде нет | ADR-0002 | граф зависимостей | derived | при чтении snapshot | read channel | нет | неизвестное значение показывать как UNKNOWN с пометкой «источник не найден», не заменять предположением | реализации нет: Project Graph в коде отсутствует | ADR-0006 и read API |
| Варианты A/B/C | фиксированный набор вариантов | нет | сущность запроса решения | OWNER decision request | canonical до использования | при записи решения | command channel | да — по варианту Coord выбирает переход | неизвестное значение показывать как UNKNOWN с пометкой «источник не найден», не заменять предположением | реализация не существует, прямой тест не найден | ADR-0006 и read API |
| Последствия | без прогнозов без основания | нет | спецификация | граф, структурированный анализ | derived | при чтении snapshot | read channel | нет | неизвестное значение показывать как UNKNOWN с пометкой «источник не найден», не заменять предположением | реализация не существует, прямой тест не найден | ADR-0006 и read API |
| Рекомендация | не применяется автоматически | нет | формат рекомендации | выбор координатора и основание | derived | при чтении snapshot | read channel | нет | неизвестное значение показывать как UNKNOWN с пометкой «источник не найден», не заменять предположением | реализация не существует, прямой тест не найден | ADR-0006 и read API |
| Время создания | время события | для Attention события нет | тип события | Domain Event | canonical | по событию | read channel | нет | неизвестное значение показывать как UNKNOWN с пометкой «источник не найден», не заменять предположением | реализация не существует, прямой тест не найден | ADR-0006 и read API |
| «Принять A» | подписанный challenge одной команды | нет | OwnerCredential, decide, схема события (Gap P0-3); решение OWNER R131 (Q4): команда подаётся из CLI, web только показывает точную CLI-команду и данные challenge | команда OWNER | mutation | при записи | command channel | да | неизвестное значение показывать как UNKNOWN с пометкой «источник не найден», не заменять предположением | реализация не существует, прямой тест не найден | решение OWNER R131 (Q4) и Gap P0-3 (OwnerCredential, decide, схема события) |
| «Другой вариант» | текст не подменяет identity | нет | то же; решение OWNER R131 (Q4): также только CLI | команда OWNER | mutation | при записи | command channel | да | неизвестное значение показывать как UNKNOWN с пометкой «источник не найден», не заменять предположением | реализация не существует, прямой тест не найден | решение OWNER R131 (Q4) |
| «Отложить» | по умолчанию скрывает локально | нет | норма об откладывании | UI или отдельная команда | client-local либо canonical | локально | нет — пока только UI | зависит от нормы | N/A — значение клиентское; staleness внешних данных UI не проверяет | реализация не существует, прямой тест не найден | N/A — значение клиентское |

Граница записи: Attention read model → выбор в TUI → подписанный challenge одной
команды → `OwnerCredential` → проверка актуальности Task и revision → durable
OWNER decision. Кнопка не пишет в storage сама.

Расхождение с вводной OWNER-3 §3: вводная относит «Решение OWNER» к событию
журнала. Типа события решения OWNER нет ни в коде (`task/event.go:13-16`), ни в
норме; `OWNER_DECISION` в TASK-MODEL — это Stage, а не тип `DomainEvent`. Это Gap и
зависимость проекта границы команд P0-3, не вопрос OWNER (см. раздел 12; решение
OWNER R130, Q3 — про побочные эффекты решения, а не про имя события).

## 7. Explain и доказательства

### 7.1 Explain

Порядок объяснения строится по структуре событий и блокеров, а не по свободному
тексту модели (CONCEPT §6): первый непройденный gate, кто должен действовать,
последний подтверждённый факт, что уже выполнено, что не является причиной.

Target: детерминированная проекция; порядок задаёт спецификация проекции, не UI.

Current: нет — TM-12 («BLOCKED сохраняет текущий Stage») не реализована,
структурированного blocker нет, журнал ограничен четырьмя типами. Проверка: «нет».

Gap: TM-12, структура blocker, спецификация порядка объяснения, покрытие
альтернативных причин (transport, AgentSession, решение OWNER, provider blocker) —
для этого нужны наблюдения, часть которых внешняя.

### 7.2 Доказательства: что есть в коде и чего нет

| UI element | Target | Current | Gap | Source | Data class | Freshness | Trust boundary | Affects Task transitions | UNKNOWN / stale behavior | Current evidence | Required dependency |
|---|---|---|---|---|---|---|---|---|---|---|---|
| TaskID | идентификатор Task для корреляции | `Task.ID` | canonical | текущий | read channel | да — входит в Task-scoped capability (TM-24) | есть | — | отсутствующий TaskID — UNKNOWN | `TestCanonicalStateValidatesTaskEventAndRunRelations` (store/canonical_test.go): ссылка Run на существующий Task проверяется | ADR-0006 для показа |
| Task revision | ревизия Task для CAS и трассировки | `Task.Revision` | canonical | текущий | command channel при CAS | да | есть | общей revision нет (см. 4.1) | устаревшая revision при команде — отказ с причиной | `TestApplyInboxCommandRejectsStaleExpectedRevision` (store/canonical_test.go): отклоняет устаревшую revision без durable write | ADR-0006 для consistency token |
| Task.Fence | эпоха владения Task | `Task.Fence` | canonical | текущий | command channel при сверке эпохи | да | есть | — | снижение Fence — отказ | подтверждения монотонности Fence и запрета её изменения вне transfer ищу в `store/canonical_test.go` и `store/canonical_fence_test.go`: ближайшее найденное подтверждение — `TestCanonicalTransitionRetryPreservesAttemptAndTaskFence` | — |
| RunID | идентификатор Run для корреляции | `Run.RunID` | canonical | текущий | command channel | да | есть | — | RunID без существующей записи — UNKNOWN | `TestCanonicalStateValidatesTaskEventAndRunRelations`: расхождение ключа и `Run.RunID` отклоняется | ADR-0006 для показа |
| Attempt | номер попытки | `Run.Attempt` | canonical | текущий | command channel при сверке | да | есть | — | изменение вне transfer — отказ | `TestCanonicalTransitionRetryPreservesAttemptAndTaskFence` (store/canonical_test.go): изменение `Attempt` отклоняется как «Attempt is immutable» | — |
| Run.Fence | эпоха Run | `Run.Fence` | canonical | текущий | command channel при сверке эпохи | да | есть | — | Fence вне recovery transfer — отказ | подтверждения правила «Fence меняется только при recovery transfer» ищу в `store/canonical_test.go` и `store/canonical_agentsession_binding_test.go`: ближайшее найденное — `TestCanonicalRecoveryTransferChangesAgentWithOwnerAndHistory` | — |
| AgentID | устойчивая identity исполнителя | `Run.RoleSelection.AgentID` | canonical | текущий | command channel | да — actor берётся из persisted Run | есть | обязательность для всех ролей по R116 расширяется | пустой AgentID — fail-closed | `TestCanonicalRoleIndependenceFailsClosedWithoutStableAgentID` (store/canonical_independence_test.go) | — |
| Session address | адрес привязки, не AgentID | `AgentSession`, `Run.OwnerSession` | canonical | текущий | command channel | да | есть | — | неизвестный адрес — отказ, не пустой | `TestCanonicalStartupRequiresGenerationAndBindingForActiveTaskRun` (store/canonical_agentsession_binding_test.go) | — |
| Session generation | поколение привязки | `AgentSession.Generation`, `Run.AgentSessionGeneration` | canonical | текущий | command channel | да | есть | проверка в командах — часть 4 | поколение выше привязки — отказ | `TestCanonicalStartupRequiresGenerationAndBindingForActiveTaskRun`, `TestCanonicalRunGenerationCannotChangeWithoutRecoveryTransfer` (store/canonical_agentsession_binding_test.go) | часть 4 AgentSession |
| Key fingerprint | идентификатор ключа без раскрытия | производное от `AgentSession.PublicKey` | derived (вычисляемо) | пересчёт при каждом чтении | read channel; не является credential | нет | вычисляемо: `PublicKey ed25519.PublicKey` в привязке (`agentsession/session.go:28-37`), SHA-256 считается на лету | формат не стандартизован | ключ не загружен — поле не показывать, а не пустое значение | `TestCanonicalAgentSessionRegistrationPersistsOperatorBinding` (store/canonical_agentsession_test.go) подтверждает хранение ключа; вычисление fingerprint тестом не покрыто | норма о формате, если станет interop-контрактом |
| CommandID | идентификатор команды для дедупликации | `InboxReceipt.MessageID` | canonical | по committed receipt | read channel | нет — реплика, не новый факт | есть: `MessageID` в `InboxReceipt` (`store/canonical.go:42-50`) | в схеме концепта называется CommandID — это тот же идентификатор | повтор команды — возврат прежнего receipt | `TestApplyInboxCommandRejectsStaleExpectedRevision` и другие вызовы `ApplyInboxCommand` (store/canonical_test.go:99,128,145) | — |
| Inbox receipt | доказательство принятия команды | canonical Inbox | canonical | по committed receipt | read channel | нет | есть | — | receipt для неизвестной команды — не показывать | вызовы `ApplyInboxCommand` в store/canonical_test.go формируют receipt; отдельного теста только на Inbox нет | ADR-0006 для показа |
| EventID | ссылка на событие журнала | Domain Event | canonical | по событию | read channel | нет | есть | журнал ограничен четырьмя типами | EventID без события — UNKNOWN | `TestCanonicalStateRejectsEventSequenceGapAfterFirstEvent` (store/canonical_test.go): разрыв журнала отклоняется | типы событий — по нормам |
| Outbox record | состояние доставки Run | canonical Outbox | canonical | при доставке | read channel | нет | есть: `Envelope`, `EventID` (`store/canonical.go:27-38`) | pending вычислим, но не хранится как метрика | пустая очередь — «нет pending», не ошибка | `TestCanonicalTransitionCommitsStateEventOutboxAtomically` (checkpoint/canonical_test.go) | ADR-0006 для показа |
| ReviewResult / AuditResult | принятый вердикт с findings | canonical | canonical | по committed result | read channel | да — результат переводит Task | есть (RA-1) | Executor Result command нет (R117, RT-04) | вердикт без durable commit — не показывать | `TestReviewResultCommitsWithTransitionEvent`, `TestAuditResultCommitsWithTransitionEvent` (store/canonical_result_test.go) | путь Executor RESULT |
| Source tree hash | хеш дерева принятого результата | Result/Review/Audit | canonical / будущее | на момент результата | read channel | нет | Gap: поля tree hash нет ни в `ResultPayload` (`envelope/envelope.go:78-82`), ни в `ReviewResult`/`AuditResult` (`task/result.go`); `Run.TargetSHA` — цель назначения и попадает в `RESULT_ACK` (`store/canonical_result.go:185`) | цель назначения не заменяет хеш результата | нет поля — не показывать, а не показывать `TargetSHA` как хеш результата | прямого теста нет: искал в `store/canonical_result_test.go` и `executor/` | поле tree hash в результате либо норма об источнике |
| Commit SHA, push, CI, merge, deploy | доказательства публикации | Git, CI, deployment | external | по `observed_at` | внешний наблюдатель | нет | внешние наблюдения; в Coord наблюдателя нет | решение OWNER R128 (Q1) | нет наблюдения — UNKNOWN с источником и временем | наблюдателей в коде нет: искал в `cmd/coordd` и `store/` | решение OWNER R128 (Q1) |
Приватные ключи и credentials не показываются; для ключа — только fingerprint
(OWNER-3 §7, правило приёмки 10). Отдельно: ссылки `ARTIFACT` в результатах
fail-closed с «artifact metadata not defined» — метаданные артефакта зарезервированы
в RA-1, но не определены, поэтому доказательства по артефактам сейчас недоступны
(`docs/plans/p0-review-audit/RA-1.md`).

## 8. Publication: commit, push, CI, merge, deploy

Показывается отдельно даже при автономной работе координатора-разработчика
(CONCEPT §8).

Норма о поведении Coord здесь одна: ADR-0008 (Proposed) — Review PASS и Audit PASS на
одном tree → READY_TO_COMMIT → Git CAS → проверка SHA координатором →
`COMMIT_CREATED` и перевод Task в COMPLETED; ADR прямо не разрешает Coord автокоммит
самому себе. Он же объявляет push, merge и deploy вне своей области. Действующей
нормы о commit-модели Coord, кроме ADR-0008 в статусе Proposed, нет.

| UI element | Target | Current | Gap | Source | Data class | Freshness | Trust boundary | Affects Task transitions | UNKNOWN / stale behavior | Current evidence | Required dependency |
|---|---|---|---|---|---|---|---|---|---|---|---|
| Commit readiness | все обязательные gates выполнены | не реализовано: в Coord нет ни readiness-проекции, ни commit path; `ADR-0008` (Proposed) описывает только целевое поведение, код проверок готовности commit нет | проекция готовности (ADR-0006) и, для автокоммита, принятие ADR-0008 | Run + Review + Audit + tree + gates | derived | при чтении snapshot | read channel | нет | неизвестное значение показывать как UNKNOWN с пометкой «источник не найден», не заменять предположением | реализация не существует, прямой тест не найден | ADR-0006 и read API |
| Local commit | SHA, source tree | наблюдаемо извне, в `CanonicalState` нет | решение OWNER R128 (Q1) | Git | external | наблюдение с `observed_at` | внешний наблюдатель | нет во внешних наблюдениях; целевой переход из ADR-0008 (Proposed) в действующей норме не действует | нет наблюдения — UNKNOWN | реализация не существует, прямой тест не найден | решение OWNER R128 (Q1) |
| Commit signature | evidence | наблюдаемо | решение OWNER R128 (Q1) для наблюдений подписи | Git verification | external | наблюдение с `observed_at` | внешний наблюдатель | нет | нет проверки подписи — UNKNOWN, а не «подписано» | наблюдателя в Coord нет; проверка подписи Git — вне coord | решение OWNER R128 (Q1) |
| Push | remote, branch | наблюдаемо, в Coord нет | решение OWNER R128 (Q1) | remote Git | external | наблюдение с `observed_at` | внешний наблюдатель | нет | нет наблюдения remote — UNKNOWN | наблюдателя в Coord нет: remote/branch в canonical state не хранятся | решение OWNER R128 (Q1) |
| CI | SHA, источник, время | workflow есть (`6eb78e3`, `make check` на ubuntu-latest и macos-latest), статус в Coord нет | чтение статуса CI | CI provider | external | наблюдение с `observed_at` | внешний наблюдатель | нет | нет данных CI — UNKNOWN | наблюдателя в Coord нет; есть только конфигурация workflow | решение OWNER R128 (Q1); чтение статуса CI — разрыв ADR-0006 |
| Merge readiness | не Task Status | нет | решение OWNER R128 (Q1) для наблюдений merge и CI; правило готовности к merge не определено | branch, CI, policy | derived из external | при чтении наблюдений | read channel | нет | устаревшее или недоступное наблюдение помечать как устаревшее с `observed_at`; нет источника — UNKNOWN, не «зелёный» | реализация не существует, прямой тест не найден | ADR-0006 для наблюдений; решение OWNER R128 (Q1); правило готовности к merge не определено |
| Merge в main | факт merge в целевую ветку: SHA, источник и время наблюдения | наблюдателя merge в Coord нет; правила merge в нормах Coord нет | решение OWNER R128 (Q1) | Git | external | наблюдение с `observed_at` | внешний наблюдатель | нет | нет наблюдения merge — UNKNOWN | в нормах Coord правила нет, наблюдателя в Coord нет | решение OWNER R128 (Q1) |
| Deploy | evidence | нет системы деплоя в дереве | решение OWNER R128 (Q1) | deployment provider | external | наблюдение с `observed_at` | внешний наблюдатель | нет | нет наблюдения deploy — UNKNOWN | реализация не существует, прямой тест не найден | решение OWNER R128 (Q1) |
| Release | отдельная норма | нет | решение OWNER R128 release не перечисляет; отдельная норма | release system | external | наблюдение с `observed_at` | внешний наблюдатель | нет | нет наблюдения release — UNKNOWN | реализация не существует, прямой тест не найден | решение OWNER R128 release не покрывает: отдельная норма |
| Retry state | не в Task без нормы | нет | норма о retry state не определена; R128 его не покрывает | процесс координатора-разработчика | client-local / process state | локально | нет | нет | нет наблюдения retry — UNKNOWN | реализация не существует, прямой тест не найден | норма о retry state не определена; R128 его не покрывает |

Внешние наблюдения (push, CI, merge, deploy) не завершают Task и не участвуют в
recovery: по OWNER-2 §8 они остаются external observations. Целевой local commit из
ADR-0008 (Proposed) — другое поведение: он вводит `COMMIT_CREATED` и перевод Task в
COMPLETED, но ADR в статусе Proposed, поэтому в действующей модели Coord завершение
Task локальным commit не предусмотрено, а в коде не реализован ни один из этих
переходов. Для каждого внешнего наблюдения нужны source, subject SHA, observed
status, observed_at, freshness, external reference.

Расхождение с вводной OWNER-3 §10: вводная пишет «Commit readiness — Derived»,
что согласуется с нормами; но в строке «Commit readiness» как «канонический статус —
не хранится» верно. Расхождений здесь нет; ключевое расхождение в самом факте
каноничности публикации — его нет ни в коде, ни в норме.

## 9. Cursor последнего просмотра

`coord changes --since-last-owner-view` требует хранить точку отсчёта.

- Вариант по OWNER-1: различать персональное UI-предпочтение и cursor просмотра,
  подтверждённое ознакомление OWNER и канонический acknowledgement. По умолчанию
  достаточно клиентского cursor; cursor нельзя использовать как доказательство
  ознакомления.
- Норма: прямой нормы о cursor или acknowledgement нет. ADR-0003 применяется, только
  если будущая подписанная OWNER-команда сделает acknowledgement каноническим фактом:
  тогда это внешняя ссылка с provenance по ADR-0003.
| UI element | Target | Current | Gap | Source | Data class | Freshness | Trust boundary | Affects Task transitions | UNKNOWN / stale behavior | Current evidence | Required dependency |
|---|---|---|---|---|---|---|---|---|---|---|---|
| Точка отсчёта `changes` | разница с последней точкой | нет: в `CanonicalState` нет ни cursor, ни acknowledgement (`store/canonical.go:68-77`), локальной настройки в коде тоже нет | решение OWNER R129 (Q2): cursor локальный; доказуемое ознакомление — отдельная подписанная команда в будущем | локальный профиль клиента | client-local (R129) | устаревший cursor показывается как устаревший | нет — не доказательство ознакомления | нет | N/A — значение клиентское; staleness внешних данных UI не проверяет | реализация не существует, прямой тест не найден | N/A — значение клиентское |
| Ознакомление OWNER | отдельная отметка с автором и временем | нет | решение OWNER R129 (Q2): ознакомление не следует из cursor | команда OWNER | UNKNOWN — форма будущей подписанной OWNER-команды не определена (R129) | при подтверждении | command channel | нет | нет acknowledgement — UNKNOWN, cursor ознакомления не доказывает | реализации нет: сущности acknowledgement нет | решение OWNER R129 (Q2); будущая подписанная OWNER-команда |

Правило приёмки 9 (OWNER-3 §14): UI cursor не является подтверждением ознакомления
OWNER. Это правило подтверждено решением OWNER R129 (Q2): cursor хранится в профиле
клиента и не означает ознакомления. Gap: доказуемое ознакомление потребует отдельной
подписанной OWNER-команды.

## 10. Соответствие ADR и TASK-MODEL

| Пункт концепта | Норма | Оценка |
|---|---|---|
| Счётчики и статус как проекция | ADR-0001 CM-02, раздел про вычисляемый статус; ROADMAP: «Attention/Health вычисляются, не хранятся» | совпадает |
| Внутренние поля скрыты на первом уровне | ADR-0001, «Read-only API и Dashboard»: «Сам Dashboard не запускает задачи, не изменяет FSM и не управляет агентами»; CM-02 (нет своей БД и FSM) | совпадает; разделение уровней деталей — форма Dashboard, нормами не запрещена |
| Attention = `Status=BLOCKED` + причина, основание, варианты | TASK-MODEL TM-11, TM-12; ADR-0001 CM-06; ADR-0002 DG-07 | совпадает по модели, не реализовано |
| Решение OWNER через OwnerCredential и challenge | ADR-0001 CM-12 (R110), TM-24, CM-06 | совпадает при разделении интерфейсов |
| «OWNER не рассылает решение агентам вручную» | ADR-0001 CM-06: Coord атомарно записывает decision и переход | совпадает |
| «Обновление зависимых задач» после решения | ADR-0002 DG-08: изменение графа — Owner-команда с Task-scoped capability и ожидаемой Graph Revision, в одном commit | расходится по смыслу: массовое обновление зависимых Task не нормировано; см. Q3 |
| Смысловые состояния публикации как состояния | TASK-MODEL (единственный `TaskStatus`), ADR-0001 CM-02, ROADMAP «UI не создаёт собственных Task/workflow states» | расходится: допустимы только как проекция; назвать для каждого канонический факт |
| `READY_TO_COMMIT` в потоке задач | в нормах Task такого состояния нет; `READY_FOR_COMMIT` из AGENTS.md — состояние координатора, не Task Status | расходится |
| Публикационные факты в каноническом состоянии | ADR-0005 (границы durable state), ADR-0008 Proposed и вне его области; решение OWNER R128 (Q1) | расходится: решение R128 оставляет факты публикации внешними |
| `Key fingerprint SHA256` | привязка хранит публичный ключ или его отпечаток; формат не нормирован | расходится как норма; допустимо как вычисляемое отображение |
| Стартовая карточка на первом запуске | ADR-0001 «Agent Startup Card» — артефакт назначенного Run | расходится: на первом запуске назначенного Run ещё нет |
| Событие `OWNER_DECISION_ACCEPTED` | типа события нет ни в коде, ни в норме; `OWNER_DECISION` в TASK-MODEL — Stage | расходится: Gap проекта границы команд, не вопрос OWNER |
| Секреты не попадают в UI-структуры | ADR-0001 CM-09 | совпадает |
| Итоговый отчёт «решений OWNER не потребовалось» | AGENTS.md «Автономия координатора», R107 | совпадает |
| Проекция не используется для recovery | ADR-0007 BR-03 (Proposed): «Ни один переход, проверка инварианта и восстановление не читают Brief» — единственная найденная норма о производной, не участвующей в recovery; общего запрета для всех проекций Coord в нормах нет (см. UNKNOWN в разделе 12) | совпадает для Brief; для OwnerView норма отсутствует |
| `coord discover`: reachable не превращается в ready | ADR-0001 CM-10 требует показывать generation, freshness, attachment и readiness раздельно и запрещает сводить их в один «online»; строка «Проверка:» CM-10 говорит «поколение канонических Session не реализовано», но части 1–3 AgentSession ввели каноническое поколение — расхождение документа и кода | совпадает по смыслу; расхождение CM-10 с кодом — отдельная задача, здесь не правится |
| `coord start crewflow --coordinator REF` с ожиданием accept | ADR-0001 CM-04 (DONE требует терминального Run и подтверждённого результата) | совпадает |
| Блокировки по макету (OW-17 «address reuse») | решён R126 | пример устарел, см. раздел 12 |

## 11. Текущее состояние реализации

Утверждение «реализовано» — только код и тест, который падает при поломке
поведения (AGENTS.md).

| Пункт | Состояние | Основание |
|---|---|---|
| Каноническое состояние Tasks/Runs/Events/Inbox/Outbox/AgentSessions | реализовано | `store/canonical.go`; `TestCanonicalStateValidatesTaskEventAndRunRelations`, `TestCanonicalStoreRejectsTaskMutationWithoutEvent` |
| Fail-closed ownership состояния | реализовано | `store.CanonicalPersistence`; `TestCoorddSecondProcessCannotOpenOwnedNamespace`, `TestRefusedOwnershipFailsBeforeLoadingState` |
| TaskID | реализовано | `taskid.New` (`taskid/id.go:34`), `taskid.ID` (`taskid/id.go:31`); `TestCreateTaskGeneratesCanonicalTaskID` |
| Task.Title и Task.Description | поля существуют, прямых тестов нет | `task/model.go:73-86`; искал в `task/model_test.go` и по `Title`/`Description` в `*_test.go` — прямого теста, падающего при поломке поля, нет |
| `OWNER_DECISION` как Stage и `RequireOwnerDecision` | реализовано как поле плана | `task/model.go:17,36`; проверка `TestTaskValidationRejectsInvalidPlanAndTerminalStage` |
| Журнал DomainEvent | реализован частично: четыре типа | `task/event.go:13-16` |
| Привязка Run к `AgentSession` и поколение | реализовано (части 1–3) | `store/canonical_agentsession_binding.go`; `TestCanonicalAssignmentTakesCurrentActiveAgentSessionBinding`, `TestCanonicalStartupRequiresGenerationAndBindingForActiveTaskRun` |
| Независимость Reviewer/Auditor по `AgentID` | реализовано в пределах startup и перехода | `TestReviewerRejectsExecutorAgentIDAcrossHistory`, `TestAuditorRejectsExecutorAgentIDAcrossTaskRuns` |
| Structured blocker у Task | не реализовано | в `task/model.go` нет; `Run.Blocker` — строка из heartbeat |
| Attention projection | не реализовано | нет кода; TM-11/TM-12 частичны или не реализованы |
| Owner decision через OwnerCredential | не реализовано | COMMAND-BOUNDARY §3.2 относит к P0-3; в коде нет `OwnerCredential` |
| Read API daemon | не реализовано | есть только control endpoint `GET /runs` (supervisor recovery) |
| `OwnerView` | не реализовано | нет кода и нормы |
| Project/Task Brief | не реализовано | ADR-0007 (Proposed) |
| Публикационные факты в Coord | не реализовано | нет в `CanonicalState`, нет нормы |
| Commit readiness и локальный commit силами Coord | не реализовано | в нормах Coord такого поведения нет: ADR-0008 (Proposed) только описывает целевое и не разрешает автокоммит самому себе; правило работы координатора-разработчика — раздел 12.1, не норма продукта |
| CI в репозитории | реализовано как workflow | `.github/workflows/ci.yml`, `make check` на ubuntu-latest и macos-latest |
| Темы и визуальная система | не реализовано | нормами не задана |
| `TaskID` adoption, `task_<ULID>` | реализовано в пакете `taskid` и в каноническом состоянии store; в `internal/taskservice` adoption пока изолирован | `taskid.New` (`taskid/id.go:34`), `TestCreateTaskGeneratesCanonicalTaskID`; комментарий `internal/taskservice/service.go` называет пакет изолированным и используемым пока только тестами, поэтому production adoption через него не заявлен |

UNKNOWN (источник не назван ни в норме, ни в коде): время последнего
подтверждённого обновления, агрегаты «последние изменения», формат `OwnerView`,
перечень `SourceReferences`, приоритет задачи, прогресс задачи, «уверенность» и
«прогноз срока» в Explain.

## 12. Зависимости и порядок срезов

Срезы — порядок из CONCEPT §15. В ROADMAP он не записан: этапы определяет владелец
ROADMAP. Ниже только зависимости, следующие из норм, и кандидаты на существующие
строки.

| Срез | Норма-зависимость | Кандидат в ROADMAP | Комментарий |
|---|---|---|---|
| 1. Read-only: status, tasks, task show, changes; daemon read API; OwnerView | ADR-0001 шаги 12–13; ADR-0004 RT-05; решение OWNER R129 (Q2): cursor локальный | «После ADR-0006 \| Dashboard и Project Map» (ROADMAP:48) | ADR-0006 не написан (TARGET-ARCHITECTURE, «Номера ADR»); без него срез не начинается |
| 2. Attention: AttentionItem, `coord attention`, причина, источники, варианты | TM-11, TM-12; CM-06 | «После ADR-0006», в части «Attention/Health вычисляются» | требует исполненных TM-11/TM-12 |
| 3. Owner decisions: OwnerCredential, challenge, `coord decide`, durable решение | CM-12 (R110), TM-24; решение OWNER R130 (Q3): только предписанный нормой переход; ADR-0002 DG-08 | «До P0-3 \| Owner command boundary» (ROADMAP:38) и «После ADR-0006» | проект границы команд OWNER принят (R110–R113), реализация — P0-3 |
| 4. Explain | CM-06; TM-12 | «После ADR-0006» | требует структуры blocker и журнала |
| 5. Publication: commit/push/CI/merge/deploy | решение OWNER R128 (Q1): внешняя read-only проекция по ADR-0006; ADR-0008 (Proposed) остаётся целевой нормой о commit-модели Coord и для этой проекции не требуется — раздел 12.1 | «После ADR-0006» | решение R128 принято; срез — после ADR-0006 |
| 6. Полный TUI и темы | CM-02; ADR-0001 «Read-only API и Dashboard» | «После ADR-0006» | оформление нормами не затронуто |

### 12.1 Правило работы координатора-разработчика (не норма продукта)

AGENTS.md, Commit Readiness (R127) регулирует, кто и с какой проверкой выполняет
локальный commit, push, merge в main, deploy и release при разработке этого
репозитория. К поведению Coord это отношения не имеет: commit-модель продукта
задаёт ADR-0008-AUTONOMOUS-COMMIT.md (Proposed), и до его принятия Owner Console не
показывает автономный commit Coord как действующую норму. В разделах 3–9 это
правило не используется как источник требований; commit, push, CI, merge и deploy
остаются внешними наблюдениями (решение OWNER R128, Q1). В разделе 8 указано только, что
ADR-0008 выводит push, merge и deploy за пределы своей области, — как факт о тексте
ADR, без правил действий.

Уточнения, которые уточняются в ADR-0006 или ALIGNED_UX, а не решением OWNER:

- **Имя и схема события решения OWNER.** Типа события нет ни в коде, ни в норме;
  `OWNER_DECISION` в TASK-MODEL — Stage. Определяется в проекте границы команд P0-3
  (COMMAND-BOUNDARY §3.2, §6.2). Расхождение с вводной OWNER-3 §9.
- **Формат fingerprint ключа.** Привязка хранит публичный ключ или его отпечаток;
  формат не нормирован. Если станет interop-контрактом между UI и внешним
  потребителем, потребуется норма; пока — UI-convention с пометкой «вычисляемо».
- **`coord discover`.** По ADR-0001 CM-10 поколение канонических Session не
  реализовано, проверены только legacy rooms; применимость discover к канонической
  модели сессий уточняется в ADR-0006 и разделе первого запуска ALIGNED_UX.
- **Стартовая карточка владельца.** По ADR-0001 Agent Startup Card — артефакт
  назначенного Run; на первом запуске назначенного Run нет. Форма для состояния
  владельца уточняется в ADR-0006 и ALIGNED_UX.
- **`CommandID` в схеме доказательств** — это `MessageID` в `InboxReceipt`
  (`store/canonical.go:42-50`); отдельного `CommandID` в канонической модели нет.
- **`GET /runs`** — supervisor control endpoint для recovery, а не Task read API;
  использовать его как read API нельзя.
- **`AgentSession` — привязка, а не сетевая доступность.** Reachability —
  отдельное runtime-наблюдение transport-слоя; привязка не доказывает, что агент
  доступен, а доступность не подтверждает привязку (ADR-0005 ST-02).
- **Зелёное значение в блоке System** обязано сопровождаться источником, временем
  и критерием; `/readyz` не равен полной готовности модели Task-wide (ADR-0004,
  «Наблюдаемость»).

Заметка о макете: `OW-17 «AgentSession address reuse»` — условный пример карточки
типа, вопрос закрыт R126 (ADR-0001 CM-13, TASK-MODEL TM-27,
`docs/plans/p0-2-task/AGENT-COMMAND-BOUNDARY.md`). Данные макета не отражают
текущее состояние.

Первичные находки координатора C1–C9 проверены по источникам: C1 подтверждён
(раздел 2), C2 подтверждён (раздел 8 и Q1), C3 подтверждён (раздел 3, 4.3 и Q3),
C4 подтверждён (раздел 6), C5 подтверждён (раздел 7.2), C6 подтверждён (раздел 9 и
Q2), C7 подтверждён (раздел 12), C8 подтверждён (раздел 12), C9 уточнён (раздел 6
и Q3): механизм изменения графа нормирован DG-08 и не совпадает с формулировкой
«обновления зависимых задач».

## 13. Открытые вопросы OWNER

Ровно четыре вопроса. Решения OWNER приняты 2026-10-09: R128, R129, R130, R131 —
по одному на вопрос (Q1…Q4). Варианты ниже сохранены как обоснование выбора,
включая отклонённые варианты и их последствия.

### Q1. Publication facts: где живут факты публикации

- Вариант A — внешняя read-only проекция: факты наблюдаются из Git, CI и системы
  деплоя, показываются с источником, временем и пометкой «внешнее наблюдение». До
  решения OWNER push, CI, merge и deploy не участвуют в transitions Task и не
  пишутся в canonical state как Domain Events.
- Вариант B — канонические publication facts: они становятся частью
  `CanonicalState`. Требуется новая норма или ADR с решением: кто и как их пишет
  (сегодня ни один компонент Coord не выполняет git-операции), жизненный цикл,
  идемпотентность, связь с Task и commit. Изменяет границы durable state
  (ADR-0005).
- Вариант C — специализированный integration journal: отдельный журнал внешних
  наблюдений рядом с каноническим состоянием, с provenance по ADR-0003 и без
  влияния на transitions. Сам по себе журнал не участвует в канонической команде и
  не является миграцией canonical snapshot, поэтому ни ST-01, ни ST-05 не
  запрещают его напрямую: ST-01 (ADR-0005) требует одной атомарной фиксации
  состояния, события и Outbox внутри канонической команды, ST-05 — атомарной
  смены схемы с резервной копией. Пересмотр ST-01 и ST-05 становится необходим
  только если журнал включается в canonical command/commit boundary или затрагивает
  миграцию canonical schema. Требуется отдельное архитектурное решение: ADR,
  владение журналом и границы его consistency относительно canonical state.
  ADR-0003 разрешает внешние ссылки, но не делает внешние статусы источником
  workflow.

**Решение OWNER R128: вариант A — внешняя read-only проекция.** Факты публикации
наблюдаются во внешних системах; каждый отображается с источником, SHA, временем
наблюдения и свежестью; в `CanonicalState` не входят, в recovery и переходах Task
не участвуют; ошибка или недоступность внешней системы даёт UNKNOWN или STALE, но
не изменяет каноническое состояние. Канонизация publication facts может быть
рассмотрена отдельно при принятии модели автокоммита и публикации; сейчас менять
schema и ST-01/ST-05 не нужно.

Последствия: A — принято решением R128, срез 5 реализуем без новой нормы, но
остаётся внешним и не доказывает состояние системы; B — не выбран, потребовал бы
нового ADR и изменения схемы; C — не выбран, отдельный ADR, владение и границы
consistency остаются открытым вопросом; факты local commit, push, CI и merge
остаются отдельными и не выводятся один из другого.

### Q2. Точка последнего просмотра OWNER

- Вариант A — локальный cursor в профиле клиента: достаточно для «изменений с
  прошлого просмотра», разных клиентов показывают разное, ознакомление не
  доказывается.
- Вариант B — подтверждённое ознакомление OWNER: отдельная отметка, которую OWNER
  подтверждает через command channel; cursor остаётся клиентским, но «прочитано»
  становится фактом с автором и временем.
- Вариант C — канонический acknowledgement в `CanonicalState`: ознакомление
  становится каноническим фактом, участвует в проекциях и может влиять на
  последующие решения.

**Решение OWNER R129: вариант A — локальный cursor клиента.** Cursor хранится в
профиле конкретного клиента и используется только для изменений с прошлого
просмотра; не означает, что OWNER прочитал или принял информацию; не влияет на
Task, Attention, блокеры и сроки; потеря cursor приводит только к повторному показу
изменений. Для доказуемого ознакомления в будущем потребуется отдельная подписанная
OWNER-команда.

Последствия: A — принято решением R129, минимальный объём, доказательства ознакомления
нет; B — не выбран, соответствующая норма сейчас не вводится; C — не выбран,
изменение схемы и решение о том, участвует ли acknowledgement в transitions,
сейчас не вводятся.

### Q3. Побочные эффекты решения OWNER

Что именно должно произойти при фиксации решения помимо самой durable записи.

- Вариант A — только durable запись решения: по CM-06 и TM-24 Coord проверяет
  область capability, вариант, актуальную версию Task и hard policy, затем атомарно
  записывает decision event и переход, который сама норма CM-06 предписывает для
  эскалации. Всё, что не этот переход — снятие отдельного blocker'а, создание Runs,
  разблокирование зависимых задач, формирование Outbox, — остаётся отдельными
  действиями координатора по нормам. Граница варианта: переход Task, вызванный
  самим решением, входит; побочные мутации не входят.
- Вариант B — запись решения дополнительно снимает blocker и разблокирует
  зависимые задачи в одном commit: по DG-07/DG-08 изменение графа допускается
  атомарной Owner-командой с Task-scoped capability и ожидаемыми Task и Graph
  Revision, но произвольная массовая мутация зависимых Task не нормирована —
  значит, потребуется норма либо сужение до графовой команды и пересчёта проекций.
- Вариант C — запись решения дополнительно формирует Outbox и создаёт Runs по
  StagePlan: требует нормы о порядке (решение → назначение), иначе возможен
  недетерминированный порядок fence и ownership.

**Решение OWNER R130: вариант A — запись решения и только предписанный нормой
переход Task.** Решение фиксируется вместе с тем переходом, который прямо
предусмотрен действующей нормой; блокеры и зависимые задачи после commit
пересчитываются как проекции; массовые мутации зависимых Task не выполняются;
новые Runs не создаются; Outbox формируется только тогда, когда его прямо требует
выполняемый нормативный переход; если норма не определяет дальнейшее действие, Task
остаётся заблокированной с конкретной причиной, а не разблокируется эвристически.
`coord decide` не получает скрытой оркестрационной логики.

Последствия: A — принято решением R130, каноническая запись и переход уже
нормированы, объём минимален; задача может остаться BLOCKED, но с конкретной
причиной, а объяснение строится проекцией; B — не выбран, соответствующая норма
сейчас не вводится; C — не выбран, соответствующая норма сейчас не вводится.

### Q4. Web command channel

- Вариант A — web остаётся полностью read-only: ни одна команда OWNER не подаётся
  из браузера, решение принимается в CLI.
- Вариант B — переход из web во внешний защищённый CLI: web показывает карточку и
  отдаёт подписанный вызов наружу, сама аутентификация OWNER остаётся вне браузера.
- Вариант C — полноценный web command channel с OWNER-аутентификацией, подписанным
  challenge, защитой от replay и проверкой revision — требует отдельного security
  design и, вероятно, нового ADR.

**Решение OWNER R131: вариант A — web read-only, решения через CLI.** Web
показывает Overview, Tasks, Attention, Explain и Evidence; кнопка решения в web не
выполняет команду; web может показать точную CLI-команду и данные challenge, но не
подписывает и не отправляет решение; `coord decide` проходит через защищённый Owner
command boundary и `OwnerCredential`. Варианты B и C требуют отдельного security
design; до него их не реализовывать.

Последствия: A — принято решением R131, web остаётся read-only (правило приёмки 5),
кнопка решения в web не выполняет команду; B — не выбран, требует отдельного
security design; C — не выбран, требует отдельного security design, и до его
принятия браузерная аутентификация OWNER не реализуется.

## 14. Проверяемые критерии приёмки

Правила UI из OWNER-3 §14, каждое проверяемо по проекту реализации:

1. Ни один вычисляемый badge не записывается обратно в Task.
2. Каждый пункт Explain имеет ссылку на источник.
3. UNKNOWN не заменяется предполагаемым значением.
4. «Принять» проходит через Owner command boundary, а не записью в Dashboard.
5. Web UI read-only до отдельного security design.
6. External publication observations не участвуют в recovery.
7. Transport reachability не показывается как agent readiness.
8. `COMMITTED_LOCAL`, `PUSHED`, `MERGED`, `DEPLOYED` различаются и не смешиваются.
9. UI cursor не является подтверждением ознакомления OWNER.
10. Приватные ключи, credentials и приватные evidence не попадают в `OwnerView`.
11. Для одной revision `OwnerView` детерминирован, кроме помеченных внешних
    наблюдений.
12. TUI, CLI и JSON используют одну проекцию и одну семантику полей.
13. Факты публикации не появляются в `CanonicalState` и не участвуют в recovery;
    недоступность внешней системы даёт UNKNOWN или STALE (R128).
14. Cursor не означает ознакомление OWNER и не влияет на Task, Attention, блокеры и
    сроки (R129).
15. Зависимые Task массово не мутируются, их блокеры пересчитываются как проекции;
    если норма не определяет дальнейшее действие, Task остаётся заблокированной с
    конкретной причиной (решение OWNER R130).
16. Кнопка решения в web не подписывает и не отправляет решение: она показывает
    только точную CLI-команду и данные challenge (R131).

Критерий готовности анализа: для каждого элемента интерфейса из разделов 3, 4, 6,
7, 8 и 9 заполнены все восемь полей — источник, класс, свежесть, trust boundary,
участие в переходах, Target, Current с именем теста или «нет», Gap; каждое
утверждение «реализовано» подтверждено именем теста; каждое утверждение «нет»
проверено поиском по коду и указано, где искали.