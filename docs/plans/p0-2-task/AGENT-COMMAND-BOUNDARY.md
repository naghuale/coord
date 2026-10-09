# Граница команд агента (проект, зависимость P0-3)

Статус: Accepted (OWNER, 2026-10-08). Автор: OpenCode (исполнитель), ревью:
Codex (ACCEPTED_BY_CODEX), приёмка: OWNER. Реализация — в P0-3 после приёмки
этого документа и `docs/plans/p0-2-task/COMMAND-BOUNDARY.md` (зависимость 1
из §5 того документа, зависимость 2 — этот документ).

Решения OWNER 2026-10-08:

- **R114** — §5.1 и §5.2, вариант A: канонический агрегат `AgentSession`
  (`AgentID`, адрес сессии, монотонное поколение, отпечаток ключа) со своим
  потоком событий. Регистрирует оператор. Поколение записывается в Run и
  сверяется на каждой входящей команде.
- **R115** — §5.3, вариант A: проверки идентичности и эпохи (Task, Run, сессия,
  actor, поколение, Fence) выполняются до replay; CAS ревизии остаётся после
  replay и применяется только к новой команде. Реализация — новый метод store под
  той же блокировкой `s.mu` (§3.2).
- **R116** — §5.4, вариант A: непустой `AgentID` обязателен для любого
  Task-scoped Run, принимающего агентскую команду.
- **R117** — §5.5, вариант A: проект задаёт границу для `ACK`, `HEARTBEAT` и
  `RESULT`; канонический Executor RESULT — отдельный кусок P0-3. До него запись
  от Executor не подключается.
- **R118** — §5.6, вариант (b): actor определяется внутри нового метода. Вход —
  аутентифицированная сессия, `RunID` — только селектор.
- **R122** — §5.1, §5.2 и §3.2, смена адреса сессии:
  старая привязка отзывается отдельным событием, новый адрес регистрируется как
  новая `AgentSession`; поколение означает только ротацию ключа в той же привязке,
  и смена адреса под ротацию не маскируется. Отозванная привязка не авторизует
  команду; повтор команды через старую привязку отклоняется до replay. История
  хранит адрес, ключ, время действия и причину отзыва.
- **R123** — §5.1: старый canonical state с активным command-capable Run без
  действующей `AgentSession` блокирует readiness fail-closed с понятной
  диагностической причиной; автоматического backfill и новой
  recovery-процедуры сейчас нет, файл автоматически не переписывается, `AgentID`,
  поколение, ключ и адрес задним числом не заполняются. До подключения
  канонического agent command path такие состояния штатным путём не создаются.
  Owner recovery command (stop proof, завершение старого Run, новое назначение из
  свежей `AgentSession`) — запланированное продолжение P0-3, не реализуется до
  отдельного проекта и решения.
- **R124** — §5.5, уточняет R117: при подключении канонического Executor RESULT
  legacy-путь закрывается для всех Run с непустым `TaskID`; для Task-scoped Run
  канонический command path — единственный, fallback в legacy при ошибке запрещён.
  Для Run без `TaskID` legacy-путь временно сохраняется как совместимость, а не
  второй production write path для Task; полное удаление требует отдельного
  решения после появления работающей замены.
- **R125** — §5.1 и §5.3, решение OWNER по вопросу #13, вариант A: ротация ключа
  или отзыв `AgentSession` при активном Run разрешены. Такой Run огорожен: его
  команды отклоняются до replay, readiness не блокируется, а переназначение идёт
  только через recovery transfer на текущую активную привязку. Отклонение команд
  до replay реализовано в канонической границе `ApplyAgentCommand`; не подключён
  production command path.
- **R126** — §5.1, повторная регистрация отозванного адреса: отозванный адрес
  `AgentSession` не регистрируется повторно никогда; вернувшийся агент
  регистрируется на новом адресе с прежним `AgentID`. Повторная регистрация с
  поколения 1 совпала бы с огороженным Run — тем же адресом и поколением — и сняла
  бы ограждение R125, то есть вернула бы ABA.

Принятые решения меняют статус проекта, но не Accepted ADR: правки ADR-0001 и
TASK-MODEL, которые из них следуют, фиксируются в §6 и делаются отдельно.
R122–R124 подтверждены OWNER и записаны в ADR-0001 (CM-13, CM-15) и TASK-MODEL
(TM-27, TM-29) нормативной записью NORM-AS. R125 записан в ADR-0001 (CM-13) и
TASK-MODEL (TM-27) записью NORM-AS-3; R126 — записью NORM-AS-4 в те же места.

Этот проект не переносит legacy `SessionAuth` из rooms в новую модель
(`docs/plans/p0-2-task/DESIGN.md`, §1) и не вводит второй write path.

## 1. Факты на текущем дереве

### 1.1. Три разных входа агентского сообщения

| Вход | Где | Что проверяет | Чего не проверяет |
|---|---|---|---|
| Legacy `Coordinator.Handle` | `coordinator.go:227` | адресата (`:234`), `e.Fence` против `Run.Fence` в обе стороны (`:260`, `:265`), `OwnerSession`/`Attempt`/`TargetSHA`/`Task` (`:270`), дедупликацию по `Seen[MessageID]` (`:286`), recovery claim (`:293`), корреляцию с `TaskMessages` (`:313`), FSM `Run.Apply` (`:325`) | `TaskID` (сравнивается legacy `Run.Task`), `Task.Fence`, session generation, стабильный `AgentID`, роль агента; канонический commit не используется — только `RunStore.Update` |
| `CanonicalStore.ApplyInboxCommand` (`store/canonical.go`) | идентичность команды и handler (проверка в начале `ApplyInboxCommand`), replay по `InboxKey(actorID, messageID)` (проверка replay перед CAS), CAS по `Task.Revision` до вызова handler, запрет handler менять Inbox, совпадение receipt с командой, ровно одна ревизия, snapshot- и transition-validation, commit до публикации в память | входных `Task.Fence`, `Run.Fence`, `Attempt`, `RunID` извне, `OwnerSession`, session generation, `AgentID`, роли. Комментарий над `ApplyInboxCommand` прямо относит эти проверки к daemon command layer |
| `RecordReviewResult` / `RecordAuditResult` | `store/canonical_result.go:17`, `:26` | actor берётся из сохранённого Run, а не из входа (`:71-75`), повторная сверка actor внутри handler (`:78`), Task существует и `IN_PROGRESS` на review stage (`:81-87`), Run не terminal и `STARTED`/`RESULT_READY` (`:88`), совпадение Stage и роли (`:97`, `:110`), finding ID генерирует Coord (`:102`), ссылки только внутри Task (`:126`), typed result в одном commit с `RESULT_ACK` (`:137-156`) | входных `Task.Fence`, `Attempt`, `OwnerSession`, session generation — их нет в сигнатуре. Это зафиксировано в `docs/plans/p0-review-audit/RA-1.md:23-30` как требование daemon command boundary |

Факт кода, нормы нет: `actorID` в `recordRoleResult` берётся из persisted
`Run.RoleSelection.AgentID` (`store/canonical_result.go:71-75`). Это уже
правильная форма «актор назначен Coord, а не принят от агента», и она должна
остаться. Норма, требующая actor из аутентифицированной сессии, есть в ADR-0001,
но в Accepted ADR она сформулирована для Discussion facts, не для Review/Audit
result; прямое применение к result path — вывод, а не цитата.

Факт кода, нормы нет: канонического Executor RESULT command не существует.
`ACK`/`HEARTBEAT`/`RESULT` обрабатываются `fsm.Run.Apply` (`fsm/fsm.go:139`) в
legacy Run-представлении и сохраняются через `RunStore.Update`, то есть через
legacy `Run.Task` без `TaskID`, `Task.Fence` и сессионной привязки. Канонический
Task-scoped путь сегодня покрыт только Review/Audit
(`store/canonical_result.go:17`, `:26`). Это согласуется с аннотациями
RT-04 в ADR-0004 («Проверка: не реализовано») и RT-06 в ADR-0004 (не
реализована именно проверка входящей записи против текущего `Task.Fence` в
production command path).

### 1.2. Что проверяется в persisted state (уже доказуемо)

| Инвариант | Где | Что именно проверяет |
|---|---|---|
| `Run.Fence <= Task.Fence` | проверка `run.Fence > t.Fence` в `ValidateCanonicalState` (`store/canonical.go`) | снимок Run не превышает эпоху Task |
| активный Run имеет `Run.Fence == Task.Fence` | проверка единственного активного Run и равенства `Run.Fence == Task.Fence` в `ValidateCanonicalState` (`store/canonical.go`) | ровно один активный writer на Task, его снимок равен текущей эпохе |
| `Attempt` неизменен у Task-scoped Run | `ValidateCanonicalTransition` (`store/canonical.go`) отклоняет изменение `Attempt` через «run %q Attempt is immutable»; `SameDomainState` (`fsm/fsm.go:78-91`) | повторная доставка и обычная мутация не двигают Attempt |
| `Run.Fence` меняется только при recovery transfer | `validateTaskOwnershipTransfers` (`store/canonical.go`) | смена Fence требует смены `OwnerSession` у нетерминальных обоих |
| terminal Run не меняет owner | `validateTaskOwnershipTransfers` (`store/canonical.go`) | после остановки старый writer не возвращается |
| transfer повышает `Task.Fence` ровно на 1 и `Run.Fence == Task.Fence` | `validateTaskOwnershipTransfers` (`store/canonical.go`) | fencing отсекает старого writer |
| recovery сохраняет RunID и Attempt | recovery-ветка `validateTaskOwnershipTransfers` (`store/canonical.go`) | recovery не подменяет Run, а меняет эпоху |
| Executor получает следующий Attempt, Reviewer/Auditor сохраняют текущий | `validateTransferredAttempt` (`store/canonical.go`) | номер прохода берётся из committed истории |
| `RoleSelection.Session == Run.OwnerSession` | `validateTaskRunRole` (`store/canonical.go`) | сессия роли совпадает с владельцем Run |
| результат привязан к Task-scoped role Run той же роли и терминален | `store/canonical_result.go:225-231` | результат без role Run невалиден |
| у result Run есть Executor Run того же Attempt | `store/canonical_result.go:242-251` | Review/Audit не висят без прохода исполнения |
| `ARTIFACT` refs отклоняются | `store/canonical_result.go:321-322` | metadata артефактов не определены |
| независимость Reviewer/Auditor по `AgentID` | `docs/plans/p0-review-audit/RA-2.md`, реализовано коммитом `2d0cf60`; код — `store/canonical_independence.go:29`, вызовы из `ValidateCanonicalState` и `ValidateCanonicalTransition` (`store/canonical.go`) | агент не проверяет свою работу: `AgentID` Reviewer/Auditor не должен совпадать ни с одним Executor `AgentID` этой Task. Fail-closed срабатывает, когда у самого Reviewer/Auditor `AgentID` пуст, и когда Executor-назначение существует, но его `AgentID` неверифицируем и от него зависит проверка независимости. Отсутствие Executor-назначений как таковое не отклоняется: Reviewer допускается без них (`TestRoleIndependenceDoesNotRequireAnExecutorAssignment`) |

Это проверки формы durable state. Они не доказывают, что пришедшее сообщение
пришло от текущего владельца: форма проверяется на snapshot и на переходе, а
входная идентичность команды проверяется другим слоем.

### 1.3. Слой транспорта и сессии

| Механизм | Где | Что доказывает | Чего не доказывает |
|---|---|---|---|
| `envelope.Verify` | `envelope/signature.go:33`, вызов `transport/signed.go:60` | подпись ed25519 ключом, закреплённым за адресом сессии `e.From` (`envelope/signature.go:48`); вход не доходит до adapter без проверки | что за ключом стоит человек или нужный агент; что сессия ещё текущая |
| `transport.Adapter.Send` | `transport/transport.go:34-49` | `e.From` совпадает с собственной сессией (`:38`) | поколение сессии |
| Telepathy client | `telepathy/client.go:76-116` | доставку кадра; дедупликация в пределах соединения по хешу кадра (`:93-100`) | атомарный read-and-ack: по комментарию (`:20-21`) после рестарта повторяются последние 50 архивных сообщений, а при чтении запрашивается `limit: 50` (`:85`) |
| Проверка присутствия сессии при старте | `telepathy/factory.go:48-55` | сессия присутствует в `list_peers` | поколение и агент за сессией |
| Конфиг host/worker | `internal/telepathyhost/config.go:72-73`, `:38-39` | имя сессии выводится как `opencode-<anchor_pid>`; приватный ключ и пины пиров лежат в конфиге | что конфиг выдан владельцем, а не агентом; связи ключа с `AgentID` |
| Сторона worker | `internal/telepathyhost/worker.go:35-45` | входящее привязано к сохранённому Run (session, Task, Run, Attempt, Fence, SHA) | что это текущий, а не прошлый Run |

UNKNOWN: как Telepathy 0.8.0 сам аутентифицирует процесс агента. В репозитории
это не проверяется: используются только `send_message` и `read_messages`
(`telepathy/client.go:48`, `:85`) и `list_peers` (`telepathy/factory.go:48`).
Реальный API плагина не изучался; выводы о нём в этом документе не делаются.

Факт кода, нормы нет: `roles.Registry.Register` — операторское действие, и
комментарий фиксирует, что сообщения не могут изменить `agent_id`
(`roles/roles.go:181-182`). Привязка «сессия → `AgentID` → профиль» существует
(`roles/roles.go:320-324`, `:326-354`), но её revision — это ревизия реестра,
а не поколение сессии: она меняется при изменении профиля и не меняется при
переподключении.

### 1.4. Чего нет в модели

- Канонической сущности `Session` нет: тип `CanonicalState` (`store/canonical.go`)
  содержит `Tasks`, `Runs`, `Events`, `Inbox`, `Outbox`. ADR-0001 перечисляет
  `Session` среди целевых канонических сущностей (раздел «Целевые
  канонические сущности»), а CM-10 в ADR-0001 говорит, что поколение
  канонических Session не реализовано, и что проверены только legacy rooms
  (`internal/rooms/sessions.go:45-50`).
- Поля session generation нет ни в `fsm.Run` (`fsm/fsm.go:39-71`), ни в
  `envelope.Envelope` (`envelope/envelope.go:31-54`).
- Стабильный `AgentID` присутствует в `RoleSelection` (`roles/roles.go:320-324`)
  — это единственное место, где он связан с сессией и ролью текущего назначения.
  Он же сохраняется как снимок в `DomainEvent.ActorID` (`task/event.go:28`) и
  `InboxReceipt.ActorID` (тип `InboxReceipt`, `store/canonical.go`), но эти поля не связывают
  сессию с агентом и не участвуют в решении, кто имеет право записи.

Следствие: требование TASK-MODEL, раздел «Task-wide Fence и ownership»,
«TaskID, RunID, Attempt, session generation и Run.Fence совпадают с текущим
Task ownership и текущим
`Task.Fence`» сегодня выполнимо по TaskID, RunID, Attempt и Run.Fence
(эти значения есть в Run и в persisted transition) и невыполнимо по session
generation. Это и есть главный пробел, который закрывает проект; решение принято:
агрегат `AgentSession` вводится по R114 (§5.1, §5.2).

## 2. Модель capability агентского Run

Capability агентского Run — это набор проверяемых фактов, а не токен. Важно не
смешивать две вещи: persisted-инвариант (свойство сохранённого состояния) и
проверку входящей команды (сверка того, что прислал агент, с текущим
состоянием). Первое в дереве есть; второе реализовано частично и неодинаково для
двух канонических входов — общий `ApplyInboxCommand` проверяет формат `TaskID` и
ревизию, специализированный путь Review/Audit дополнительно проверяет роль,
Stage и принадлежность Run (см. §1.1 и итог под таблицей). Поэтому колонки
разделены: (a) что уже проверяется в состоянии, (b) что реально проверяется во
входящей команде сейчас и что требуется целевой границей §3.

| Что доказывает право записи | (a) Инвариант persisted state (проверяется сейчас) | (b) Проверка входящей команды (текущее состояние; целевая граница — §3) | Источник |
|---|---|---|---|
| TaskID команды существует и принадлежит Task | Task существует в snapshot и проходит `Validate` (`ValidateCanonicalState`, `store/canonical.go`) | валидный формат `TaskID` в команде (проверка формата есть; принадлежность Run проверяется в цели, §3 шаг 4) | TM-02; RT-04 |
| Команда адресована текущему активному Run | активный writer ровно один и его `Run.Fence == Task.Fence` (`ValidateCanonicalState`, `store/canonical.go`) | `RunID` входа равен этому Run — **не реализовано в общем пути**: `ApplyInboxCommand` `RunID` не принимает (§3.1). Частично реализовано в специализированном пути: `RecordReviewResult`/`RecordAuditResult` принимают `runID` и сверяют принадлежность Run этой Task (`store/canonical_result.go:71-75`, `:78`), но не сверяют эпоху, попытку и сессию | RT-04; RT-06 |
| Attempt команды равен текущему проходу | `Attempt` неизменен у Task-scoped Run, Executor получает `max+1`, Reviewer/Auditor сохраняют текущий (`ValidateCanonicalTransition` и `validateTransferredAttempt`, `store/canonical.go`) | `Attempt` входа равен `Run.Attempt` — **не реализовано**: `ApplyInboxCommand` не принимает `Attempt` | TM-17; TM-20; TM-21; TM-22 |
| эпоха записи актуальна | при передаче `Task.Fence` повышается ровно на 1, `Run.Fence == Task.Fence` (`validateTaskOwnershipTransfers`, `store/canonical.go`) | `Fence` входа равен `Task.Fence` и `Run.Fence` — **не реализовано**: входного `Fence` у канонического command path нет | RT-06 (ADR-0004); TASK-MODEL, «Task-wide Fence и ownership» |
| писатель — текущий владелец | `RoleSelection.Session == Run.OwnerSession` (`validateTaskRunRole`, `store/canonical.go`) | `OwnerSession` входа равен `Run.OwnerSession` — **не реализовано**: `ApplyInboxCommand` не принимает сессию; в legacy это делает `coordinator.go:270` | RT-04 |
| агент известен и его роль верна | `validateTaskRunRole` (`store/canonical.go`) сверяет `Profile.Role` с `RequiredRole` и сессию с владельцем; Stage соответствует роли (`store/canonical_result.go:97`, `:110`); непустой `AgentID` обязателен там, где его требует независимость (RA-2) | actor берётся из persisted Run, а не из входа (`store/canonical_result.go:71-75`) — реализовано для Review/Audit, **не реализовано** для Executor-команд, которых ещё нет. Непустой `AgentID` для любого Task-scoped Run, принимающего агентскую команду, — норма по R116 (§5.4); правка TASK-MODEL под неё — в §6 | Нормы: CM-07, TM-16 (роль и запрет самопроверки), TM-23 и RA-2 (непустой `AgentID` для Reviewer/Auditor и для Executor-истории). Требование для всех Task-scoped Run этими нормами не установлено — оно введено решением R116 |
| ревизия не устарела | — | CAS по `Task.Revision` до изменения состояния (проверка CAS в `ApplyInboxCommand`) — **реализовано**, одна из реализованных проверок общего `ApplyInboxCommand` | TM-06 |
| переход допустим | FSM для legacy (`fsm/fsm.go:139-217`) | Stage/Status проверки result path (`store/canonical_result.go:85-90`) — реализовано для Review/Audit; для Executor-команд канонического пути нет | TM-07; TM-12; TM-16 |
| поколение сессии актуально | **нет представления в модели** | **не реализовано**: сверка вводится вместе с агрегатом `AgentSession` (R114, §5.1) | CM-10; RT-04 |

Итог таблицы различает два канонических входа, и различие существенно.

- **Общий `ApplyInboxCommand`** (`store/canonical.go`) принимает только
  `actorID`, `messageID`, `TaskID` и `expectedRevision`. Из проверок входящей
  команды он реализует две: формат `TaskID` и CAS ревизии. Ни `RunID`, ни `Fence`, ни `Attempt`, ни сессию он не принимает, а
  значит не сверяет вход с Run, эпохой, попыткой и владельцем.
- **Специализированный путь Review/Audit** (`RecordReviewResult`,
  `RecordAuditResult`, `store/canonical_result.go:17`, `:26`) принимает `runID` и
  сверяет, что Run существует, относится к этой Task и несёт `RoleSelection` с
  непустым `AgentID` (`:71-75`, `:78`); actor берётся из Run, а не из входа.
  Дальше он проверяет Stage и роль (`:97`, `:110`), нетерминальность Run и его
  статус (`:88`), время результата (`:91-93`). Это реальные проверки входящей
  команды, но не в общем пути и не для Executor-команд, которых ещё нет.
- **Чего нет нигде:** сверки входного `Task.Fence`/`Run.Fence`, `Attempt` и
  аутентифицированной сессии. В legacy их делает `Coordinator.Handle`
  (`coordinator.go:260`, `:265`, `:270`), но это не канонический путь.

Итог: канонический путь делится между собой — общий вход проверяет ревизию и
формат, специализированный проверяет роль и принадлежность Run, а сверка эпохи,
попытки и сессии отсутствует в обоих. Это расхождение с разделом «Task-wide Fence
и ownership» TASK-MODEL и с RT-06 в ADR-0004 и есть предмет проекта.

### 2.1. Как fencing отсекает старого writer

Цепочка полностью опирается на существующие правила, новое правило здесь не
вводится:

1. Передача владения повышает `Task.Fence` ровно на 1 и записывает marker
   `TASK_OWNERSHIP_TRANSFERRED` в тот же commit
   (`validateTaskOwnershipTransfers` и `requireOwnershipTransferEvent`, `store/canonical.go`).
2. Новый writer получает `Run.Fence == Task.Fence`
   (`validateTaskOwnershipTransfers`); startup-проверка требует того же для
   активного Run (`ValidateCanonicalState`).
3. Входящая команда старого writer несёт прежний `Run.Fence`. Сверка входа с
   текущим `Task.Fence` и `Run.Fence` отклоняет её до любых побочных эффектов.
   Legacy-путь делает это раньше owner- и dedup-проверок
   (`coordinator.go:258-269`), и это правило переносится в каноническую
   границу.
4. `Run.Fence` у нетерминального Task-scoped Run не может измениться иначе,
   чем при recovery transfer со сменой `OwnerSession`
   (`validateTaskOwnershipTransfers`); terminal Run не меняет owner.
5. Recovery сохраняет RunID и Attempt (recovery-ветка `validateTaskOwnershipTransfers`), то есть
   fencing не подменяет Run, а выдаёт новую эпоху тому же Run.

Ограничение, которое не закрывает fencing: он доказывает актуальность эпохи,
но не доказывает, что старый процесс остановлен. Stop proof — отдельное
требование TASK-MODEL о stop proof и RT-06 в ADR-0004, реализуемое через
supervisor.

### 2.2. Повторная доставка

- Повторная доставка не меняет Attempt: `Attempt` неизменен у Task-scoped Run
  (`ValidateCanonicalTransition`), а transport retry сохраняет RunID и Attempt
  по TM-19. Ни одна проверка §3 не пишет `Attempt`.
- Повтор той же команды с тем же `(actorID, messageID)` возвращает прежний
  receipt и ничего не пишет: проверка replay в начале `ApplyInboxCommand` —
  при существующем
  receipt с тем же `TaskID` и тем же `expectedRevision` функция возвращает
  `(receipt, true, nil)`, не вызывая handler и не вызывая `Save`. При
  расхождении `TaskID` или `expectedRevision` — отказ. Это и есть требуемое
  поведение; шаг 9 в §3 фиксирует его как правило. Порядок относительно проверки
  эпохи решён R115 (§5.3): проверки идентичности и эпохи идут до replay.
- Telepathy может повторить доставку после рестарта (`telepathy/client.go:20-21`),
  поэтому идемпотентность по message ID обязана быть durable, а не только
  in-memory.

### 2.3. Transport delivery — не подтверждение

Доставка через Telepathy и успешный `RESULT_ACK` не означают принятия работы.
`RESULT_ACK` подтверждает исходящее подтверждение агенту, а не остановку
процесса (TASK-MODEL, раздел «Task-wide Fence и ownership»). Канонический
result commit — это факт принятия
результата Coord; принятие результата не создаёт следующего writer Run без stop
proof (TASK-MODEL, там же). Legacy-путь дедуплицирует доставку по `Seen` и
повторно отправляет `RESULT_ACK` (`coordinator.go:286-292`, `:360-378`), что
подтверждает: доставка и принятие — разные факты, и обе стороны это уже
различают в коде.

## 3. Порядок проверок входящей агентской команды

Шаги разделены на две группы, и различие между ними принципиально.

**Группа A — проверки кадра, не зависящие от состояния.** Шаги 1–3. Они не
читают каноническое состояние, поэтому им не нужна сериализация с store. Сегодня
они выполняются в transport-слое до границы команд: `envelope.Verify` вызывается
в `transport.SignedClient.Incoming` (`transport/signed.go:60`) и кадр без
верной подписи не доходит до adapter; сверка адресата есть в
`Coordinator.Handle` (`coordinator.go:234`). То есть шаги 1–3 в текущем дереве —
это факт кода, а не целевая конструкция этого проекта.

**Группа B — проверки, зависящие от состояния.** Шаги 4–12. Они обязаны читать
актуальный `Task`, `Run` и ownership, поэтому должны выполняться под одной
сериализованной границей: эпоха и роль могут измениться между двумя вызовами, и
команда была бы авторизована по устаревшему снимку. Это тот же довод, что в
`COMMAND-BOUNDARY.md:293-296`.

### 3.1. Как устроен `ApplyInboxCommand` сейчас (факт кода)

`ApplyInboxCommand` сериализует вызов под `s.mu` и делит работу
на три части, и они не совпадают с шагами 4–12 по расположению:

| Часть | Где | Что делает |
|---|---|---|
| до callback | начало `ApplyInboxCommand` | валидация аргументов, replay по `InboxKey(actorID, messageID)`, CAS по `Task.Revision` |
| callback | тело `ApplyInboxCommand` | копия состояния и вызов `apply` |
| после callback | хвост `ApplyInboxCommand` | проверки receipt, вставка Inbox, snapshot и transition validation, `Save`, публикация в память |

Отсюда три следствия, важных для проекта:

1. Replay (шаг 9) и CAS по ревизии (шаг 10) выполняются **до** callback, а
   атомарный commit (шаг 12) — **после**. Значит группа B не помещается в
   callback.
2. `actorID` — входной аргумент `ApplyInboxCommand`, а не результат проверки: он
   нужен для
   построения `InboxKey` и replay, то есть до чтения состояния. Шаг 8 (актор из
   persisted `RoleSelection`) не может произойти внутри текущего callback.
3. Шаг 5 (эпоха) должен идти раньше replay (R115, §5.3), а обёртка даёт replay
   раньше любой сверки с `Run`.

Вывод: текущая обёртка не выражает требуемый порядок. Ниже он описан как цель,
а не как описание существующего кода.

### 3.2. Целевая форма границы (design этого проекта)

Требуется отдельный метод store под той же блокировкой `s.mu`, который
выполняет шаги 4–12 в указанном порядке, а мутацию Task/Run/DomainEvent/Outbox
принимает как последний шаг. Форма (не сигнатура):

1. заблокировать `s.mu`, скопировать состояние для последующих проверок;
2. разрешить Run по `TaskID`/`RunID` и получить actor из persisted Run (R118,
   §5.6, вариант (b)) — шаги 4, 6, 7, 8 выполняются **до** построения
   `InboxKey`;
3. проверить эпоху записи (шаг 5) — по R115 (§5.3) она идёт до replay;
4. построить `InboxKey(actorID, messageID)` и выполнить replay (шаг 9): при
   найденном receipt сверить его `TaskID` и исходный `expectedRevision` и вернуть
   прежний receipt без записи;
5. если receipt отсутствует, сравнить `expectedRevision` с текущим
   `Task.Revision` (шаг 10) — CAS стоит **после** replay: после первого commit
   `Task.Revision` уже увеличена, поэтому повтор той же команды с исходным
   `expectedRevision` обязан вернуть прежний receipt, а не быть отклонённым как
   устаревший;
6. проверить допустимость перехода (шаг 11);
7. вызвать handler, проверить receipt, вставить Inbox, выполнить snapshot и
   transition validation, сделать один `Save` и опубликовать в память (шаг 12).

Инвариант этой формы: `InboxKey` строится только из actor, полученного из
persisted Run в той же критической секции. Поэтому команда не может предъявить
«свой» `actorID` ни аргументом, ни телом сообщения. Текущая сигнатура
`ApplyInboxCommand` этого не обеспечивает — `actorID` приходит снаружи,
и форма §3.2 не реализована нигде в дереве; это проект, а не описание кода.

Рассмотренные варианты реализации (R118 выбрал (b); (a) сохранён как история
выбора, §5.6):

- **(a) подтверждение actor до границы, привязка под блокировкой.** Actor
  заранее аутентифицирован по доверенному реестру сессий (§5.2) и приходит в
  команду уже как подтверждённый principal; под блокировкой проверяется только
  его привязка к Run (`RoleSelection.AgentID`/`Session`) и остальные шаги 4–11.
- **(b) разрешение actor внутри нового метода.** Обязательный вход — идентификация
  аутентифицированной сессии, подпись которой уже проверена группой A (шаг 3).
  Метод читает persisted Run, сверяет эту сессию с `Run.OwnerSession` и с
  поколением сессии (R114, §5.1), а actor берёт из `Run.RoleSelection.AgentID`;
  тогда `actorID` не является входным значением вообще. `RunID` может приходить дополнительным селектором, но сам по себе он
  ничего не доказывает: метод сверяет его с тем Run, который принадлежит
  аутентифицированной сессии, и отказывает при несовпадении. Вход без
  аутентифицированной сессии, даже с верным `RunID`, отвергается.

Обе формы требуют изменения API store, а не только добавления проверок; принята
форма (b) — R118.

#### 3.2.1. Уточнённый порядок проверки (детализация R121 шаги 1–2)

OWNER уточнил первые два шага границы; это детализация R121 (CM-15, TM-29), а не
новый порядок. Полная последовательность:

1. transport проверяет подпись кадра и формирует `AuthenticatedFrame`;
2. ищется `AgentSession` по зарегистрированной привязке;
3. проверяется, что привязка активна — отозванная привязка не авторизует команду;
4. сверяются адрес, отпечаток ключа и generation;
5. только после этого команда получает trusted `AgentID`;
6. проверяются актуальность Run и `Task.Fence`;
7. проверяются роль и Stage;
8. выполняется replay и дедупликация;
9. для новой команды — проверка `Revision` и допустимости перехода;
10. один durable commit.

Практическое следствие для формы (b): transport может проверять подпись ключом из
реестра `AgentSession`, но проверка активности привязки, адреса, отпечатка и
поколения повторяется внутри нового метода под той же блокировкой (R118), поэтому
отзыв или ротация между подписью и обработкой приводит к отказу до replay.
Повтор команды через отозванную привязку не возвращает прежний receipt (R122).

Целевая конструкция проекта — группа B плюс требование к шагам 1–3 «проверка
кадра обязательна, но она не авторизация и не заменяет ни одного шага группы
B». Именно это различает границу команд от аутентификации канала, в отличие от
формулировки «всё внутри `apply`».

Каждый отказ имеет свой код ошибки. Отказ ничего не пишет, кроме диагностической
метрики.

1. **Синтаксис и идентичность команды (группа A).** `envelope.Validate`; непустые
   `messageID`, `TaskID`, валидный формат `TaskID`; ровно один payload. Отказ:
   `AGENT_COMMAND_MALFORMED`.
2. **Адресат (группа A).** `To` равен собственной сессии daemon. Отказ:
   `AGENT_COMMAND_WRONG_RECIPIENT`. Сегодня это `coordinator.go:234`.
3. **Аутентификация кадра (группа A).** Подпись валидна для ключа сессии; ключ
   закреплён за тем адресом, который заявлен в кадре
   (`envelope/signature.go:48`). Выполняется в transport-слое до границы; при
   отказе кадр не доходит до command handler. Сегодня это
   `transport.SignedClient.Incoming` (`transport/signed.go:60`). Отказ:
   `AGENT_COMMAND_UNTRUSTED_SIGNATURE`. Эта проверка доказывает, что кадр
   подписан ключом сессии, и не заменяет шаги 4–12.
4. **TaskID и Run (группа B).** Task существует; `RunID` команды существует, привязан к
   этому `TaskID` и не terminal; это единственный активный writer этой Task
   (`ValidateCanonicalState`). Отказ: `AGENT_COMMAND_UNKNOWN_TASK` и
   `AGENT_COMMAND_NOT_CURRENT_WRITER`.
5. **Эпоха записи (группа B).** `Run.Fence` из команды равен `Run.Fence` и `Task.Fence`
   snapshot. Отклоняются и меньший, и больший Fence: эпоха не секрет, и
   «будущая» эпоха невалидна независимо от отправителя. Отказ:
   `AGENT_COMMAND_STALE_FENCE` и `AGENT_COMMAND_FUTURE_FENCE`. Наследует
   `coordinator.go:258-269`.
6. **Attempt (группа B).** `Attempt` из команды равен `Run.Attempt` и равен текущему
   проходу Executor (`validateTransferredAttempt`). Отказ:
   `AGENT_COMMAND_ATTEMPT_MISMATCH`.
7. **Сессия и поколение (группа B).** `OwnerSession` из команды равен `Run.OwnerSession`
   и `RoleSelection.Session`. Сверка поколения сессии вводится вместе с агрегатом
   `AgentSession` (R114, §5.1): поколение из входа должно совпадать с поколением,
   записанным в Run при назначении. Отказ: `AGENT_COMMAND_WRONG_SESSION` и
   `AGENT_COMMAND_STALE_GENERATION`.
8. **Актор и роль (группа B).** Актор берётся из persisted `RoleSelection.AgentID`,
   а не из тела команды (`store/canonical_result.go:71-75`, `:78`); роль
   соответствует `RequiredRole` (`validateTaskRunRole`) и текущему Stage
   (`store/canonical_result.go:97`, `:110`); независимость Reviewer/Auditor по
   `AgentID` — смежная проверка RA-2, здесь не дублируется. Отказ:
   `AGENT_COMMAND_WRONG_ROLE`, `AGENT_COMMAND_STAGE_ROLE_MISMATCH`.

   Порядок относительно replay: actor разрешается внутри нового метода до
   построения `InboxKey` (R118, §5.6, форма (b); §3.2, шаг 2). Текущий API этого
   не позволяет: `actorID` приходит аргументом до блокировки
   (`ApplyInboxCommand`, §3.1), и callback не может получить actor впервые.

   Непустой `AgentID` обязателен для любого Task-scoped Run, принимающего
   агентскую команду (R116, §5.4). В Accepted ADR и TASK-MODEL это правило для
   Executor не установлено; правка TASK-MODEL под него — в §6. Отказ при пустом
   `AgentID`: `AGENT_COMMAND_MISSING_AGENT_ID`.
9. **Повторная доставка (группа B).** Если `(actorID, messageID)` уже есть в Inbox, а
   `TaskID` и `expectedRevision` совпадают — вернуть прежний receipt, ничего не
   писать (проверка replay в начале `ApplyInboxCommand`). Расхождение `TaskID` или
   `expectedRevision` — отказ `AGENT_COMMAND_REPLAY_MISMATCH`. Шаг 9 следует за
   шагами 4–8: по R115 (§5.3) проверки идентичности и эпохи идут до replay, а CAS
   ревизии (шаг 10) идёт после него. Текущая обёртка требование об идентичности и
   эпохе до replay не выполняет; порядок replay → CAS в ней уже правильный.
10. **Ревизия Task (группа B).** Для новой команды `expectedRevision` равен
    текущему `Task.Revision` (проверка CAS в `ApplyInboxCommand`). Шаг выполняется
    только если receipt не найден на шаге 9. Отказ: `REVISION_CONFLICT`
    (существующий `RevisionConflictError`).
11. **Допустимость перехода (группа B).** Stage/Status, terminal-состояние Run,
    необратимость `RESULT_READY`, монотонность прогресса
    (`store/canonical_result.go:85-90`, `fsm/fsm.go:168-174`). Отказ:
    `AGENT_COMMAND_TRANSITION_FORBIDDEN`.
12. **Один commit (группа B).** Изменение Run и Task, DomainEvent, Inbox receipt и
    Outbox фиксируются одним commit до публикации в память
    (хвост `ApplyInboxCommand`). Отказ на любом шаге выше не вызывает
    `Save`. Шаг выполняется после callback, а не внутри него (§3.1); в целевой
   форме §3.2 это последний шаг.

Порядок не сводится к «сначала дедупликация»: проверки идентичности и эпохи
(шаги 4–8) идут раньше повторной доставки (шаг 9) именно потому, что команда с
устаревшей эпохой не должна получать успешный ответ «уже применено». CAS ревизии
(шаг 10), наоборот, идёт после replay: повтор той же команды с исходным
`expectedRevision` обязан вернуть прежний receipt, и только новая команда
сверяется с текущей `Task.Revision`. Целевой порядок задан в §3.2 и решён R115.
Сегодня `ApplyInboxCommand` выполняет replay до любой сверки с Run (проверка
replay в начале `ApplyInboxCommand`),
то есть порядок replay → CAS у него уже верный, а проверок идентичности и эпохи
перед replay нет. Реализация затрагивает `store/canonical.go` (владелец — Codex)
и выполняется как отдельный кусок P0-3.

Правило канала: control token и факт успешной доставки не являются авторизацией
и не заменяют ни одного шага (§2.3).

## 4. Тесты и мутации

На каждый пункт §3 — отрицательный тест, в котором нарушено ровно это условие,
а все остальные выполнены. Тест проверяет конкретный код отказа и то, что
состояние, `Task.Fence`, `Attempt` и `Revision` не изменились.

| Условие | Тест (целевой) | Мутация, которую тест обязан убить |
|---|---|---|
| 1 | `TestAgentCommandRejectsMalformedEnvelope` | проверка синтаксиса команды отключена |
| 2 | `TestAgentCommandRejectsWrongRecipient` | сверка `To` отключена |
| 3 | `TestAgentCommandRejectsUntrustedSignature` | проверка подписи кадра отключена или ключ ищется по адресу из тела |
| 4 | `TestAgentCommandRejectsUnknownTaskOrRun` | ссылка на несуществующий Task или Run не отвергается |
| 4 | `TestAgentCommandRejectsNonCurrentWriter` | проверка «единственный активный writer» отключена |
| 5 | `TestAgentCommandRejectsStaleTaskFence` | сверка входного `Fence` с `Task.Fence` отключена |
| 5 | `TestAgentCommandRejectsFutureTaskFence` | приведённый эпохой Fence принимается |
| 5 | `TestAgentCommandRejectsFenceCheckBypassedByKnownMessageID` | ранее виденный `messageID` обходит проверку эпохи |
| 5 | `TestTaskFenceIsAuthoritativeAndMonotonic` (цель ROADMAP) | `Run.Fence` трактуется как авторитетный, а `Task.Fence` как производный |
| 6 | `TestAgentCommandRejectsAttemptMismatch` | сверка `Attempt` отключена |
| 6 | `TestRecoveryAcrossRunsRejectsOldTaskFence` (цель ROADMAP) | recovery сохраняет старую эпоху |
| 7 | `TestAgentCommandRejectsForeignOwnerSession` | сверка `OwnerSession` отключена |
| 7 | `TestAgentCommandRejectsStaleSessionGeneration` (вместе с R114) | сравнение поколения сессии отключено |
| 8 | `TestAgentCommandIgnoresActorIDFromBody` | actor берётся из тела команды |
| 8 | `TestAgentCommandRejectsRoleStageMismatch` | проверка соответствия роли Stage отключена |
| 8 | `TestAgentCommandRejectsMissingAgentIDForEveryRole` (R116) | непустой `AgentID` требуется только для Reviewer/Auditor |
| 8 | `TestAgentCommandAppliesRoleIndependenceFromCommittedRun` | сверка `AgentID` с назначенной ролью отключена |
| 9 | `TestAgentCommandReplayReturnsPriorReceiptWithoutWriting` | повторная доставка создаёт новый commit или второй event |
| 9 | `TestAgentCommandReplayAfterRevisionGrowthReturnsPriorReceipt` | повтор уже применённой команды после роста `Task.Revision` отклоняется как устаревший вместо возврата прежнего receipt |
| 9 | `TestAgentCommandReplayWithDifferentExpectedRevisionRefused` | replay с чужим `expectedRevision` принимается |
| 9 | `TestAgentCommandReplayDoesNotChangeAttempt` | повторная доставка меняет `Attempt` |
| 10 | `TestAgentCommandRejectsStaleRevision` | сравнение `Revision` отключено |
| 11 | `TestAgentCommandRejectsForbiddenTransition` | FSM-проверка перехода отключена |
| 12 | `TestAgentCommandCommitsEventReceiptOutboxAtomically` | событие или receipt пишутся вторым commit |
| 12 | `TestAgentCommandRefusalLeavesDurableStateUntouched` | отказ вызывает `Save` или публикует состояние |
| порядок | `TestAgentCommandChecksEpochBeforeReplay` (R115) | шаги 4–8 перенесены после шага 9 |
| порядок | `TestAgentCommandKeepsRevisionCheckAfterReplay` (R115) | CAS ревизии перенесён до replay |
| форма API | `TestAgentCommandBoundaryResolvesActorInsideLock` (R118, вариант (b)) | actor принимается аргументом и не сверяется с persisted Run |
| форма API | `TestAgentCommandBoundaryRejectsActorUnboundToRun` | аутентифицированная сессия принимается для чужого Run |
| форма API | `TestAgentCommandBoundaryRejectsKnownRunIDWithoutAuthenticatedSession` (R118) | верный `RunID` без аутентифицированной сессии принимается как разрешение на запись |
| форма API | `TestAgentCommandBoundaryRejectsInboxKeyBuiltFromUnverifiedActor` | `InboxKey` строится до сверки actor с Run |
| порядок | `TestAgentCommandRejectsUnknownRunBeforeReplayLookup` | replay ищется по RunID вместо reject |
| канал | `TestAgentCommandTransportDeliveryIsNotAcceptance` | успешная доставка или `RESULT_ACK` трактуется как принятие |
| канал | `TestAgentCommandValidSignatureWithoutRunOwnershipIsRefused` | подпись проверяется, а Run-принадлежность нет |
| recovery | `TestAssignTickRecoveryFailClosedWithMultipleWriters` (цель ROADMAP) | второй активный writer допускается |

Тесты используют fake clock, fake transport и существующие in-memory
персистентности `CanonicalPersistence` (интерфейс `store.CanonicalPersistence`,
`store/canonical.go`). Ни один
тест не принимает control token, legacy `SessionAuth` или факт доставки как
замену проверке эпохи и принадлежности Run.

## 5. Вопросы на решение OWNER

### 5.1. Как представлено поколение сессии агента

Решено: R114, вариант A. Дополнено подтверждёнными OWNER R122 (смена адреса
сессии), R123 (старый canonical state без `AgentSession`), R125 (ротация или отзыв
при активном Run) и R126 (повторная регистрация отозванного адреса запрещена).

Границы поколения, зафиксированные R122: поколение означает только ротацию ключа в
той же привязке. Смена адреса сессии ротацией не маскируется — старая привязка
отзывается отдельным событием, а новый адрес регистрируется как новая
`AgentSession`. Отозванная привязка не авторизует команду: команда, пришедшая через
неё, отклоняется до replay, а не после. История хранит адрес, ключ, время действия и
причину отзыва.

Состояние без привязки по R123: старый canonical state с активным
command-capable Run без действующей `AgentSession` блокирует readiness fail-closed с
понятной диагностической причиной; автоматического backfill и новой
recovery-процедуры сейчас нет, файл автоматически не переписывается, `AgentID`,
поколение, ключ и адрес задним числом не заполняются. До подключения канонического
agent command path такие состояния штатным путём не создаются. Owner recovery
command (stop proof, завершение старого Run, новое назначение из свежей
`AgentSession`) — запланированное продолжение P0-3; до отдельного проекта и решения
он не реализуется.

Ротация ключа или отзыв `AgentSession` при активном Run по R125 разрешены: такой
Run огорожен, его команды отклоняются до replay, readiness не блокируется, а
переназначение идёт только через recovery transfer на текущую активную привязку.
R125 относится к Run, у которого привязка и поколение были записаны при назначении,
а затем устарели или привязка была отозвана; R123 — к Run без записанной привязки.
Отклонение команд до replay реализовано в канонической границе
`ApplyAgentCommand` (`store/canonical_agent_command.go`); не подключён
production command path.

Повторная регистрация отозванного адреса по R126 запрещена: адрес, уже бывший в
истории, не регистрируется заново, а вернувшийся агент регистрируется на новом
адресе с прежним `AgentID`. Основание — ABA: регистрация того же адреса с
поколения 1 дала бы пару «адрес + поколение», совпадающую с огороженным по R125
Run, и ограждение снялось бы. Код уже это обеспечивает: `RegisterAgentSession` и
`ReplaceAgentSessionAddress` отклоняют адрес, бывший в истории
(`ErrAgentSessionAddressExists`), а `agentsession.ValidateHistory` запрещает события
после отзыва; проверки — `TestCanonicalAgentSessionAddressChangeRevokesAndRegistersInOneCommit`
и `TestCanonicalAgentSessionAddressChangeRejectsInactiveOrExistingAddressWithoutWrite`
(`store/canonical_agentsession_test.go`), `TestValidateHistoryRejectsEventsAfterRevocation`
(`agentsession/session_test.go`).

Норма требует его дважды: TASK-MODEL (раздел «Task-wide Fence и ownership») и
RT-04 в ADR-0004, плюс CM-10 в ADR-0001. В модели его нет (§1.4). Что можно
сделать:

- **A (принято, R114).** Канонический агрегат сессии агента: `AgentSession` с
  `AgentID`, адресом сессии, монотонным поколением и отпечатком публичного
  ключа; регистрация и смена поколения — отдельные события в собственном потоке,
  по образцу `OwnerCredential` из `COMMAND-BOUNDARY.md` §3.2 (R110). Схема v1
  расширяется по образцу R85, как для R108. Список целевых канонических
  сущностей ADR-0001 правится явно со ссылкой на решение (правка — §6.2).
  Поколение записывается в `Run` при назначении и сверяется на каждой входящей
  команде.
- **B.** Не вводить агрегат, а использовать `roles.Selection.Revision`
  (`roles/roles.go:322`) как замену поколения. Не рекомендуется: это ревизия
  реестра профилей, она меняется при изменении профиля и не меняется при
  переподключении, то есть не выражает freshness сессии.
- **C.** Считать поколение эквивалентом эпохи записи: отказаться от отдельной
  проверки, оставив `Run.Fence` + `OwnerSession`. Не рекомендуется: это молча
  ослабляет раздел «Task-wide Fence и ownership» TASK-MODEL и RT-04 в ADR-0004,
  а расхождение Accepted ADR с нормой
  разбирает OWNER, а не проект.

Остаточный риск, не снимаемый ни одним из вариантов: процесс под тем же OS user
может менять файлы состояния напрямую. Граница защищает канал команд, а не
файлы; полную защиту даёт отдельная OS identity агентов (P0-5, R70).

### 5.2. Как агент доказывает свою сессию через Telepathy

Решено: R114, вариант A.

Факты (§1.3): подпись ed25519 проверяется ключом, закреплённым за адресом
сессии (`envelope/signature.go:48`); ключ и пины пиров приходят из конфига
(`internal/telepathyhost/config.go:38-39`, `:72-73`); `list_peers` подтверждает
присутствие сессии (`telepathy/factory.go:48-55`); Telepathy сам не
аутентифицирует агента в коде репозитория (UNKNOWN). Вывести из норм
требование «агент предъявляет ключ, зарегистрированный Coord» нельзя: RT-04
требует проверки аутентификации и capability, но не определяет реестр.

- **A (принято, R114).** Реестр привязок в каноническом состоянии Coord:
  `(AgentID, адрес сессии, поколение, публичный ключ или его отпечаток)`.
  Регистрация — операторское действие на границе Owner-проекта, а не сообщение
  агента (`roles/roles.go:181-182` уже фиксирует, что `agent_id` не меняется
  сообщениями). Назначение Run записывает поколение; входящая команда сверяет и
  адрес, и поколение, и подпись.
- **B.** Считать достаточным закрепление ed25519-ключа адресом сессии в конфиге
  и не хранить привязку в Coord. Не рекомендуется: конфиг — файл под тем же OS
  user, доступный агенту, и такая проверка не связывает адрес с `AgentID` и не
  имеет поколения.
- **C.** Поручить доказательство внешнему supervisor (например, PID и
  process generation в stop proof). Не рекомендуется как основа capability:
  supervisor по ADR-0004 не владеет Task workflow; его доказательство нужно для
  передачи владения, а не для каждой записи.

### 5.3. Порядок replay-проверки и проверки эпохи

Решено: R115, вариант A.

Цель P0-3 в ROADMAP (строка «P0-3 | Production write path через daemon»)
требует, чтобы «входящее сообщение старого Run с прежним Task Fence
отвергалось», и требует проверки входного `Task.Fence` до dedup и
ownership-побочных эффектов. TASK-MODEL, раздел «Task-wide Fence и ownership»,
требует того же: входящая
агентская запись принимается только при совпадении с текущим ownership и текущим
`Task.Fence`. Сегодня `ApplyInboxCommand` проверяет replay первым и возвращает
прежний receipt команде с устаревшей эпохой (проверка replay в начале
`ApplyInboxCommand`).

- **A (принято, R115).** Проверки идентичности и эпохи идут до replay; при
  успешной проверке повтор возвращает прежний receipt без записи. Поведение
  идемпотентности сохраняется, а устаревший writer перестаёт получать успешные
  ответы. CAS ревизии при этом остаётся после replay: перенос CAS до replay
  сломал бы идемпотентность, потому что после первого commit `Task.Revision` уже
  увеличена и повтор с исходным `expectedRevision` был бы отклонён как устаревший
  вместо возврата прежнего receipt. Реализуемо только в форме §3.2, то есть новым
  методом под той же блокировкой `s.mu`: текущая обёртка (проверка replay в начале
  `ApplyInboxCommand`, затем CAS по `Task.Revision`) проверок идентичности и эпохи
  перед replay не содержит.
  Изменение затрагивает `store/canonical.go` (владелец — Codex) и выполняется по
  согласованию с владельцем файла.
- **B.** Оставить replay первым и явно записать исключение в TASK-MODEL. Не
  рекомендуется: норма требует сверки входа до принятия, а исключение
  ослабляет её ровно в сценарии отложенного сообщения.
- **C.** Сделать ключ replay зависимым от эпохи (`actorID, messageID, Fence`).
  Не рекомендуется: меняет схему Inbox и не даёт выигрыша — правильный порядок
  проверок решает задачу без изменения ключа.

Дополнено подтверждённым OWNER R125 (ротация ключа или отзыв `AgentSession` при
активном Run): такой Run огорожен, его команды отклоняются до replay в порядке
R115, readiness не блокируется, переназначение идёт только через recovery transfer
на текущую активную привязку. R125 относится к Run, у которого привязка и
поколение были записаны при назначении, а затем устарели или привязка была
отозвана; R123 — к Run без записанной привязки. Отклонение команд до replay
реализовано в канонической границе `ApplyAgentCommand`; не подключён production
command path.

### 5.4. Обязателен ли `AgentID` для Task-scoped Executor

Решено: R116, вариант A.

Факт кода: `validateCanonicalRoleIndependence` допускает Task-scoped Executor без
selection (`store/canonical_independence.go:60-64`) — назначение записывается без
`AgentID`, и проверка идёт дальше. Непустой `AgentID` требуется только там, где
проверка независимости опирается на Executor-назначение, и для самого
Reviewer/Auditor; отсутствие Executor-назначений само по себе допустимо.
`RecordReviewResult`/`RecordAuditResult` требуют `RoleSelection` с непустым
`AgentID` (`store/canonical_result.go:71-75`). Нормы, требующей непустого `AgentID`
для Executor, в Accepted ADR, TASK-MODEL и решениях OWNER нет: TM-23 и CM-03
обязывают независимость Reviewer/Auditor, а не Executor; правило ADR-0001 о
Discussion facts требует присвоения author/role Coord из аутентифицированной
сессии.

Значит, это новое правило, а не заимствованное. Варианты:

- **A (принято, R116).** Ввести канонический агентский `Session`/assignment (§5.1,
  §5.2) и требовать непустой `AgentID` для любого Task-scoped Run, принимающего
  агентскую команду. Executor без selection перестаёт быть допустимым для
  записи; это меняет модель, а не только проверку, поэтому требует R-решения и,
  вероятно, правки ADR-0001 (список сущностей) и TASK-MODEL.
- **B.** Оставить текущее правило: непустой `AgentID` обязателен только там, где
  его требует независимость. Тогда actor для команд Executor без selection берётся
  из transport-сессии, и receipt связывает адрес сессии, а не стабильного агента.
  Проще, но ослабляет трассируемость: в `InboxReceipt.ActorID` попадёт то, что не
  является устойчивым principal.
- **C.** Запретить Executor без selection только на границе команд: непустой
  `AgentID` требуется для записи, но допустимый persisted Run без selection не
  считается дефектом. Промежуточный вариант: сохраняет текущую валидацию state и
  вводит правило на command boundary.

Принято R116 — вариант A. Вариант B не принимался: он сделал бы `ActorID` в
receipt величиной без устойчивого владельца. Вариант C сохранён как возможное
локальное ограничение, если реализация не успевает за R116; по принятому решению
обязательным остаётся A.

### 5.5. Граница Executor RESULT

Решено: R117, вариант A.

Канонического Executor RESULT command нет (§1.1), а RT-04 требует, чтобы все
agent writes шли через daemon command boundary. Варианты:

- **A (принято, R117).** Этот проект задаёт границу и порядок проверок (§2, §3)
  для всех команд агента, включая `ACK`, `HEARTBEAT` и `RESULT`; реализация
  канонического Executor RESULT остаётся отдельным куском P0-3 вместе с
  переводом `fsm.Run.Apply` на канонический commit. До этого agent write path
  для Executor не подключается — как и сейчас.
- **B.** Включить реализацию Executor RESULT в этот проект. Тогда проект
  перестаёт быть проектом границы и становится куском реализации; кроме того,
  это затронуло бы `fsm/fsm.go` и `coordinator.go` шире, чем граница команд.
- **C.** Оставить legacy `Coordinator.Handle` единственным путём для
  Executor-команд. Не рекомендуется: это второй write path в обход канонической
  границы, прямо запрещённый RT-04.

Переключение уточнено R124. При подключении канонического Executor RESULT
legacy-путь закрывается для всех Run с непустым `TaskID`: для Task-scoped Run
канонический command path — единственный, и fallback в legacy при ошибке запрещён.
Для Run без `TaskID` legacy-путь временно сохраняется как совместимость, а не
второй production write path для Task; полное удаление требует отдельного решения
после появления работающей замены.

### 5.6. Как подтверждается actor до построения `InboxKey`

Решено: R118, вариант (b).

Факт кода: `ApplyInboxCommand` принимает `actorID` аргументом
(`ApplyInboxCommand`) и использует его для `InboxKey` и replay до чтения
состояния (проверка replay в начале `ApplyInboxCommand`). Поэтому actor не может
быть впервые получен из persisted
Run внутри текущего callback — ни шаг 8, ни «эпоха раньше replay» (§5.3) через
эту сигнатуру не выражаются. Целевая форма §3.2 требует нового метода store.

Выбор, который должен сделать OWNER (связан с источником mapping из §5.2):

- **(a) подтверждение actor до границы, привязка под блокировкой (рекомендация
  при принятии §5.2 A).** Actor подтверждается доверенным реестром сессий вне
  command boundary и приходит уже как principal; под блокировкой проверяется
  привязка к Run и остальные шаги 4–11. Плюс: реестр — единственное место,
  где решается «кто этот агент», и он совпадает с §5.2 A. Минус: store получает
  principal извне, и корректность зависит от качества реестра.
- **(b) разрешение actor внутри нового метода (принято, R118).** Обязательный
  вход — аутентифицированная сессия (подпись проверена группой A); `RunID` —
  только дополнительный селектор. Метод сверяет сессию с `Run.OwnerSession` и поколением
  (после решения §5.1), затем берёт actor из `Run.RoleSelection.AgentID`.
  Плюс: `actorID` вообще не является входным значением, и подделать его нельзя;
  знание чужого `RunID` не даёт ничего, потому что сессия не совпадёт.
  Минус: требует, чтобы у Run всегда был непустой `AgentID`, то есть зависит от
  решения §5.4 и в его варианте B/C невыполним.
- **C (не рекомендуется).** Оставить `actorID` аргументом и ограничиться
  сверкой с Run под блокировкой внутри callback. Тогда replay уже построен по
  непроверенному actor, а требование §5.3 не выполняется: именно тот случай, от
  которого §5.3 и защищает. Отдельно: этот вариант принимает `actorID` как
  достаточное основание, поэтому сессия не проверяется вовсе — любой, кто может
  вызвать метод, действует от имени указанного principal.

Принято R118 — вариант (b): он сильнее гарантирует, что actor не предъявляется
командой, и совместим с R116, поскольку требует непустого `AgentID` у Run.
Вариант (a) и C оставлены как история выбора.

### 5.7. Что этот проект не меняет

- Legacy `Coordinator.Handle` остаётся как есть для legacy Run (`Run.TaskID`
  пуст). Его правила fence/attempt/dedup переиспользуются как образец, но его
  код не переписывается этим проектом.
- Независимость Reviewer/Auditor по `AgentID` — смежная проверка RA-2
  (`docs/plans/p0-review-audit/RA-2.md`, реализовано коммитом `2d0cf60`); здесь
  она только цитируется как входная гарантия шага 8. Требование непустого
  `AgentID` для всех ролей, включая Executor, этот проект не заимствует у RA-2:
  оно введено решением R116 (§5.4) и требует отдельной правки TASK-MODEL (§6).
- Модель `OwnerCredential`, challenge и подтверждение команд Owner — целиком
  `COMMAND-BOUNDARY.md`. Общих сущностей два проекта не вводят.
- `ARTIFACT` refs остаются fail-closed (`store/canonical_result.go:321-322`).
- Отдельная OS identity агентов — P0-5, R70.

## 6. Приёмка и зависимости реализации

### 6.1. Приёмка

Документ принят: ревью Codex (ACCEPTED_BY_CODEX), приёмка OWNER (R114–R118).
Критерий закрытия зависимости P0-3: в ROADMAP (строка «P0-3 | Production write
path через daemon») вместо «принятые Owner и Agent command boundary projects»
стоят ссылки на принятые версии обоих проектов, а в строке «До P0-3 | Agent
command boundary» остаётся требование «До принятия agent write path не
подключается» до подключения agent write path. ROADMAP меняет его владелец; здесь
фиксируется только сам критерий. Проект принят (Accepted, OWNER 2026-10-08,
R114–R118), и критерий закрыт этой же правкой: обе ссылки в ROADMAP проставлены
([#3](https://github.com/naghuale/coord/issues/3)).

Поля и имена, которые решения фиксируют концептуально, а реализация называет
конкретно (например, точное имя метода store из §3.2, имя поля поколения в Run и
структура событий `AgentSession`), определяются в реализации P0-3 и не должны
менять принятые решения: решения определяют порядок, источник actor и
обязательность сверок, а не имена.

### 6.2. Правки нормативных документов (отдельно, принимает OWNER)

Решения R114 и R116 меняют то, что сейчас записано в Accepted ADR и
TASK-MODEL. Эти правки не входят в реализацию автоматически и делаются
отдельными решениями OWNER; здесь фиксируется только, что они требуются.

| Правка | Что именно | Основание |
|---|---|---|
| ADR-0001, список канонических сущностей | добавить `AgentSession` рядом с `Session`, с указанием, что именно он хранит и кто регистрирует | R114 |
| ADR-0001, модель сессии и capability | зафиксировать, что поколение сессии агента сверяется на каждой входящей команде и что actor берётся из persisted Run, а не из тела команды | R114; R118 |
| ADR-0001 или TASK-MODEL, правило входящей записи | записать, что непустой `AgentID` обязателен для любого Task-scoped Run, принимающего агентскую команду | R116 |
| TASK-MODEL, «Task-wide Fence и ownership» | дополнить перечисление проверяемого при входящей записи: `TaskID`, `RunID`, `Attempt`, поколение сессии, `Run.Fence`/`Task.Fence`, принадлежность сессии и непустой `AgentID`; сейчас там перечислено без `AgentID` и без поколения | R114; R116 |
| Схема канонического состояния | расширить v1 по образцу R85 (новое поле состояния и его startup validation), отдельно для `AgentSession` и для поля поколения в `fsm.Run` | R114 |
| Событийная схема | `AgentSession` получает собственный поток событий регистрации и смены поколения, по образцу `OwnerCredential` (`COMMAND-BOUNDARY.md` §3.2) | R114 |

Реализация `AgentSession`, поколения в `Run` и правила непустого `AgentID`
начинается после того, как OWNER примет перечисленные правки ADR-0001 и
TASK-MODEL. Части, которые этих правок не требуют (порядок проверок R115,
источник actor R118 на существующих полях), могут реализовываться раньше.

### 6.3. Что перечитывается при изменении store

§3.1 фиксирует устройство `ApplyInboxCommand` на текущем дереве, а §3.2 — целевую
форму. Если `store/canonical.go` меняется, обе части перечитываются, потому что
именно они отвечают за порядок шагов 4–12 и за разрешение actor.