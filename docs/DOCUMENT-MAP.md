# Карта документов Coord

Этот навигатор подсказывает, что читать и обновлять для конкретной роли или
события. Полный перечень документов, владельцы и условия обновления находятся
в [`DOCUMENT-REGISTRY.yaml`](DOCUMENT-REGISTRY.yaml); последовательность действий
для событий — в [`operations/ACTION-MAP.md`](operations/ACTION-MAP.md).

## Если вы…

| Ситуация | Путь |
|---|---|
| начинаете новую сессию | Startup path ниже; затем активный assignment |
| планируете Issue | Planning path |
| выполняете изменение | Implementation path |
| проверяете изменение | Review path или Audit path |
| готовите commit | Commit Readiness path |
| запускаете или диагностируете runtime | Operations path |
| расследуете прошлый запуск или инцидент | Historical evidence path |

### Startup path

Coordinator получает наблюдаемое process event или подтверждённое изменение
состояния из активной записи, инструмента или события State Machine; затем
вручную классифицирует его по Action Map.

Coordinator восстанавливает минимальный контекст из `.coord/project.yaml`,
`AGENTS.md`, `.coord/CONTEXT-INDEX.yaml`,
`operations/project/CURRENT.yaml`, `operations/project/OPEN-WORK.yaml` и
assignment активной задачи. Проверяются ProjectID, HEAD, активная задача,
blockers, следующий безопасный переход, OWNER boundary и доступность evidence.
Только затем загружаются owning нормы текущей работы. Сейчас Coordinator вручную
определяет применимые документы по `readWhen`, `status`, `authority` и `startupPaths` в
Registry/Action Map; программный
resolver и launcher admission не подключены. Для Executor, Reviewer и Auditor
наборы указаны в `startupPaths` реестра.

Admission Card и Action Card — рекомендуемый process control, но технически не
enforced. При отсутствующем обязательном документе или неизвестном событии
Coordinator не угадывает переход и фиксирует `UNKNOWN_EVENT`/fail-closed
результат.

### Planning path

Для новой Issue Coordinator читает `CURRENT.yaml`, `OPEN-WORK.yaml`,
`ROADMAP.md`, затронутые записи `COMPONENT-STATUS.yaml`, owning Accepted ADR и
`TASK-MODEL.md`, если меняется Task semantics. Assignment фиксирует компонент,
base SHA, worktree, allowed paths, проверки, Review/Audit, architecture impact
и stop conditions.

### Implementation path

Executor получает assignment, WorkspaceBinding, точный base SHA, owning norms,
разрешённые файлы, проверки и условия остановки. Он читает только относящиеся к
задаче process/design/operations материалы. При изменении компонента
архитектурные projections обновляются одной транзакцией по
[`ARCHITECTURE-SYNC-PROCESS.md`](operations/ARCHITECTURE-SYNC-PROCESS.md).

### Review path

Reviewer сверяет assignment и owning norms с замороженным subject, tree/patch
hash, тестами и negative controls. При изменении зрелости или архитектурных
проекций проверяются `COMPONENT-STATUS.yaml`, `ARCHITECTURE.md` и
`TARGET-ARCHITECTURE.md`. Автор не принимает собственную работу.

### Audit path

Auditor получает frozen subject, относящиеся к identity/security/recovery нормы,
отрицательные тесты, мутации, Review verdict и явный список `NOT_EVALUATED`.
Исторический отчёт не заменяет проверку текущего subject.

### Commit Readiness path

Coordinator проверяет обязательные структурированные Review/Audit verdicts,
Architecture Sync, точную принятую версию, полный staged scope,
`git diff --check`, `git diff --cached --check` и полный `make check` через
project-local wrapper; точная команда формируется по актуальному `--help`
wrapper'а. Прямой запуск не удовлетворяет Commit Readiness, если создаёт
temp/cache вне WorkspaceBinding. После интеграции обновляются component maturity,
`CURRENT.yaml`, `OPEN-WORK.yaml`, Operations Ledger, `CHANGELOG.md`, Completion
Report и Issue.

### Operations path

Применяйте операционный документ только если его шапка указывает подходящий
component/profile и `currentOperationalAuthority: true`. Legacy guides
описывают только названный legacy runtime; proposed и historical материалы не
являются инструкциями запуска.

### Historical evidence path

Читайте конкретный manifest/report только для расследования. Сверяйте его
subject revision и ограничения переносимости с текущими authority sources.
Исторические evidence не загружаются автоматически, не доказывают текущий HEAD
и дополняются resolution reference без переписывания прежних фактов.

## Классы документов

| Класс | На какой вопрос отвечает | Примеры |
|---|---|---|
| `normative` | Как система обязана работать? | Accepted ADR, `TASK-MODEL.md`, записанные решения Rxx |
| `architecture_status` | Что реализовано и насколько подключено? | `COMPONENT-STATUS.yaml`, `ARCHITECTURE.md`, `TARGET-ARCHITECTURE.md` |
| `process` | Кто выполняет работу и какой следующий переход? | `AGENTS.md`, State Machine, role prompts, verification, Architecture Sync |
| `operations` | Как запускать конкретный существующий профиль? | `OPERATIONS.md`, `QUICKSTART.md`, legacy guides |
| `active_context` | Где проект сейчас и что делать дальше? | `.coord/*`, CURRENT/OPEN-WORK, assignments, Operations Ledger |
| `historical` | Что было проверено на конкретной версии? | `CURRENT_VERSION.md`, `UX_AUDIT.md`, старые reports/incidents/Completion Reports |
| `design` | Как реализовать решение в пределах действующих норм? | design documents, plans |

RFC-0002, `AGENT-BOOTSTRAP-TEMPLATE.md` и весь каталог `docs/ai-agents/`
зарегистрированы как `proposed`. Они описывают будущий процесс, политики и
шаблон, но не являются действующей нормой, обязательным launcher contract или
основанием запускать агента. Читайте их при планировании оценки или
рассмотрении RFC; действующие правила по-прежнему задаются `AGENTS.md`,
State Machine и принятыми процессными нормами.

- [RFC-0002: Управляемая разработка с генеративными AI-агентами](rfc/RFC-0002-AI-AGENT-ENGINEERING-GOVERNANCE.md)
- [Proposed Agent Bootstrap Template](templates/AGENT-BOOTSTRAP-TEMPLATE.md)
- [AI Agent Governance: Implementation Status](ai-agents/IMPLEMENTATION-STATUS.yaml) — и другие политики в `docs/ai-agents/` (adapter-registry, evaluation-profiles, context-budgets, retention, hidden-case, baseline-acceptance, cost-model, model-adapter-standard; все `proposed`)
- [ActionContext Schema](schemas/action-context.schema.yaml), [EvidenceRecord Schema](schemas/evidence-record.schema.yaml) — JSON Schema (proposed)
- [AI Agent Governance Work Plan](plans/ai-agent-governance/GITLAB-WORK-PLAN.md) — initiative, milestones, AIAG-01..AIAG-18, dependency graph, board columns (`proposed`); issues в `docs/plans/ai-agent-governance/issues/`
- [Priority and Estimation](plans/ai-agent-governance/PRIORITY-AND-ESTIMATION.md) — priority rules and relative complexity estimates for AIAG issues (`proposed`)
- [ActionContext Resolver + Launcher](operations/ACTION-MAP.yaml) — машиночитаемая YAML-проекция ACTION-MAP.md (`proposed`); используется `scripts/resolve-action-context` и `scripts/launch-agent`
- [OS Sandbox Profiles](../config/SANDBOX-PROFILES.yaml) — профили Bubblewrap sandbox (evaluation-linux, production-linux) для `scripts/sandbox_exec.py` (`proposed`)
- [AgentSession Registry Contract](../config/SESSION-REGISTRY-CONTRACT.yaml) — контракт адаптера registry: операции, поля ответа, политика launcher (`proposed`)
- [Security Boundary](SECURITY-BOUNDARY.md) — граница OS sandbox и AgentSession Registry; Linux backend, статус macOS/Windows, ограничения (`proposed`)
- [Supervisor Policy](../config/SUPERVISOR.yaml) — heartbeat intervals, fence conditions, process group termination для `scripts/session_supervisor.py` (`proposed`)
- [Session Registry HTTP](../config/SESSION-REGISTRY-HTTP.yaml) — superseded by SESSION-REGISTRY-OPENAPI-MAPPING.yaml; retained as reference (`proposed`)
- [Session Registry OpenAPI Mapping](../config/SESSION-REGISTRY-OPENAPI-MAPPING.yaml) — canonical mapping contract: transport, operations, request/response/status mapping, idempotency; verification NOT_EVALUATED (`proposed`)
- [Registry Retry Policy](../config/REGISTRY-RETRY.yaml) — retry/idempotency: max 4 attempts, Idempotency-Key inputs, heartbeat sequence contract (`proposed`)
- [Seccomp Profiles](../config/SECCOMP-PROFILES.yaml) — Linux allowlist profiles для `scripts/seccomp_exec.py`; python-agent-v1 (`proposed`)
- [Production Registry Adapter](PRODUCTION-REGISTRY-ADAPTER.md) — критерии принятия HTTP adapter; NOT_EVALUATED OpenAPI mapping (`proposed`)
- [Seccomp](SECCOMP.md) — дизайн Linux seccomp profiles; сочетание с namespaces и supervision (`proposed`)

## Приоритет источников

Приоритет зависит от вопроса; универсальная линейная иерархия смешала бы
разные виды authority.

- **Нормы продукта:** Accepted ADR и `TASK-MODEL.md`, затем записанные решения
  Rxx в owning document. Proposed ADR и design не создают текущую норму.
- **Процесс:** `AGENTS.md` и Coordinator State Machine; assignment задаёт
  ограниченный scope конкретной работы.
- **Статус реализации:** `COMPONENT-STATUS.yaml` — машиночитаемый источник;
  `ARCHITECTURE.md` объясняет текущий код, `TARGET-ARCHITECTURE.md` — разрыв
  до нормы.
- **Текущее состояние:** `.coord` context, CURRENT/OPEN-WORK и Operations Ledger;
  они не меняют продуктовые нормы.
- **Порядок работ:** `ROADMAP.md` в пределах Accepted norms и текущего state.
- **Операции:** guide, применимый только к конкретному профилю и статусу.
- **История:** доказательство только для указанного subject и ограничений
  конкретного исторического отчёта.

## Обновление материалов

Для каждого документа реестр задаёт `readWhen`, `updateWhen`, ответственные
роли и приёмку. После каждого перехода State Machine обновляются только
изменённые записи рабочего состояния; архитектурные projections обновляются
при изменении компонента/гарантии/integration level; исторические документы не
переписываются.
