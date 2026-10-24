# ADR-DRAFT: Durable orchestration в существующем coordd

- **Статус:** Proposed — не является принятой нормой или разрешением production implementation.
- **Эксплуатационная пометка (2026-10-24):** OWNER решил отключить durable orchestration до принятия этого ADR (#96). До посадки #96 fixture-only runtime всё ещё подключён в `coordd`. Эта пометка не меняет код и не принимает ADR. Аутентифицированный production write path остаётся за #93 и #95.
- **Дата:** 2026-10-15.
- **Номер ADR:** не назначен. ADR-0006 и ADR-0009 уже упоминаются в архитектуре как отдельные контракты; этот draft не занимает их номера.
- **Класс решения:** R; класс implementation slice: TD-R2.
- **Issue:** назначить существующий подходящий или отдельный tracker перед implementation; #47/#48 автоматически не переопределяются.
- **Область:** deterministic project orchestration через canonical state, durable external-action intents, observations и восстановимые projections.
- **Связанные нормы:** ADR-0001, ADR-0003, ADR-0004, ADR-0005; TASK-MODEL; принятые Owner/Agent command-boundary designs. Session/process stop-proof contract требует согласования с ADR-0009.

## Контекст

Coord уже имеет coordd, exclusive namespace ownership, CanonicalStore/CanonicalFile, Tasks, Runs, DomainEvents, Inbox receipts, Outbox envelopes, AgentSession projections/history, authenticated agent command validation и проверки независимости ролей. Coordinator.RunWithReadiness обрабатывает сообщения и Tick для текущего Run lifecycle. Supervisor содержит process lifecycle и heartbeat/fencing primitives.

Эти компоненты пока не образуют полный production workflow Issue → assignment → admission → execution → Review → Audit → CI → integration. CURRENT/OPEN-WORK и сообщения чат-координатора могут расходиться с фактами. Новый watchdog draft сам по себе не закрывает отсутствие production wiring.

Внешний launch перед durable записью опасен: crash после launch оставляет Coord без знания о side effect. Повтор может создать второго writer. Durable outbox устраняет потерю intent, но сам по себе не обеспечивает exactly-once внешнее исполнение.

## Решение

### DO-01. Единственный runtime owner

Существующий coordd остаётся единственным writer своего canonical namespace. Не вводятся второй daemon, независимый project State или очередь workflow вне canonical persistence.

Orchestration расширяет canonical Task/Run/AgentSession model. Assignment получает устойчивую identity и явные ссылки на TaskID, RunID, role, subject и generation. GitHub Issue — External Reference по ADR-0003, а не canonical identity или источник authority.

CLI является клиентом существующего daemon. Dispatcher, watchdog и projection builder могут работать конкурентно, но изменяют canonical state только через команды владельцу coordd. Supervisor продолжает владеть process launch/termination; dispatcher запрашивает эти операции через проверяемый adapter.

### DO-02. Транзакционная граница

Один canonical commit атомарно сохраняет:

- проверенный переход и изменения aggregate;
- связанные DomainEvents;
- необходимые Inbox dedup receipts;
- внешние action intents и/или transport Outbox messages;
- durable scheduling/blocker metadata.

Публикация нового состояния в памяти — после успешного durable Save. Неопределённый результат durability закрывает дальнейшие dispatch/mutations до reopen/recovery, согласно ADR-0005.

Внешний side effect не выполняется под storage lock и не предшествует intent commit. Протокол:

```text
command → authenticate/authorize/validate → state + event + action intent → commit
→ claim → durable dispatch-start marker → external adapter
→ authenticated observation/result → inbox dedup + validate
→ state + event + action completion + acknowledgement outbox → commit
→ projection scheduling
```

Вторая транзакция включает completion и acknowledgement intent вместе: состояние «принято, но receipt ещё надо создать» не допускается. Crash может оставить receipt ещё не доставленным; его replay идёт из Outbox.

### DO-03. Модель ExternalAction и совместимость

В canonical state вводится типизированная запись ExternalAction. Минимальные поля:

```go
type ExternalAction struct {
    ID, OperationInstanceID, IdempotencyKey string
    ProjectID, AssignmentID, RunID string
    TaskID taskid.ID
    SessionID string
    Type ExternalActionType
    SubjectSHA string
    SessionGeneration, TaskFence uint64
    RequestDigest string
    RequestRef string
    ResultDigest, ObservationRef string
    State ExternalActionState
    Claim *ActionClaim
    Attempts, MaxAttempts uint32
    NextAttemptAt time.Time
    RecoveryRequired bool
    CreatedAt, UpdatedAt time.Time
}

type ActionClaim struct {
    ActionID string
    ClaimedAt time.Time
    DispatcherID string
    OwnerEpoch uint64
    ClaimEpoch uint64
    LeaseDeadline time.Time
}
```

RequestRef указывает на доступный immutable validated request, сохранённый атомарно или гарантированно durable до intent commit. Один digest без request недостаточен для восстановления. Result/observation payload или безопасная durable ссылка сохраняются до acceptance. Secrets, credentials и lease tokens не сохраняются в обычных payload/projections; используются защищённые references.

Existing OutboxMessage сегодня содержит wire envelope и привязан к Run/Event. Произвольные external actions не подставляются в него с отключением существующих validators. Реализация выбирает typed extension/коллекцию actions внутри того же CanonicalState; transport Outbox продолжает выполнять свои гарантии. Точное schema layout и mapping events фиксируются до implementation.

Изменение требует явного schema-version/migration contract: старые snapshots без actions получают проверенную пустую структуру; неизвестные версии fail-closed. Старые binaries не должны молча удалить новые поля при read-modify-write. Добавляется глобальная canonical commit revision; она не заменяет Task.Revision и EventSeq.

### DO-04. Lifecycle и окно неопределённости

Состояния:

```text
PENDING → CLAIMED → DISPATCHING → DISPATCHED → OBSERVED → COMPLETED
                         ↘ AMBIGUOUS → observation/reconcile
                         ↘ recovery/observation
terminal outcomes: FAILED / FENCED
suspended outcome: BLOCKED (resumption requires a validated trigger)
```

- PENDING: intent durable; dispatch-start ещё не разрешён.
- CLAIMED: dispatcher имеет актуальный claim; это не admission или execution.
- DISPATCHING: durable разрешение начать конкретный внешний вызов. После этого side effect мог произойти независимо от наличия ответа.
- DISPATCHED: запрос отправлен/adapter вернул delivery evidence. Это не результат.
- AMBIGUOUS: side effect мог произойти; разрешено только observation/reconcile до следующего проверенного решения. После restart DISPATCHING обязательно классифицируется AMBIGUOUS.
- OBSERVED: durable observation записана, но ещё не обязательно принята workflow.
- COMPLETED: результат принят canonical command boundary.
- BLOCKED: автоматическое продвижение запрещено до определённого trigger/reconcile; blocker durable.
- FENCED: конкретная операция потеряла authority; новое внешнее действие не разрешено. Это не утверждение о физической остановке процесса.

Состояния не обязаны проходиться отдельными commits там, где их можно атомарно объединить. Важны durable границы до внешнего вызова и перед acceptance. Ошибка/timeout после DISPATCHING всегда считается potentially ambiguous, пока adapter не предъявил достаточное observation.

### DO-05. Idempotency и отдельные экземпляры операций

Ключ строится из versioned canonical encoding, без неоднозначной конкатенации:

```text
key = SHA256(protocolVersion, operationType, projectID, taskID,
             assignmentID, subjectSHA, sessionGeneration, taskFence,
             operationInstanceID, sessionID, requestDigest)
```

OperationInstanceID фиксируется вместе с intent. Retry одной logical action использует тот же key и RequestDigest. RequestDigest вычисляется по immutable semantic request; claim, transport timestamps и счётчики retries в digest не входят. Новый subject/generation/fence создаёт другую operation identity. Повторное observation в будущем — новый operation instance: нельзя бесконечно получать старое observation по тому же key.

Receiver/wrapper гарантирует durable key+body binding:

- тот же key + тот же digest → исходный результат, без повторного side effect;
- тот же key + другой digest → conflict;
- истёкшая/неизвестная dedup retention → observation/reconcile либо BLOCKED, не слепой retry.

Локальная таблица dispatcher не закрывает crash window внутри внешнего launch. Exactly-once side effect заявляется только для adapter, чей внешний boundary подтверждает идемпотентность. Для остальных гарантируется отсутствие автоматического blind retry; ambiguity может завершиться BLOCKED_EXTERNAL.

### DO-06. Claim lease и stale dispatchers

Claim создаётся canonical command с проверкой action state, expected revision, текущего OwnerEpoch/ClaimEpoch, TaskFence и session binding. Lease не даёт authority сверх assignment.

Перед переходом CLAIMED → DISPATCHING повторно проверяются claim и все fences. Устаревший claim не может записать результат или завершить action. Ответ запоздавшего dispatcher сохраняется как observation при допустимой authenticated ingestion, но не переносит authority автоматически.

Истечение lease не доказывает отсутствие side effect. CLAIMED можно безопасно reclaim только при доказанном отсутствии dispatch-start и защите от старого worker. DISPATCHING/DISPATCHED требуют observation/reconcile до redispatch. Receiver обязан проверять fencing/идемпотентность на границе внешнего эффекта; один локальный CAS не предотвращает поздний вызов ранее приостановленного worker.

### DO-07. Observations, Inbox и Watchdog

Watchdog сообщает typed observations и не выполняет самостоятельный workflow replacement. Единая цепочка:

```text
HEARTBEAT_STALE → canonical SUSPECT/authority suspended + OBSERVE_SESSION intent
→ observation of unavailable session → FENCE_SESSION intent
→ confirmed fencing + required supervisor stop proof
→ replacement assignment intent
```

Логический fence запрещает canonical writes. Если старый процесс способен менять worktree, replacement writer не запускается до stop proof/изоляции, требуемых ADR-0004. В первом slice это проверяется fake Supervisor; production guarantee без реального adapter не объявляется.

ObservationRecord содержит ID, source identity, subject/action/session binding, generation, fence, source revision/sequence, observedAt, payload digest и durable safe payload/ref. Свободный текст агента — REPORTED, не trusted observation. Stale/out-of-order/replayed observations не восстанавливают authority и не создают повторные transitions.

Inbox key закреплён за authenticated actor + message ID. Одинаковое identity с другим body — conflict. Dedup lookup не должен обходить текущую authentication/fencing boundary. Replay уже принятого command возвращает сохранённый receipt; другой body или недопустимая identity не принимаются.

### DO-08. Recovery, heartbeat и retry

При restart coordd получает exclusive ownership, валидирует schema/invariants, поднимает незавершённые action obligations и классифицирует claims. Persisted RUNNING не доказывает liveness, но не означает death: session получает NEEDS_OBSERVATION как recovery obligation, затем выполняется свежая проверка.

Монотонные deadlines относятся к текущему process lifetime. Сериализованные timestamps используются как wall-clock evidence; старые lease/deadline calculations не принимаются за доказательство свежести. Будущий timestamp, clock rollback и слишком старое observation требуют проверки; receiver clock и registry revisions/sequence учитываются явно.

Retries ограничены, с fake-clock-testable backoff/jitter и durable NextAttemptAt. Mutation authority при ambiguity не расширяется. Исчерпание retries сохраняет blocker с error class, attempts, evidence и next permitted trigger. Ошибка Registry/API классифицируется BLOCKED_EXTERNAL, если не доказана иная граница. OWNER/HUMAN flags не выводятся из факта timeout.

Daemon ждёт ближайшее due action, event, lease/recovery deadline или timer. BLOCKED action без trigger не перепланируется немедленно; невозможен busy loop. Readiness recovery endpoints и normal mutations разделены; unavailable external API не обязательно останавливает независимые безопасные задачи, но не открывает authority unresolved assignments.

### DO-09. Проекции

Canonical state остаётся единственной authority. CURRENT/OPEN-WORK/Ledger строятся из immutable snapshot с одной sourceRevision; human-readable state нельзя использовать для обратной записи workflow в обход commands.

Projection bundle размещается в revisions/<sourceRevision>/ и содержит manifest с digest каждого файла и generatorVersion. После проверки и fsync файлов/каталога атомарно публикуется current manifest/pointer. Path containment и symlink safety обязательны.

ProjectionCompleted фиксирует целевую sourceRevision. Служебный acknowledgement увеличивает commit revision, но не создаёт бесконечную цепочку projection requests: dirty marker меняется только при изменении проектируемого содержания. Более старый job не может откатить current pointer после публикации нового revision. Crash между публикацией bundle и acknowledgement восстанавливается по manifest без повторного workflow action.

Missing/stale bundle регенерируется; он не блокирует canonical durability, но может закрывать те read/export endpoints, которые обещают согласованную проекцию.

### DO-10. LLM и границы решения

Первый slice детерминированный, без model invocation. Будущий CandidateDecision — предложение, не команда с authority. Его selected transition, requested actions и evidence проверяются runtime; неизвестные поля/действия не дают разрешения.

OWNER_REQUIRED требует accepted norm references с section/digest и структурированного основания, проверяемого в пределах поддерживаемого validator contract. Произвольное семантическое противоречие норм не считается автоматически доказуемым программой: unsupported case остаётся UNKNOWN/blocked с evidence. Один reason code не проходит gate. Requested inspection требует trusted ObservationRecord.

Review/Audit используют существующие stable-AgentID history checks и exact subject binding. Transport delivery, claim, model output и self-declared ADMITTED не заменяют authenticated admission. Этот ADR не вводит альтернативные verdict enums или альтернативную role identity.

## Первый implementation slice

### Входит

- typed external-action model и command transitions внутри canonical persistence;
- versioned schema/global revision contract;
- durable dispatch-start, claims, idempotency/body binding, observation/dedup;
- wiring dispatcher/recovery в существующий coordd loop;
- deterministic fake Supervisor/Registry adapters;
- один сквозной flow delivery-without-admission → restart → observation → fence/stop proof → replacement;
- rebuildable projection bundle одной sourceRevision;
- bounded scheduling/blockers и runtime completion gate для этого flow.

### Не входит

- второй daemon или второй canonical store;
- полный scheduler всех Issues/Review/Audit/CI стадий;
- production canonical Registry API и remote process termination без принятого контракта;
- live GitHub writes, merge/deploy или произвольный model/process launch;
- LLM CandidateDecision execution; в этом slice только резервируется boundary;
- перенос всех существующих packages в новую иерархию;
- автоматическое принятие всех prerequisites P0-3 или изменение R146/ADR-0009.

При изменении subject/fence/generation старый intent не перепривязывается: его authority закрывается; потенциально уже запущенный effect отдельно reconciles, затем создаётся новый intent. External callbacks требуют adapter identity/authentication и не принимаются как Agent command от произвольной session.

Issue → Review/Audit → CI должно быть representable без обхода canonical authority, но production integration всех этих providers не является exit первого slice.

### Сквозной acceptance scenario

1. Canonical command создаёт assignment + event + delivery intent одним commit.
2. Delivery подтверждается; admission отсутствует. Daemon durable сохраняет OBSERVE_ASSIGNMENT obligation.
3. Crash/restart не создаёт новый assignment и не повторяет неоднозначный launch.
4. После recovery выполняется observation существующего assignment/session.
5. Подтверждённая unavailable session приводит к fence intent; replacement до fence/stop proof запрещён.
6. Fake external boundary подтверждает fencing и stop proof.
7. Replacement имеет новую generation/operation identity; повторная доставка запроса с тем же key не создаёт второго процесса.
8. Canonical acceptance и receipt intent фиксируются атомарно.
9. CURRENT/OPEN-WORK/Ledger публикуются как один revision bundle; restart восстанавливает отсутствующий bundle.

## Crash matrix

| Crash point | Recovery obligation |
|---|---|
| До commit intent | Внешнего вызова нет; ничего не запускать |
| После PENDING commit | Claim и первый dispatch разрешены после проверки authority |
| После CLAIMED, без dispatch-start | Reclaim только при проверенном старом claim/worker fencing |
| После DISPATCHING marker, даже до actual send | Возможный side effect; observe или receiver-idempotent recovery |
| После launch, до ответа | Observe/reconcile; blind launch запрещён |
| После ответа, до OBSERVED commit | Найти результат по key/identity; если недоступен — BLOCKED |
| После OBSERVED, до acceptance | Повторить validated canonical acceptance с Inbox dedup |
| После acceptance, до receipt delivery | Receipt уже в Outbox; replay без side effect |
| После completion | Не повторять action; projections при необходимости rebuild |

## Проверки

Обязательные сценарные тесты первого slice, с fake clock/transport и restart persistence:

- CrashBeforeIntentDoesNothing
- CrashAfterIntentDispatchesOnce
- CrashAfterDispatchBeforeResponseObservesBeforeRetry
- RetryUsesSameIdempotencyKey
- SameKeyDifferentBodyRejected
- NewObservationInstanceDoesNotReuseStaleResult
- ExpiredClaimReconciledBeforeRedispatch
- StaleDispatcherCannotExecuteOrCompleteClaim
- ConfirmedFenceAndStopProofPrecedeReplacement
- RestartDoesNotDeclareHealthySessionDead
- RestartDoesNotTrustPersistedRunningState
- DuplicateObservationDoesNotRepeatTransition
- SubjectGenerationFenceMismatchRejected
- CompletionAndReceiptIntentCommitTogether
- ProjectionBundleUsesOneCanonicalRevision
- StaleProjectionRebuiltFromCanonicalState
- OlderProjectionCannotReplaceNewerBundle
- ProjectionAcknowledgementDoesNotScheduleItself
- RetryExhaustionCreatesDurableBlocker
- NoBusyLoopAfterBlockedAction
- ExistingCoorddRemainsSoleWriter
- OldSnapshotMigratesAndUnknownSchemaFailsClosed

CandidateOwnerReasonRequiresAcceptedNormEvidence и RequestedInspectionRequiresObservationRecord проверяются при добавлении CandidateDecision; они не объявляются выполненными без соответствующей реализации. Trusted observation requirement для deterministic flow проверяется уже в первом slice.

Компилируемые mutations должны ломать production path, а не отдельную копию алгоритма: side effect до intent commit; новый key при retry; blind redispatch после expired claim; replacement до fence/stop proof; доверие persisted RUNNING; разные projection revisions; принятие stale claim/неподтверждённого observation. Для каждой нужны passing baseline, compiling mutant и failing behavioral assertion.

Full go test -race, make check, git diff --check, независимые Review/Audit и CI exact SHA обязательны. Проверки не используют системный temp. Acceptance не выводится из сообщения модели или compile error mutant.

## Альтернативы

- **Новый Coordinator daemon:** отклонён; создаёт второго state owner и дублирует coordd.
- **Execute → Save:** отклонён; оставляет неучтённые side effects после crash.
- **Durable queue без receiver contract:** недостаточна; обеспечивает retry intent, не exactly-once effect.
- **CURRENT/OPEN-WORK как primary state:** отклонено; несколько authority и рассогласованные snapshots.
- **LLM выбирает и исполняет всё:** отклонено; свободный текст не проверяет fencing/durability.
- **Полная реализация всех integrations первым commit:** отклонено; мешает доказать один bounded crash-safe flow.

## Последствия и rollout

Переиспользуются существующие persistence, locks, command validators, role checks и Supervisor contracts. Добавляется сложность action recovery, schema migration и adapter qualification; это необходимая цена устранения crash window.

Решение не утверждает universal exactly-once или production readiness Registry. Система сохраняет ambiguity и blocker вместо потенциально опасного повтора.

Rollout: accepted design/schema contract → crash/mutation tests → canonical action commands → coordd dispatch/recovery wiring → projection bundle → независимые gates. До live adapter qualification доступны только deterministic adapters и явно ограниченный профиль.

При rollback сначала отключается создание новых intents и quiesce dispatcher; сохраняются unresolved actions и observations. Нельзя запускать старый binary, который не понимает schema, или удалять pending intents. Возврат к Execute → Save запрещён. Полная production activation требует применимых stage-boundary gates и принятых process/identity contracts.

## Дополнение v2: точный scope review candidate, issue #49

Пользователь явно разрешил «подготовка и первый срез». Это разрешает подготовку isolated implementation candidate; не превращает ADR в Accepted и не разрешает live activation. Номер не назначается. Issue #49 создан Coordinator; #47/#48 не меняются. Автор c3f не может быть Reviewer/Auditor.

Для review обязательны требования DO-01..DO-10 и отдельные проверки: AMBIGUOUS после restart DISPATCHING; OperationInstanceID/session/request digest в key; claim epoch для late results; separate FenceRecord/StopProof; подтверждённый logical fence + sufficient stop proof + rejection старой generation перед replacement.

Методы StopProof определяет принятый process contract. Первый candidate принимает только trusted fake Supervisor result с SUPERVISOR_CONFIRMED_EXIT; прочие методы и live proof verification NOT_IMPLEMENTED. Self-report агента не считается proof.

Implementation candidate использует typed Orchestration extension внутри CanonicalState, существующие Task DomainEvents/Inbox receipts и typed acknowledgement intents. Transport Outbox envelopes не переиспользуются с отключением validators. Schema v1 нормализуется в v2; старый strict loader отвергает новые поля. Global Revision отделена от Task.Revision. Pending requests сохраняются durable, а не одним digest.

Первый candidate доставляет assignment, получает observation, отзывает старую canonical AgentSession, проверяет fake stop proof и создаёт replacement intent с next-generation request. Он НЕ создаёт production Run/новую authenticated AgentSession: production binding нового writer NOT_IMPLEMENTED. Нет полного Issue/Review/Audit/CI scheduler, provider launch или LLM. Эти границы должны быть указаны в verdict; нельзя назвать весь целевой runtime IMPLEMENTED.

Вместо canonical ProjectionCompleted candidate использует проверяемый filesystem manifest и идемпотентный rebuild на Tick; это осознанный вариант первого среза, без authoritative projection state и feedback loop. Публикация pointer производится после файлов bundle; ack receipt создаётся в acceptance commit. Retry policy первого среза — bounded deterministic exponential backoff; configurable jitter не входит.

Required mutation matrix:

| ID | Нарушаемое свойство |
|---|---|
| M01 | External call раньше durable DISPATCHING |
| M02 | Retry меняет idempotency key |
| M03 | Новый observation переиспользует operation identity/key |
| M04 | Expired claim приводит к blind Execute вместо Observe |
| M05 | Late result старого ClaimEpoch принимается |
| M06 | Delivery fence принимается как подтверждённый fence |
| M07 | Replacement intent создаётся без StopProof |
| M08 | Persisted RUNNING принимается после restart |
| M09 | Projection bundle смешивает revisions |
| M10 | Unsupported free-text OWNER result принимается как workflow result |
| M11 | Retry exhaustion не сохраняет durable blocker |
| M12 | BLOCKED action вызывает бесконечные canonical commits |

M10 первого среза проверяет rejection unsupported outcome. Полная CandidateReason/Accepted NormRef validation отложена до CandidateDecision layer и не объявляется реализованной. Мутации с defense in depth могут отключать несколько относящихся к одному invariant guards; каждый mutant обязан компилироваться и падать по целевой behavioral assertion.

Review scope: compatibility с ADR-0004/0005; single writer; atomicity; action identity; claim/ambiguity; proof boundary; projection revision; retry scheduling; schema migration/rollback. TD-R2 Audit после Review: crash/restart, stale workers/results, replacement ordering, old mutation authority и качество mutation kills.

Decision Trace для OWNER: выбрать принятие/коррекцию данного Proposed решения, отдельно от разрешения подготовить candidate. Открытые решения: schema/migration acceptance; контракт production adapter/stop proof; место production scheduling в P0-3. До acceptance никакой commit/merge/live deployment не выполняется.
