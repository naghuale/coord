# Дальнейшее развитие

Это план работ, а не список реализованных функций. Приоритеты учитывают текущие
границы ядра; оценки готовности production здесь намеренно не присваиваются.

Проверенные process-level сценарии вынесены в [эксперименты](EXPERIMENTS.md):
реальный MCP round-trip, P0 owner/fence transfer после остановки writer,
P1 coordinator restart и P2/P3 recovery тестового Python worker. Это не закрывает
production bridge, произвольные legacy subprocess или весь chaos checklist.

UX-1 (срез B реализован локально, A и C — нет): первый сценарий
owner и двух агентов — карточки состояний с раздельными
attachment/reachability/readiness/availability, машинный блокер quota и
`coord tui`; per-context профили коротких команд с привязкой к generation и
честный discover/onboarding не реализованы. Нормативное поведение и критерии
приёмки:
[plans/ux-first-scenario/ALIGNED_UX.md](plans/ux-first-scenario/ALIGNED_UX.md),
план (важно → нужно → как → результат): [plans/ux-first-scenario/PLAN.md](plans/ux-first-scenario/PLAN.md),
задача на реализацию: [plans/ux-first-scenario/UX_TASK.md](plans/ux-first-scenario/UX_TASK.md).
Основание: [UX_AUDIT.md](UX_AUDIT.md).
Срез B включает CLI `session block/unblock`, раздельные карточки и машинный
blocker; это не закрывает критерии полного UX-1 и не заменяет приёмку живых TUI.
Перед `unblock` checkpoint проверяется вручную; CLI/TUI не валидируют и не
фиксируют факт проверки. Это нормативное ручное действие. Автоматическая
проверка содержимого checkpoint исключена из UX-1 решением owner 2026-10-08 и
входит в P1 «Проверка содержимого checkpoint».
`source` блокера пока не аутентифицирует supervisor/integration: API требует
owner capability; scoped service identity остаётся открытой границей.

| Приоритет | Работа | Зависит от | Критерий завершения |
|---|---|---|---|
| До P0-1 | Заморозка legacy Rooms | ADR-0001 шаг 1 | `internal/rooms`, `state/rooms` и `rooms.json` поддерживаются только bugfix/stability/compatibility; запрещены новые workflow-функции, состояния и сущности |
| Выполнено локально | Telepathy 0.8.0 MCP byte bridge и standalone CLI | — | Подписанный полный lifecycle и worker restart проверены make test-telepathy-host; arbitrary frontend и canonical Crewflow отдельно |
| P0-1 | Daemon runtime skeleton | Заморозка legacy Rooms; ADR-0001 шаг 6; ADR-0004 RT-01…RT-04; ADR-0005 ST-01…ST-06 | Production executable composition, ownership namespace, lifecycle, control/metrics и process-kill acceptance для текущего Run snapshot; design: [P0-1 Daemon Runtime](plans/p0-daemon/DESIGN.md) |
| P0-2 | Canonical Task state и TaskID adoption | P0-1: закоммиченный каркас `89a77b4` удовлетворяет зависимости P0-2 по R82; полная приёмка P0-1 остаётся OPEN; ADR-0001 шаг 7; ADR-0003; ADR-0004 RT-04/RT-06; ADR-0005 ST-01/ST-04/ST-05/ST-06; TM-01…TM-04, TM-06 (storage contract) | **Выполнено** коммитами `be7820a`, `f280440`, `66c0ddf`, `8eb6e38`. Canonical snapshot schema v1 хранит Task aggregate, явную связь Task↔Run, Task.Fence, DomainEvent, Inbox и Outbox; commit атомарен, startup validation и restart/replay проверены. `OwnerID` остаётся principal reference без аутентификации; production command/write path не подключён. `runs.json` не импортируется; до P0-3 daemon удерживает compatibility lock, а неизвестная схема/активный legacy Run оставляет diagnostic-only `NOT_READY`. Проверки входного Task Fence, передачи между Runs, Assign/Tick/recovery при нескольких writers и Fence в Outbox envelope перенесены решением OWNER R100 в P0-3. Полнота Profile и реестр EventType остаются вне критерия P0-2; design: [P0-2 Canonical Task State](plans/p0-2-task/DESIGN.md) |
| После P0-2 | Review/Audit и Discussion Graph | P0-2; ADR-0001 шаги 8–9, CM-08 | ReviewResult и AuditResult — структурированные канонические результаты; конфликт Review/Audit фиксируется DomainEvent и Discussion fact до Owner escalation, решение Owner ссылается на конфликт; Discussion nodes/edges строятся из событий без второй авторитетной копии |
| После Discussion Graph | Ограниченная Shadow Validation | P0-2; ADR-0001 шаг 10 | Новая модель сверяется с legacy только по фактам с однозначным соответствием; нет двойной записи и ложных равенств `room participant = Run`, `room message = DomainEvent` |
| До P0-3 | Owner command boundary | R69/R70; решение OWNER R130 (границы побочных эффектов решения); [проект границы команд Task](plans/p0-2-task/COMMAND-BOUNDARY.md) | Проект принят OWNER как каноническая модель команд Owner над Task: агрегат `OwnerCredential`, подписанный challenge на каждую команду, защита от replay и обновление схемы (R109–R113); см. [COMMAND-BOUNDARY](plans/p0-2-task/COMMAND-BOUNDARY.md) §3 и CM-12. До принятия Owner write path не подключается |
| До P0-3 | Agent command boundary | ADR-0001 шаг 11; ADR-0004 RT-04/RT-06; P0-2 | Проект принят OWNER как каноническая модель команд агента: стабильный agent identity, поколение сессии, RunID/Attempt/TaskID/Task.Fence, ограничения роли, stale/replay handling и единая daemon command boundary (R114–R118); см. [AGENT-COMMAND-BOUNDARY](plans/p0-2-task/AGENT-COMMAND-BOUNDARY.md). До принятия agent write path не подключается |
| P0-3 | Production write path через daemon | P0-2, Review/Audit + Discussion Graph, Shadow Validation, принятые [Owner command boundary](plans/p0-2-task/COMMAND-BOUNDARY.md) и [Agent command boundary](plans/p0-2-task/AGENT-COMMAND-BOUNDARY.md) projects; ADR-0001 шаг 11; ADR-0004 RT-04; решение OWNER R100 | Все production CLI-команды и Telepathy agent writes, меняющие каноническое состояние, проходят daemon command boundary; localchat/transport adapters не пишут его напрямую. Проверяются TaskID, RunID, Attempt, generation, входной Task.Fence до dedup/ownership side effects и разрешённость команды; delivery не означает acceptance. Recovery/rework/Assign/Tick не создают второго Task writer; входящее сообщение старого Run с прежним Task Fence отвергается. Outbox envelope Fence сверяется с принятой семантикой Task.Fence при retry/recovery. Targets: `TestTaskFenceIsAuthoritativeAndMonotonic`, `TestRecoveryAcrossRunsRejectsOldTaskFence`, `TestAssignTickRecoveryFailClosedWithMultipleWriters`. Worktree и ссылки на артефакты результата принадлежат назначенному worktree. Свободный текст проходит quarantine/redaction до durable записи; history export и диагностические логи отдельно проверяются на отсутствие исходного секрета. Секрет/MFA передаётся защищённым каналом напрямую получателю, Coord хранит только безопасную ссылку и проверяемый исход (CM-06, CM-09, TM-25); отказ не меняет состояние |
| P0-4 | Production executor lifecycle и writer lock | P0-2, P0-3; ADR-0004 RT-06 | Повторный TASK не запускает второй writer; ABORT/lease loss прекращают writer; неоднозначный restart не повторяет произвольные side effects; у каждой Task свой worktree и writer lock |
| До P0-5 | Supervisor и OS identity/isolation ADR | R70; P0-1…P0-4; ADR-0004 RT-02/RT-03/RT-06 | Приняты deployment model и поддерживаемые платформы; отдельная OS identity агентов от Owner/daemon; правила доступа к owner credentials, worktree, `/tmp`, toolchain и кэшам; проверяемый stop proof, fail-closed политика без поддерживаемой изоляции и требования CI |
| P0-5 | Supervisor конкретной установки | P0-1…P0-4; принятый Supervisor и OS identity/isolation ADR; ADR-0004 RT-03/RT-06; Linux CI с поддерживаемым механизмом изоляции (в CI есть только общий `make check` на ubuntu-latest и macos-latest, `6eb78e3`); решение OWNER R70 | Fence/stop proof подтверждает остановку writer до transfer; replacement ждёт TASK; failed spawn очищается; process acceptance пройдена на целевом окружении. Supervisor запускает agent processes под отдельной OS identity, отличной от Owner и daemon; owner credentials и интерактивная авторизация недоступны агентам. Executor изолирован ОС: запись только в назначенный worktree, приватный `/tmp`, toolchain и кэши — только чтение; доступ вне worktree — только по выданному OWNER праву. Без поддерживаемой изоляции (сейчас — только Linux) executor не запускается, fail-closed; production-конфигурация не может отключить изоляцию; в тестах изоляция подставляется через интерфейс. Гарантии изоляции принимаются только из Linux CI с поддерживаемым backend; зелёные тесты на неподдерживаемой платформе изоляцию не доказывают. В обязательной CI-задаче изоляции недоступность backend — провал теста, а не `t.Skip` (переменная окружения требует изоляцию); пропуск допустим только для macOS/локальных прогонов |
| После P0-3 | Модель интеграции агентов | P0-2, P0-3; ADR-0001 шаги 11–12 | Расширяет существующий `roles.Profile` (умения агента под отдельным термином, не «capability» — это право доступа по ADR-0001) и существующий worker-adapter; профиль выдаёт оператор (P1 «Политика доверия к roles»); используется существующий `envelope`, новый transport-протокол не вводится; Startup Card выдаёт роль, objective, inputs, permissions, expected output и completion criteria из одного согласованного снимка |
| После P0-3 | Project Dependency Graph | P0-2, P0-3; ADR-0002 DG-01…DG-08 | TASK-only MVP использует канонические TaskID; ADR/MILESTONE закрыты fail-closed без доверенного каталога. Owner-команды проверяют capability и Graph Revision, отвергают циклы/висячие ссылки и атомарно сохраняют ребро с DomainEvent; снятие блокировки FAILED prerequisite связывает изменение графа с Owner decision в одном commit. Новый Executor Run ждёт завершения всех оставшихся prerequisites, а `BLOCKS` вычисляется из `DEPENDS_ON` |
| После P0-3 | External References | P0-2, P0-3; ADR-0003 EXT-01…EXT-10 | TaskID остаётся внутренним и стабильным; ссылки допускают ноль или больше объектов с уникальностью канонических `(Provider, Instance, ExternalID)`; неканоническая ссылка отвергается fail-closed. Удаление внешнего объекта не удаляет Task; изменения связи атомарны с DomainEvent; события отличают provider-confirmed delete/restore от Owner archive/reactivate. `BROKEN` вычисляется, timeout не считается удалением. Resolver observations идут через daemon command path. Синхронизация полей не входит |
| После канонических моделей | ADR-0006 API Contract | P0-1…P0-3; ADR-0001 шаг 12; ADR-0004 RT-03/RT-05; ADR-0002; ADR-0003 | Зафиксированы versioned read API и projections; write authorization не входит в ADR-0006 и до P0-3 определяется отдельными Owner/Agent command-boundary projects. Task, runs, events, sessions, roadmap, dependencies и external refs возвращают канонические ID; read endpoints не мутируют состояние, неизвестная версия/не-ready runtime отказывает согласно контракту |
| После ADR-0006 | Dashboard и Project Map | ADR-0006; решения OWNER R128, R129, R131 (границы read-only, публикации и cursor); Review/Audit, Conversation Graph, Project Dependency Graph, External References | Dashboard — read-only проекция API; Workflow, Conversation Graph и Project Map разделены; Attention/Health вычисляются, не хранятся; UI не создаёт собственных Task/Room/workflow states |
| Отдельный design | Политика внешней синхронизации | ADR-0003; External References | Канонизация publication facts отложена решением OWNER R128 до принятия модели автокоммита и публикации. Отдельно определены направления импорта/экспорта, владение полями, provenance, расписание и разрешение конфликтов; до принятия policy внешнее изменение не переписывает канонический Task |
| После Dashboard | Перевод используемых операций с Rooms | ADR-0001 шаг 14; P0-2…P0-3; ADR-0004 RT-04 | Для используемых сценариев есть daemon-backed эквиваленты `assign`, `resume`, `say`/Discussion, `checkpoint`, `ack`, `ready/regulation`, `block/unblock`; сохраняются capability scope, generation checks и идемпотентность. Участники Rooms автоматически не превращаются в Runs |
| По отдельному решению | Проверяемый импорт legacy данных | ADR-0001 шаг 15; storage/migration design | Backup до первой записи, dry-run, явный mapping `RunID → TaskID`, проверка результата и идемпотентный повтор; импорт не угадывает TaskID и роли по room/task text или участникам |
| Последний шаг, отдельное одобрение OWNER | Удаление legacy Rooms | ADR-0001 шаг 16; перевод операций завершён; данные сохранены | Все используемые операции Rooms заменены и проверены, сохранность нужных данных подтверждена; только после отдельного одобрения удаляются `internal/rooms`, `state/rooms` и legacy `rooms.json` |
| Выполнено локально | Подтверждение commit результата | — | RESULT_ACK durable вместе с RESULT; executor.Receipt фиксирует local completion; проверены retry/restart |
| P1 | Crash/chaos integration suite | P0-1…P0-5 | Убийство executor/coordinator/watchdog на каждом шаге не создаёт второго writer |
| P1 | Проверка содержимого checkpoint | не определено | Проверяются hashes файлов/артефактов, а не только base HEAD и diff whitespace |
| P1 | Порядок и replay сообщений | не определено | Определены sequence/epoch правила; поздний heartbeat не откатывает прогресс |
| P1 | Recovery после падения watchdog | не определено | Проверяемое освобождение abandoned claim без опасного автоматического expiry |
| P1 | Политика доверия к roles | не определено | Provisioning keys/agent_id, allowlists capabilities и отзыв сессий |
| P2 | Миграции snapshot/schema | не определено | Старые версии загружаются или отклоняются до side effects с понятной диагностикой |
| P2 | Transactional database | не определено | Durable state/outbox/events и эффективные запросы без полной записи snapshot |
| P2 | Нагрузка и bounded queues | не определено | Замеры на тысячах runs; transport sends не задерживают остальные run deadlines |
| P2 | Наблюдаемость эксплуатации | не определено | Health/readiness, structured logs, dashboards, измеримые SLO |
| Известное ограничение P2 | Рост дедупликации `Run.Seen` | не определено | Карта message_id растёт монотонно и очищается только при recovery, поэтому snapshot и стоимость каждого Update растут на длинных run. Окно или лимит не введены: вытеснение открыло бы задвоение RESULT при повторной доставке, а риск выше цены. Требует отдельного решения вместе с проверкой содержимого checkpoint |
| P2 | Организационная независимость | не определено | AUDITOR проверяется по trust domain/организации, помимо agent_id |

Production acceptance: реальный полный lifecycle, restart с сохранением результата,
устойчивость к повторам/потере сообщений, подтверждённое отсутствие двух writers
и эксплуатационная инструкция, проверенная на целевом окружении.

Частичные process-level доказательства P0 executor уже есть у одно-run
Telepathy worker: повторный TASK дедуплицируется, неоднозначный restart не
переисполняется, ABORT и потеря heartbeat renewal останавливают writer process
group; см. [VERIFICATION.md](VERIFICATION.md). Это не закрывает production
executor lifecycle: worker привязан к одной заранее настроенной Run, не подключён
к canonical Task daemon, а произвольные daemonizing subprocess остаются вне его
гарантий.

`coordd` теперь восстанавливает canonical Task state и Outbox вместе с Run state;
P0-2 завершён и подтверждён коммитами, указанными в таблице. P0-1 process
acceptance на реальной composition покрывает SIGKILL/restart, отказ второго
writer, Run/outbox replay до readiness, сохранение RESULT receipt и SIGTERM
shutdown. Полная приёмка P0-1 остаётся OPEN до общей проверки репозитория и
согласования всех эксплуатационных критериев. Текущие process tests не доказывают
production Task command path, supervisor stop proof, изоляцию ОС или полный
production lifecycle executor; эти границы остаются в P0-3…P0-5. См.
[P0-1 Daemon Runtime](plans/p0-daemon/DESIGN.md) и
[проверяемые сценарии](VERIFICATION.md).

### Текущие runtime-файлы и владельцы записи

Перечень каталогов состояния, lock-файлов и владельцев записи (coordd, локальный
`cmd/coord`/localchat, `cmd/coord-telepathy`, Rooms history export, demo и тестовые
пути) приведён в [ARCHITECTURE.md](ARCHITECTURE.md), в описании текущего
устройства и каталогов состояния, и не дублируется здесь.

Эти процессы и их каталоги состояния не образуют общего writer boundary. P0-3
должен перевести production mutations на `coordd` command path; перечисление
legacy файлов не означает, что они уже подчинены namespace lock `coordd`.

Локальный reference host CLI/subprocess находится в examples/agent. Он проверен
с тестовым executable, включая process-group cancellation, и не заменяет
production supervisor для произвольного агента. Исправления и воспроизводимые
сценарии перечислены в VERIFICATION.md.
