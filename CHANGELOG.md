# История изменений

## 2026-10-10 — Remove unreachable RoleSelection nil guard ([#35](https://github.com/naghuale/coord/issues/35))

- (код) Удалён недостижимый nil `RoleSelection` guard из `validateAgentCommandRoleStage`;
  boundary сохраняет отказ `ErrAgentCommandWrongSession` до входа в helper
  (`TestAgentCommandRejectsMissingRoleSelectionBeforeRoleStage`).
- (документация) TD решение R142 для #35 записано в
  `docs/plans/p0-2-task/AGENT-COMMAND-BOUNDARY.md`; production path не подключён.

## 2026-10-10 — Discussion Graph core ([#30](https://github.com/naghuale/coord/issues/30))

- (код) Добавлены типизированные Discussion nodes/edges и DomainEvents; Graph
  восстанавливается только из Events. `RecordDiscussionNode` и
  `RecordDiscussionEdge` идут через `ApplyAgentCommand`, используют Coord-generated IDs,
  подтверждённого автора из Run и атомарный Event/Inbox/Outbox commit
  (`TestDiscussionAgentCommandCommitsTypedNodeAndRebuildsProjection`,
  `TestDiscussionAgentCommandUsesVerifiedActorAndIsReplaySafe`).
- (код) Добавлены fail-closed проверки обязательных полей, строгого JSON schema,
  ссылок той же Task, матрицы пар рёбер и запрета Discussion менять workflow Task.
  OwnerDecisionRef и ConflictID отклоняются до наличия соответствующих canonical объектов;
  ResultRef отклоняется до реализации #6, даже если `fsm.Run.Result` заполнен
  (`TestDiscussionDecisionAndResultReferencesFailClosed`,
  `TestDiscussionResultReferenceFailsClosedUntilExecutorResultSlice`,
  `TestDiscussionEdgeTypePairIsEnforced`, `TestDiscussionEventCannotChangeTaskWorkflow`).
- (документация) `docs/plans/p0-review-audit/DESIGN.md` дополнен решениями R140/R141,
  выбором TD по R142 и фактическим срезом #30; в ADR-0001 CM-11a записаны R140 и R141.
  Production agent command path не подключён до P0-3.
- (тесты) Добавлены сценарии командного commit/replay, проекции, авторства, типов узлов,
  ровно одной ссылки FINDING, строгих полей, fail-closed ссылок и полной матрицы edge pairs
  (`TestDiscussionFindingRequiresExactlyOneResultReference`, `TestDiscussionRejectsUnknownFields`,
  `TestDiscussionDoesNotPromoteTransportEvents`, `TestDiscussionRejectsCrossTaskEdgeEndpoint`).

## 2026-10-09 — Синхронизация §2 и имён тестов AGENT-COMMAND-BOUNDARY с частью 4 ([#25](https://github.com/naghuale/coord/issues/25))

- (документация) `docs/plans/p0-2-task/AGENT-COMMAND-BOUNDARY.md`, §2: строка «поколение
  сессии актуально» приведена к коду — поколение входит в `SameDomainState` и проверяется
  при хранении и recovery transfer, а в границе команд generation кадра сверяется с
  `AgentSession.Generation` и `Run.AgentSessionGeneration` до replay.
- (документация) Там же итог под таблицей: добавлен третий канонический вход
  `CanonicalStore.ApplyAgentCommand` с перечнем его проверок, а утверждения «Чего нет
  нигде: сверки входного `Task.Fence`/`Run.Fence`, `Attempt` и аутентифицированной сессии»
  и «сверка эпохи, попытки и сессии отсутствует в обоих» заменены на факт: сверка
  существует, но production path не подключён, реальный вход остаётся legacy
  `Coordinator.Handle`.
- (документация) Матрица тестов: десять плановых имён, которых нет в коде, заменены на
  реальные проверки, помечены «не реализовано» либо «не доказано отдельным тестом».
  Совпали по смыслу:
  `TestAgentCommandRejectsStaleFence`, `TestAgentCommandRejectsFutureFence`,
  `TestAgentCommandReplayReturnsPriorReceiptWithoutWriting` (сверяет `Attempt` до и после
  replay), `TestAgentCommandRejectsInvalidFrameBeforePersistence` (отказ при `saveCalls == 0`
  и неизменном состоянии), `TestAgentCommandReplayAfterRevisionGrowthReturnsPriorReceipt`
  вместе с `TestAgentCommandRejectsStaleRevision`,
  `TestCanonicalTransitionRecoveryPreservesAttemptAndIncrementsTaskFence` вместе с
  `TestCanonicalStartupRejectsRunFenceDifferentFromLatestTransfer`,
  `TestCanonicalTransitionRequiresRevisionEventAndMonotonicFence`.
- (документация) Разведены случаи «нет кода» и «нет отдельного теста». Для двух строк
  про эпоху — «эпоха при ранее виденном `messageID`» и «порядок: сверка эпохи до replay» —
  код есть: сверки `run.Fence != currentTask.Fence` и входного `Fence`
  (`store/canonical_agent_command.go:131-139`) стоят до replay lookup (`:157`), поэтому
  они помечены «не доказано отдельным тестом», а не «не реализовано». Для строки про
  `Run.Fence` авторитетный указан код: расхождение даёт `NOT_CURRENT_WRITER` (`:131`).
  Строка про recovery при двух активных writer осталась «не реализовано»: ROADMAP-цель
  относится к recovery tick, а не к границе команд.
- (документация) В §2 уточнены строки «Attempt», «эпоха», «писатель» и «роль» по
  `ApplyAgentCommand`: приведены проверки, ссылки на код и сценарные тесты; отдельно
  указано, что production path не подключён до P0-3, а `ApplyInboxCommand` не принимает
  Attempt, Fence или сессию.

## 2026-10-09 — Нормативная запись решений OWNER R138 и R139: типизированный Discussion Graph ([#29](https://github.com/naghuale/coord/issues/29))

- (документация) `docs/ADR-0001`: после CM-11 добавлено правило «CM-11a — Типизированная
  схема Discussion Graph (R138, R139)» с общим минимумом полей узла и связи, полями по типам из
  решения OWNER, ссылкой на полную матрицу допустимых пар, ссылочной целостностью, append-only
  поведением и одной строкой «Проверка: не реализовано». Вариант A отклонён, вариант C
  отложен до R135 и design проверки содержимого. CM-10a и CM-11 не менялись.
- (документация) `docs/plans/p0-review-audit/DESIGN.md`: добавлен подраздел §5.1 «Схема
  Discussion (R138, R139)» для отдельной проверки — восемь типов узлов с полями и источником на
  каждое поле, шесть типов связей с матрицей допустимых пар, ссылочная целостность,
  отказ по неизвестным типам и лишним полям, схема Conflict Discussion Fact по R137 и
  пять строк отрицательных тестов и мутаций в матрице §9.
- (документация) DESIGN.md: строка §7 «Payload fields per Discussion node/edge type»
  получила статус «Решено R138 (вариант B) и R139 (поля оставшихся типов, матрица пар)»,
  фраза §5 про открытый вопрос заменена ссылкой на R138 и R139 и §5.1, абзацы §7 и §11
  теперь констатируют, что открытых вопросов не осталось (порядок conflict/escalation —
  R137, payload-поля — R138 и R139).
- (документация) R139 закрыл оба оставшихся кандидата: у типов `QUESTION`, `PROPOSAL`,
  `FACT`, `ACTION` только общий минимум R138, новых полей нет; `Refs[]` не пуст для `FACT`
  и `FINDING` (раздел Startup Card ADR-0001); Conflict Fact — это `FACT` с `ConflictID`,
  дополнительно требующий `ReviewResultRef` и `AuditResultRef` (R137).
- (документация) R139 задаёт полную матрицу допустимых пар (From type → To type):
  `ANSWERS` — `FACT`, `PROPOSAL`, `DECISION` → `QUESTION`; `SUPPORTS` — `FACT`, `FINDING`,
  `RESULT` → `PROPOSAL`, `DECISION`, `FINDING`; `CAUSED_BY` — `DECISION` → `FINDING`,
  `ACTION` → `DECISION`, `BLOCKER` → `FACT`, `FINDING`; `RESULT_OF` — `RESULT` →
  `ACTION`; `BLOCKS` — `BLOCKER` → `ACTION`, `DECISION`, `PROPOSAL`; `REFERS_TO` — любой →
  любой. Любая другая пара отклоняется fail-closed; мягкий вариант с ограничением только
  `ANSWERS` и `RESULT_OF` отклонён. Блок «Кандидаты» в §5.1 удалён: R138 и R139 полностью
  задают схему, открытых вопросов по ней не осталось.
- (документация) Цепочка ADR-0001 `FINDING → DECISION → ACTION → RESULT` сверена: она
  описывает связи графа и не образует FSM Discussion, поэтому из неё не выведено ни одной
  допустимой пары сверх заданной R139. Мутанты в матрице §9 дополнены: разрешить пару вне
  матрицы или ограничить `REFERS_TO`, принять `FACT` или `FINDING` с пустым `Refs[]`.
  Описание проверки пар дополнено табличной проверкой по всем 8×8 парам.

## 2026-10-09 — Запрет Executor после Reviewer или Auditor в той же Task ([#27](https://github.com/naghuale/coord/issues/27))

- (код) Каноническая проверка role independence отклоняет назначение Executor, если устойчивый
  `AgentID` встречается в более ранней Reviewer или Auditor `RoleSelectionHistory` этой Task;
  ошибка — `EXECUTOR_ROLE_HISTORY_CONFLICT`. Если после такого назначения у Executor нет
  проверяемого `AgentID`, назначение также отклоняется; до первого Reviewer/Auditor legacy
  Executor без `AgentID` допускается. Проверка включает терминальные Runs и историю смены
  selection, работает при startup validation и до durable Save на переходе
  (`TestCanonicalTransitionRejectsFormerReviewerOrAuditorAsExecutorWithoutSave`).
- (тесты) Отказы с известным `AgentID` покрыты
  `TestCanonicalStartupRejectsFormerReviewerOrAuditorAsExecutor`,
  `TestCanonicalStartupRejectsExecutorMatchingOnlyPriorRoleHistory` и
  `TestCanonicalTransitionRejectsFormerReviewerOrAuditorAsExecutorWithoutSave`; без
  проверяемого `AgentID` — `TestCanonicalStartupRejectsExecutorWithoutStableAgentIDAfterReviewOrAudit`
  и `TestCanonicalTransitionRejectsExecutorWithoutStableAgentIDAfterReviewOrAudit`.
  Совместимость до Reviewer/Auditor проверяет
  `TestCanonicalStartupAllowsExecutorWithoutStableAgentIDBeforeReviewOrAudit`. Положительные
  случаи — `TestSameAgentMayBeExecutorInAnotherTaskAfterReview` и
  `TestSameAgentMayReviewAgainInSameTask`; обратный порядок назначений (сначала
  Reviewer/Auditor, затем Executor) проверяет
  `TestFormerReviewerOrAuditorCannotLaterBecomeExecutor`.
## 2026-10-09 — Нормативная запись решения OWNER R137: конфликт и эскалация одним commit ([#28](https://github.com/naghuale/coord/issues/28))

- (документация) `docs/plans/p0-review-audit/DESIGN.md`: решение владельца по строке
  «Порядок conflict fact и Owner escalation» — один durable commit (вариант A):
  Conflict Event → связанный Discussion Fact → OWNER Escalation Event → Task переходит в
  предусмотренное состояние ожидания решения → при необходимости Outbox → один durable
  commit. Conflict Event и Discussion Fact имеют меньший EventSeq, чем эскалация; все
  записи одной Task и одной новой ревизии; эскалация и Discussion Fact ссылаются на
  ConflictID, Discussion Fact — также на исходные Review/Audit results; новое состояние
  публикуется только после успешного commit, при ошибке commit не видна ни одна запись;
  повтор команды возвращает прежний receipt и не создаёт вторую эскалацию. Статус строки
  §7 — «Решено R137: принят вариант A».
- (документация) DESIGN.md: вариант с двумя commit и recovery по conflict ID удалены как
  отклонённые; отдельный recovery для состояния «конфликт записан, эскалации ещё нет»
  не требуется, такого промежуточного канонического состояния не возникает. Открытых
  вопросов в §7 и §11 остался один — обязательные payload-поля Discussion node/edge.
- (документация) `docs/ADR-0001`: после CM-08 добавлено правило «CM-08b — Конфликт и
  эскалация к OWNER в одном durable commit (R137)» с требованиями R137 и одной строкой
  «Проверка: не реализовано». Состояние ожидания решения согласовано с TASK-MODEL TM-11:
  `CurrentStage=OWNER_DECISION` и `Status=BLOCKED`. Текст CM-08 не менялся.
- (документация) DESIGN.md, матрица тестов §9: строка, описывавшая отклонённый вариант
  с двумя commit и recovery по conflict ID, заменена на строку для атомарной записи
  конфликта и эскалации одним durable commit одной ревизии: при ошибке commit нет ни
  одной из записей ни в памяти, ни на диске, а повтор команды после успеха возвращает
  прежний receipt без второй эскалации; мутанты — записать conflict/fact отдельным
  commit, опубликовать память до durable commit или создать вторую эскалацию при повторе.
  Строка про запись конфликта до Owner escalation не менялась. Имена тестов в матрице не
  привожу: они ещё не написаны, а проверка CHANGELOG требует существования упомянутых
  имён.
- (документация) TASK-MODEL не менялся: правила о порядке конфликта и эскалации в нём
  нет, есть только TM-11 про ожидание решения и TM-23a/TM-23b про независимость ролей.

## 2026-10-09 — Различие областей R127 и продукта Coord в ADR-0008 ([#24](https://github.com/naghuale/coord/issues/24))

- (документация) `docs/ADR-0008-AUTONOMOUS-COMMIT.md` (статус Proposed не менялся): добавлен
  раздел «Отличие от R127» — R127 (AGENTS.md, Commit Readiness) регулирует процесс
  разработки этого репозитория, ADR описывает функцию продукта Coord; это разные
  области, ADR не пересматривает R127 и не согласуется с ним, а R127 не является
  доказательством реализации или принятия автокоммита продукта.
- (документация) Из ADR удалены формулировки, связывавшие его с R127 как нормой, которую
  он пересматривает или с которой согласуется: «Этот ADR пересматривает ту норму и
  только её»; «Согласование AC-01, AC-02 и AC-09 с R127 — при рассмотрении ADR OWNER»;
  «действующие правила для них — AGENTS.md (R127)» в разделе о публикации; «иначе
  действует AGENTS.md, раздел “Commit Readiness” (R127)» в AC-01 и в критериях
  соответствия; «Норма `AGENTS.md` действует до выполнения prerequisites; документ не
  отменяет её задним числом» в разделе «Последствия» — ограничение уже есть в AC-01. В AC-01 и критериях условие сохранено без ссылки на R127: автокоммит
  Coord не действует до выполнения prerequisites. Ничего не придумано вместо R127 на
  уровне продукта.
- (документация) Сохранена фактическая ссылка в разделе «Назначение»: сегодня commit
  создаёт координатор-агент, а не runtime Coord — это описание текущего состояния
  процесса, а не норма продукта.
## 2026-10-09 — Документация по тесту независимости до replay (#26) ([#26](https://github.com/naghuale/coord/issues/26))

- (документация) `TestAgentCommandRoleIdentityCollisionRejectsPriorReceiptBeforeReplay` отмечен
  как прямое доказательство порядка: команда сохранила receipt, Executor Run получил
  `AgentID` Reviewer, и повтор того же кадра отклоняется `NO_INDEPENDENT_REVIEWER` до
  replay lookup — `replayed` остаётся false, handler не вызван, durable Save не было,
  состояние и каноническое состояние store не изменились. Мутант с проверкой
  независимости после replay lookup этим тестом убивается.
- (документация) ADR-0001 (CM-03, CM-15) и TASK-MODEL (TM-23, TM-29), а также блок
  «Часть 4» в `docs/TARGET-ARCHITECTURE.md`: «порядок этой проверки до replay прямым
  тестом не доказан (#26)» заменено на факт — независимость роли проверяется до replay
  lookup. В CM-03 и TM-23 факт перенесён в подтверждённую часть, а «не реализовано:»
  оставлено только за реальным остатком (проверка до назначения не подключена).
- (документация) ADR-0001 CM-03a и TASK-MODEL TM-23b: тест добавлен в список подтверждающих
  в строке «Проверка: частично — …».
## 2026-10-09 — Нормативная запись решений OWNER R132–R136 ([#23](https://github.com/naghuale/coord/issues/23))

- (документация) R132 — `docs/ROADMAP.md`: порядок этапов до P0-3 сохраняется (Review/Audit,
  затем Discussion Graph, затем Shadow Validation, затем приёмка на границе этапа, и только
  потом P0-3 с #6 и #7); часть 4 AgentSession закрывает техническую зависимость от #5, но
  порядок не отменяет; до P0-3 по #6 и #7 разрешены только проектирование, уточнение
  контрактов, тестовая матрица, проверка зависимостей и фиксация открытых вопросов;
  production-код #6 и #7 не пишется; иной порядок требует отдельной правки ROADMAP с
  обоснованием.
- (документация) R132 и требование о проверках — `docs/ROADMAP.md` добавлен критерий для
  будущих срезов: каждое правило получает отрицательные тесты и проверку мутациями, а
  изменения, затрагивающие разные агрегаты, не объединяются в один срез реализации.
- (документация) R133 — ADR-0001 и TASK-MODEL: для одной Task запрещено назначать Executor
  агента, который ранее в этой Task был REVIEWER или AUDITOR; правило действует на всю
  историю Task, проверка идёт по устойчивому `AgentID` и полной `RoleSelectionHistory`, а не
  по адресу сессии или имени frontend. Статус «Проверка: не реализовано» — кода и теста в
  дереве нет. Прежняя фраза «такое ограничение не установлено и остаётся открытым до
  отдельного решения OWNER» заменена.
- (документация) R134 — ADR-0001 (CM-03a) и TASK-MODEL (TM-23b): две отдельные
  структурированные причины `NO_INDEPENDENT_REVIEWER` и `NO_INDEPENDENT_AUDITOR` с полной
  семантикой из решения; они не объединяются в общее `NO_AGENT`. Статус «частично»: раздельные
  причины и отказ в канонической границе команд есть, durable blocked-состояние Task с
  причиной, проверенная выборка кандидатов и пересчёт eligibility — не реализованы.
- (документация) R135 — ADR-0001 (CM-08a) и TASK-MODEL (TM-04a): в `CanonicalState`
  хранятся только метаданные Artifact, содержимое внешнее; перечислены минимальные поля и
  правила, включая обязательный digest для используемых Review/Audit артефактов и запрет
  переписывать старый digest. Статус «не реализовано»: полей метаданных Artifact в store
  нет, ссылки `ARTIFACT` в результатах отклоняются fail-closed.
- (документация) R136 — ADR-0001 (CM-10a) и TASK-MODEL (TM-06a): состав Discussion Graph,
  перечень запретов (не менять Status/Stage, не назначать роль, не принимать Result, не
  заменять Review/Audit, не снимать blocker, не создавать решение OWNER, не быть источником
  recovery) и граница P0-2 — минимальный граф и ссылочная целостность. Статус «не
  реализовано»: Discussion Graph в коде отсутствует.
- (документация) Перечень закрытых вопросов #8 (R126, R133, R134, R135, R136) добавлен в
  ADR-0001 со ссылкой на действующие формулировки; в нормативных документах отдельного
  перечня открытых вопросов #8 ранее не было.

## 2026-10-09 — Регрессионный тест: независимость роли проверяется до replay ([#26](https://github.com/naghuale/coord/issues/26))

- (тесты) Добавлен `TestAgentCommandRoleIdentityCollisionRejectsPriorReceiptBeforeReplay`: после того как receipt команды сохранён, повтор той же команды при конфликте идентичности ролей отклоняется `NO_INDEPENDENT_REVIEWER` до replay — без вызова handler и без записи состояния. Мутант с проверкой независимости после replay lookup этим тестом убивается.

## 2026-10-09 — Фактическое состояние agent command boundary после части 4 (#5) ([#22](https://github.com/naghuale/coord/issues/22))

- (документация) ADR-0001 (CM-13, CM-14, CM-15) и TASK-MODEL (TM-27, TM-28, TM-29):
  строки «Проверка» приведены к коду `CanonicalStore.ApplyAgentCommand`
  (`store/canonical_agent_command.go`) и `transport.AuthenticatedFrame` /
  `SignedClient.IncomingAuthenticated` / `AuthenticatedAdapter.ReceiveAuthenticated`
  (`transport/signed.go`): подпись и совпадение с телом, получатель, сессия и поколение
  до replay, Run, эпоха и Attempt, роль и Stage, независимость роли — проверяется в канонической границе без durable Save
  (`TestAgentCommandRejectsRoleIdentityCollisionBeforeReplay`), причём порядок этой проверки до replay прямым тестом не
  доказан ([#26](https://github.com/naghuale/coord/issues/26)); затем replay и дедупликация без записи, CAS для новой команды и один commit с
  переходом, событием, receipt и подтверждением в Outbox; actor берётся из проверенного
  назначения Run, а не из тела команды; обязательный `AgentID` проверяется и при
  назначении Task-scoped Run (`validateRunAgentSessionBinding`), и на входящей команде
  для всех ролей. Фразы «отклонение команд до replay относится к части 4 и пока
  не реализовано» заменены на утверждение о реализованной границе.
- (документация) Остаток «не реализовано» одинаков и назван явно: production command path
  не подключён — `ApplyAgentCommand` вызывается только из тестов, реальный вход остаётся
  legacy `Coordinator.Handle` (`coordinator.go`); `OwnerCredential` не реализован; команд
  управления `AgentSession` (регистрация, ротация ключа, смена адреса) в границе нет;
  Executor RESULT (#6) не подключён — P0-3.
- (документация) ADR-0001 CM-03 и TASK-MODEL TM-23: «проверка до назначения в agent command
  boundary не существует» заменено на факт — каноническая граница проверяет независимость
  через `validateCanonicalRoleIndependence`
  (`TestAgentCommandRejectsRoleIdentityCollisionBeforeReplay`,
  `TestAgentCommandAppliesRoleIndependenceFromCommittedRun`), порядок этой проверки до
  replay прямым тестом не доказан ([#26](https://github.com/naghuale/coord/issues/26)),
  но проверка до назначения не реализована, потому что production command path не
  подключён и назначение идёт через legacy `Coordinator.Handle`.
- (документация) `docs/plans/p0-2-task/AGENT-COMMAND-BOUNDARY.md`: фразы об отклонении
  команд до replay как о нереализованной части 4 в §5.1 (решения R125) и в §5.3 приведены
  к реализованной границе с оговоркой о неподключённом production command path.
- (документация) `docs/TARGET-ARCHITECTURE.md`, строка AgentSession (CM-13, TM-27): добавлен
  блок «Часть 4» с составом transport и store границы и списком проверок; список
  «Не реализовано» приведён к фактическому остатку P0-3.
- (документация) Текст норм, статус ADR и порядок R121 не менялись: правились только строки
  «Проверка» и фразы о статусе реализации. Каждый названный тест проверен на существование и
  на соответствие утверждению рядом (`grep func` по `*_test.go` и чтение тела теста).
## 2026-10-09 — Удалён повторный заголовок в CHANGELOG ([#21](https://github.com/naghuale/coord/issues/21))

- (документация) Заголовок «# История изменений» повторялся дважды начиная с
  ae45a88 (#18); повтор удалён, записи не менялись.

## 2026-10-09 — Каноническая граница агентских команд AgentSession ([#5](https://github.com/naghuale/coord/issues/5))

- `transport.AuthenticatedFrame` сохраняет проверенный подписанный кадр, sender,
  ключ и generation; ключ разрешается через внедрённый интерфейс AgentSession,
  legacy `Peers` не является fallback. `CanonicalStore.ApplyAgentCommand` повторно
  проверяет подпись на границе store до блокировки, а текущую привязку, поколение и
  совпадение ключа — под `s.mu`, затем выполняет role/Stage и Fence checks до
  replay, а Revision CAS — после replay только для новой команды; commit
  объединяет переход, Event, Inbox receipt и Outbox acknowledgment
  (`TestAgentCommandCommitsEventReceiptOutboxAtomically`,
  `TestAgentCommandReplayReturnsPriorReceiptWithoutWriting`).
- Сценарии и проверки: `TestAgentCommandRejectsFabricatedAuthenticatedFrame`,
  `TestAgentCommandRejectsEnvelopeDifferentFromSignedBytes`,
  `TestAgentCommandCopiesSignedBytesBeforeReadingOtherFrameFields`,
  `TestAgentCommandRejectsStaleSessionGeneration`,
  `TestAgentCommandRejectsStaleFence`,
  `TestAgentCommandRejectsSignedFrameFromDifferentKeyAtCurrentGeneration`,
  `TestAgentCommandRejectsSessionAgentIDMismatchBeforeReplay`,
  `TestAgentCommandRejectsRunGenerationAfterKeyRotation`,
  `TestAgentCommandRejectsSessionBeforeStaleFence`,
  `TestAuthenticatedAdapterRejectsWrongRecipient`,
  `TestSignedClientResolverErrorDoesNotFallbackToPeers`,
  `TestSignedClientRejectsZeroResolvedGeneration`,
  `TestAgentCommandRejectsAttemptMismatch`,
  `TestAgentCommandRevokedSessionDoesNotReturnPriorReceipt`,
  `TestAgentCommandReplayAfterRevisionGrowthReturnsPriorReceipt`,
  `TestAgentCommandBoundaryResolvesActorInsideLock`,
  `TestAgentCommandBoundaryRejectsKnownRunIDWithoutAuthenticatedSession`,
  `TestAgentCommandRejectsRoleIdentityCollisionBeforeReplay`,
  `TestAgentCommandCommitsEventReceiptOutboxAtomically`,
  `TestAgentCommandRejectsInboxWithoutCommittedAcknowledgement`,
  `TestAgentCommandTransportDeliveryIsNotAcceptance`,
  `TestSignedClientAuthenticatedFrameCopiesReceivedSignedBytes`,
  `TestAgentCommandLegacyPeersDoNotAuthorizeAgentCommand` и
  `TestAgentCommandIgnoresActorIDFromBody`.
- (документация) Ограничения: production Coordinator/Telepathy command path ещё не подключён;
  legacy `Coordinator.Handle` остаётся действующим для production сообщений.
  OwnerCredential/Owner command boundary, управление AgentSession командами и
  production integration остаются P0-3; schema остаётся v1. Эта запись описывает
  библиотечную границу и её тестовый transport integration, а не production
  authorization path.

## 2026-10-09 — Нормативная запись решений OWNER R128–R131 по Q1–Q4 ([#20](https://github.com/naghuale/coord/issues/20))

- (документация) Записаны четыре решения OWNER, принятые 2026-10-09 по вопросам Q1–Q4
  раздела 13 `docs/plans/owner-console/ANALYSIS.md`: R128 (Q1) — факты публикации
  (commit, push, CI, merge, deploy) наблюдаются во внешних системах и отображаются с
  источником, SHA, временем наблюдения и свежестью; в `CanonicalState` не входят, в
  recovery и переходах Task не участвуют; ошибка или недоступность внешней системы
  даёт UNKNOWN или STALE, но не изменяет каноническое состояние; канонизация
  publication facts отложена до принятия модели автокоммита и публикации, schema и
  ST-01/ST-05 сейчас не меняются. R129 (Q2) — cursor последнего просмотра хранится в
  профиле конкретного клиента, используется только для изменений с прошлого просмотра,
  не означает ознакомления OWNER и не влияет на Task, Attention, блокеры и сроки; для
  доказуемого ознакомления потребуется отдельная подписанная OWNER-команда. R130 (Q3) —
  решение фиксируется вместе с тем переходом, который прямо предусмотрен действующей
  нормой; блокеры и зависимые задачи после commit пересчитываются как проекции;
  массовые мутации зависимых Task не выполняются, новые Runs не создаются, Outbox
  формируется только тогда, когда его прямо требует выполняемый нормативный переход;
  если норма не определяет дальнейшее действие, Task остаётся заблокированной с
  конкретной причиной, а не разблокируется эвристически. R131 (Q4) — web остаётся
  read-only и показывает Overview, Tasks, Attention, Explain и Evidence; кнопка решения
  в web не выполняет команду и может показать точную CLI-команду и данные challenge, но
  не подписывает и не отправляет решение; `coord decide` проходит через защищённый Owner
  command boundary и `OwnerCredential`; варианты с командами из браузера требуют
  отдельного security design и до его принятия не реализуются.
- (документация) `docs/plans/owner-console/ANALYSIS.md`: раздел 13 теперь содержит
  решение по каждому вопросу с номером и ссылкой, отклонённые варианты и их
  последствия сохранены как обоснование выбора; карточки разделов 3, 4.2, 6, 8 и 9
  переведены с «вопрос Q1/Q2/Q4» на соответствующее решение; подраздел 2.3 фиксирует
  границу браузера по R131; таблица срезов раздела 12 и критерии приёмки раздела 14
  дополнены пунктами по R128, R129, R130 и R131.
- (документация) ADR-0001, раздел «Read-only API и Dashboard»: добавлены три абзаца со
  ссылками на решения — внешние наблюдения публикации не являются каноническими
  данными (R128), cursor последнего просмотра локален для клиента, хранится в его
  профиле и не входит в каноническое состояние (R129), граница браузера
  зафиксирована, решение принимается через CLI (R131). Другие нормы ADR не менялись.
- (документация) TASK-MODEL, TM-24: в заголовок и тело добавлена граница побочных
  эффектов решения по R130; строка «Проверка:» осталась «не реализовано» — код для
  R128–R131 не написан. В абзац об эскалации Owner decision добавлена ссылка на R130:
  зависимые Task массово не мутируются, их блокеры пересчитываются как проекции, и
  если норма не определяет дальнейшее действие, Task остаётся заблокированной с
  конкретной причиной.
- (документация) ROADMAP: в строку «До P0-3 | Owner command boundary» добавлена
  ссылка на R130, в строку «После ADR-0006 | Dashboard и Project Map» — на R128, R129
  и R131, в строку «Отдельный design | Политика внешней синхронизации» — отложенную
  до принятия модели автокоммита канонизацию publication facts. Порядок этапов не
  изменился.
- (документация) Release не перечислен в R128: это зафиксировано как Gap «решение
  OWNER R128 release не перечисляет; отдельная норма», решение не расширено.

## 2026-10-09 — Уточнение границ «не реализовано» у CM-03 и TM-23 ([#19](https://github.com/naghuale/coord/issues/19))

- (документация) CM-03 в ADR-0001 больше не называет отсутствующим канонический
  сценарий session-address. Отсутствует только отдельный изолированный тест на смену
  адреса сессии; сам сценарий покрыт каноническими проверками совпадения, которые
  используют разные session addresses и падают при сравнении по `Session` вместо
  `agent_id`. Формулировка «проверка до назначения в agent command boundary не
  существует» сохранена — она про agent command boundary, а не про канонический
  переход.
- (документация) TM-23 в TASK-MODEL: в «не реализовано» оставлено подключение
  проверки к production command path назначения, включая recovery и новые session
  адреса, — отказ кандидатного назначения до durable Save уже реализован в
  `ValidateCanonicalTransition` и покрыт `TestNoIndependentReviewerOrAuditorFailsClosed`,
  который проверяет, что отказ не меняет Task/Run/Fence и не выполняет durable Save.
  Вторым пунктом оставлено поведение «Task остаётся blocked на Stage REVIEW с причиной
  `NO_INDEPENDENT_REVIEWER`» как durable или derived факт: канонические проверки
  фиксируют отказ, но не состояние и Stage Task. Упоминание legacy-теста про Auditor
  удалено: он не проверяет контур независимости ролей, проверяется только Reviewer.
- (документация) Статус ADR и формулировки норм CM-03 и TM-23 не менялись: изменены
  только строки «Проверка:» и «не реализовано», перечисляющие факты покрытия и
  непокрытого поведения. Запись #9 не переписывается — она опубликована.

## 2026-10-09 — Анализ концепта Owner Console по нормам и коду ([#18](https://github.com/naghuale/coord/issues/18))

- (документация) Добавлен `docs/plans/owner-console/ANALYSIS.md`: разбор концепта Owner
  Console, переданного OWNER 2026-10-09, по 14 разделам вводной OWNER-1, с карточками
  Target/Current/Gap для элементов интерфейса и сверкой каждой ячейки с кодом на
  `51275c1` (`store/canonical.go`, `task/*.go`, `fsm/fsm.go`, `agentsession/*.go`,
  `cmd/coordd`, `.github/workflows/ci.yml`). Нормы не правятся и не предлагаются к
  правке, решения OWNER не выбираются.
- (документация) Зафиксированы границы доверия: read channel строит OwnerView и ничего
  не изменяет; кнопка «Принять A» — клиент отдельного command channel с подписанным
  challenge; web остаётся read-only до отдельного security design. Каждая граница
  сопровождена фактом по коду и Gap.
- (документация) Сверка с кодом выявила расхождения вводных с деревом: журнал
  `DomainEvent` содержит только `TASK_CREATED`, `TASK_OWNERSHIP_TRANSFERRED`,
  `REVIEW_RESULT_RECORDED`, `AUDIT_RESULT_RECORDED`, поэтому «назначение Run»,
  «переход Task», «решение OWNER» и «отзыв AgentSession» как события журнала
  отсутствуют; типа события решения OWNER нет ни в коде, ни в норме, а `OWNER_DECISION`
  в TASK-MODEL — Stage, а не тип события; key fingerprint вычисляем из публичного
  ключа и не хранится; `CommandID` в схеме доказательств — это `MessageID` в
  `InboxReceipt`; `GET /runs` — supervisor control endpoint, а не Task read API;
  структурированного blocker у `Task` нет, `Run.Blocker` приходит строкой из
  heartbeat.
- (документация) Раздел 13 содержит ровно четыре вопроса OWNER: publication facts,
  точка последнего просмотра, побочные эффекты решения OWNER, web command channel —
  с вариантами и последствиями, без выбора. Вопросы про имя события, формат
  fingerprint, `discover` и стартовую карточку вынесены в раздел 12 как уточнения к
  ADR-0006 и ALIGNED_UX либо как расхождения с нормой, поскольку отдельного решения
  OWNER они не требуют.
- (документация) Документ переработан по вводной OWNER-5-REWORK.md: карточка каждого
  элемента приведена к 11 полям (Target, Current, Gap, Source, Data class, Freshness,
  Trust boundary, Affects Task transitions, UNKNOWN/stale behavior, Current evidence,
  Required dependency), пустых ячеек нет — неприменимое помечено с причиной, необоснованное
  — как UNKNOWN с указанием, что нормативный источник не определён.
- (документация) По вводной OWNER-5 уточнены факты: R127 вынесен в отдельный подраздел
  12.1 как правило координатора-разработчика, не норма продукта, и в разделах 4–9 больше
  не используется; общей canonical revision нет, показаны `Task.Revision` и schema version,
  общая revision — Gap к ADR-0006; `Run.TargetSHA` отнесён к цели назначения, а не к хешу
  результата, и подтверждения привязки результата к дереву нет; Inbox не описан как
  очередь ожидания, поскольку `CanonicalState.Inbox` — durable-карта обработанных
  команд, источника для «Inbox pending» нет.
- (документация) По находкам Codex уточнено: read-only API для Task и OwnerView отсутствует,
  существующий control endpoint `GET /runs` перечислен как control interface с его recovery-семантикой;
  `OwnerCredential` нормирован CM-12, но не реализован; утверждения о Title, Description и
  выдаче TaskID разнесены по своим тестам, а для Title и Description прямой тест не найден.
- (документация) По замечанию ревью уточнены рамки: Dashboard read-only отнесён к
  CM-02 и разделу «Read-only API and Dashboard» ADR-0001 (CM-07 — это hard policy,
  он не про Dashboard); запрет участия производных в recovery подтверждён только для
  Brief через BR-03 (ADR-0007 Proposed), для OwnerView общей нормы нет; R127 отнесён
  к правилу работы координатора-разработчика, а не к нормам продукта — норма о
  публикации в Coord есть только ADR-0008 (Proposed); CAS по revision подтверждён
  тестом `TestApplyInboxCommandRejectsStaleExpectedRevision`; строка «Проверка:» CM-10
  о неизменности поколений помечена как расхождение с кодом — каноническое поколение
  введено частями 1–3 AgentSession, отдельная задача, здесь не правится; Q3-A
  сформулирован однозначно — входит только переход, предписанный CM-06 и TM-24, и он
  не содержит побочных мутаций. Карточки разделов 3, 4, 6, 7, 8 и 9 дополнены
  Freshness и Trust boundary до восьми полей критерия готовности.
- (документация) По OWNER-5 п.2 правила commit/push/merge/deploy и упоминания R127
  оставлены только в подразделе 12.1 о процессе координатора-разработчика: из
  раздела 8 убран абзац-метакомментарий про исключение, строка «Merge в main» в
  Target описывает только наблюдательный факт (SHA, источник, время), а из Q1-B
  удалена ссылка на R127. В §9 удалён дословный дубль правила приёмки 9.
- (документация) Макет помечен как условный пример: карточка `OW-17 «AgentSession
  address reuse`» закрыта решением R126, данные макета не отражают текущее
  состояние. Первичные находки C1–C9 проверены по источникам; C9 уточнён — механизм
  изменения графа нормирован ADR-0002 DG-08.
- (документация) Карточки переписаны по факту каждого элемента вместо шаблона: поле
  Current evidence теперь совпадает с текущим состоянием строки, для отсутствующих
  реализаций указано «реализация не существует», для существующих полей названы
  конкретные проверки или указано, где искали и что не нашли; поле Affects Task
  transitions согласовано с текущим поведением (в том числе у Result); «Inbox/Outbox
  pending» разделены, «Source tree hash» переведён в Gap, строка Revision приведена к
  формуле OWNER-5, таблица доказательств переведена на 11 полей, зависимости Changes и
  Первый запуск исправлены на ADR-0006 и `OwnerCredential`. В разделе 8 разделены
  внешние наблюдения публикации и целевой local commit из ADR-0008 (Proposed),
  который вводит `COMMIT_CREATED` и COMPLETED. В разделе 11 TaskID и Task.Title/Description
  разнесены по своим доказательствам, а TaskID adoption отмечен как изолированный в
  `internal/taskservice` по его комментарию.
- (документация) По третьему проходу Codex уточнено: challenge «Принять A» связывает
  MessageID, TaskID, действие, payload hash и Task Revision, а для команды по графу —
  ещё Graph Revision (COMMAND-BOUNDARY §3.4, DG-07/DG-08); `/readyz` отмечен как не
  равный полной готовности модели Task-wide; вариант «integration journal» в вопросе Q1
  вариант «integration journal» в вопросе Q1 сформулирован как архитектурное
  последствие: требуется решение об ADR, владении и границах consistency, а ST-01
  и ST-05 затронуты только если журнал войдёт в canonical command/commit boundary
  или в миграцию canonical schema; `AgentSession` отнесён к
  привязке, а не к сетевой доступности; метаданные артефакта отмечены как
  зарезервированные, но не определённые (RA-1).

## 2026-10-09 — Проверка CM-03 и TM-23 дополнена каноническими тестами ([#9](https://github.com/naghuale/coord/issues/9))

- (документация) Строка «Проверка:» у CM-03 в ADR-0001 перестала называть только
  legacy-тест. Добавлены канонические тесты RA-2 с указанием, что проверяет каждый:
  `TestReviewerRejectsExecutorAgentIDAcrossHistory` и
  `TestAuditorRejectsExecutorAgentIDAcrossTaskRuns` — отказ по
  `NO_INDEPENDENT_REVIEWER` и `NO_INDEPENDENT_AUDITOR`, когда `agent_id` совпадает с
  любым текущим или прежним Executor этой Task; `TestIndependentAuditorMayUseSameModel` —
  имя модели не признак зависимости. Проверено по коду
  (`store/canonical_independence_test.go`, валидатор `store/canonical_independence.go`).
- (документация) Перечислены все проверки из `store/canonical_independence_test.go`,
  покрывающие формулировку CM-03: fail-closed на `UpdateCanonical`, отказ на startup и
  на переходе, пустой `AgentID` и отсутствие selection, история в границах Task, агент,
  позже ставший Executor, соседний EventSeq, поведение role history. В CM-03 добавлено
  «не реализовано»: проверка до назначения в agent command boundary не существует, а
  канонический валидатор сравнивает только `agent_id`, поэтому отдельного сценария
  «тот же `agent_id`, другой адрес сессии» в нём нет по построению — в отличие от
  legacy-пути, который этот случай покрывает.
- (документация) Зеркальная запись в TASK-MODEL, TM-23 о независимости Reviewer,
  синхронизирована: перечислены только проверки Reviewer-контура, а в «не реализовано»
  добавлены части нормы, которые проверки не покрывают — проверка до назначения, включая
  recovery и новые session адреса, и поведение «Task остаётся blocked на Stage REVIEW с
  причиной `NO_INDEPENDENT_REVIEWER`»: канонические проверки фиксируют отказ валидации,
  но не состояние и Stage Task. Legacy-тест про Auditor в TM-23 не указан, так как он
  проверяет контур AUDITOR, а не Reviewer. Статус ADR и формулировки норм CM-03 и TM-23 не менялись.

## 2026-10-09 — Решение OWNER R127: границы commit и публикации ([#17](https://github.com/naghuale/coord/issues/17))

- (документация) Координатор сам создаёт локальные коммиты после независимой приёмки и
  всех проверок Commit Readiness: он вычисляет Readiness, называет причины блокировки
  и при READY_FOR_COMMIT создаёт локальный commit, после чего отчитывается OWNER —
  SHA, изменённые файлы и результаты проверок. Требования к прогону не изменились.
- (документация) READY_FOR_COMMIT разрешает только локальный commit — не публикацию,
  не merge и не deploy. Push, merge в main, deploy, release, изменение remote и
  операции с реальными агентскими сессиями отдельной команды OWNER не требуют, но у
  каждого своя проверка: push — только принятых локальных коммитов; merge в main — на
  границе этапа при зелёном CI; deploy и release — при зелёном CI на этом коммите.
- (документация) Изменения gate-файлов (AGENTS.md, Makefile, CI, `.gitignore`,
  тестовые механизмы) отдельной приёмки OWNER не требуют; независимое ревью и
  проверки для них сохраняются. Поэтому «меняются gate-файлы» и «нужно изменить
  реальные агентские сессии» удалены из списка причин остановки координатора;
  остальные причины, включая приёмку работы самого координатора, сохранены.
- (документация) Force-push и переписывание опубликованной истории запрещены.
  Абзац о пересмотре политики автокоммитов отдельным ADR сохранён: он остаётся в силе,
  пока ADR-0008-AUTONOMOUS-COMMIT.md в статусе Proposed.
- (документация) AGENTS.md — gate-файл; по R127 отдельная приёмка OWNER не требуется,
  ревью Claude и Codex.
- (документация) По R101 приведены в соответствие с R127 три места в других
  документах: TARGET-ARCHITECTURE — строка «Автономная приёмка и git commit» теперь
  говорит, что до пересмотра действует локальный commit координатором по AGENTS.md
  «Commit Readiness» (R127); ADR-0008-AUTONOMOUS-COMMIT — в назначении указано, что
  commit создаёт координатор-агент, а не runtime Coord; в разделе о действующей норме
  — что локальный commit после `READY_FOR_COMMIT` создаётся координатором-агентом без
  отдельной команды OWNER, а этот ADR переносит фиксацию в runtime Coord; в разделе
  «Локальный commit не равен публикации» фраза «Push, merge и deploy остаются за
  OWNER» заменена на границу области Proposed ADR со ссылкой на действующие правила
  AGENTS.md (R127). Статус ADR-0008 (Proposed) не менялся.
- (документация) В ADR-0008 добавлен абзац, отделяющий ограничения AC-01…AC-13 и
  критерии соответствия от действующей нормы: они описывают целевое поведение runtime
  Coord по Proposed ADR и до его принятия не действуют, а упоминания ручного коммита,
  публикации и изменений gate-файлов за OWNER отнесены к состоянию на момент
  предложения (2026-10-08); с 2026-10-09 для координатора-агента действует AGENTS.md
  (R127), а согласование AC-01, AC-02 и AC-09 с R127 отнесено к рассмотрению ADR.
  Две ссылки на ручной коммит как на действующий порядок заменены на «действует
  `AGENTS.md`, раздел «Commit Readiness» (R127)». Текст AC и критериев по существу
  не менялся, статус ADR и строки «Проверка:» не тронуты.

## 2026-10-09 — Нормативная запись решения OWNER о повторной регистрации отозванного адреса ([#16](https://github.com/naghuale/coord/issues/16))

- (документация) OWNER подтвердил номер **R126**: отозванный адрес `AgentSession` не
  регистрируется повторно никогда, вернувшийся агент регистрируется на новом адресе
  с прежним `AgentID`. Основание — исключить ABA: повторная регистрация с поколения 1
  совпала бы с огороженным Run (тот же адрес и поколение) и сняла бы ограждение R125.
- (документация) R126 записан в ADR-0001 (CM-13), TASK-MODEL (TM-27) и проект границы
  команд агентов: шапка решений, §5.1 и ссылка на нормативную запись NORM-AS-4.
- (документация) В TARGET-ARCHITECTURE утверждение об открытом вопросе #8 заменено на
  правило R126 со ссылками на `RegisterAgentSession`, `ReplaceAgentSessionAddress`
  (`ErrAgentSessionAddressExists`) и `agentsession.ValidateHistory`.
- (документация) Поведение уже обеспечено кодом и проверено:
  `TestCanonicalAgentSessionAddressChangeRevokesAndRegistersInOneCommit` и
  `TestCanonicalAgentSessionAddressChangeRejectsInactiveOrExistingAddressWithoutWrite`
  (`store/canonical_agentsession_test.go`), `TestValidateHistoryRejectsEventsAfterRevocation`
  (`agentsession/session_test.go`).
- (документация) Статусы ADR и строки «Проверка:» не менялись: CM-13 и TM-27 остаются
  «не реализовано», новых правил сверх текста решения OWNER не введено.

## 2026-10-09 — Номера строк в ссылках на store заменены именами ([#12](https://github.com/naghuale/coord/issues/12))

- (документация) В `AGENT-COMMAND-BOUNDARY.md` устаревшие номера строк в ссылках на
  `store/canonical.go` заменены именами функций и типов, сверенными с кодом:
  `ApplyInboxCommand` (вместо диапазонов внутри неё), `ValidateCanonicalState`,
  `ValidateCanonicalTransition`, `validateTaskOwnershipTransfers`,
  `validateTransferredAttempt`, `validateTaskRunRole`, тип `CanonicalState`,
  интерфейс `store.CanonicalPersistence`. Ссылки на вызовы
  `validateCanonicalRoleIndependence` даны через имя вызывающей функции, а не через
  номера строк.
- (документация) Фрагменты внутри функции описаны словами: «проверка replay в начале
  `ApplyInboxCommand`», «проверка CAS по `Task.Revision`», «начало/тело/хвост
  `ApplyInboxCommand`», «recovery-ветка `validateTaskOwnershipTransfers`». Смысл
  утверждений не менялся.
- (документация) В `COMMAND-BOUNDARY.md` ссылка на требование `TaskID` в
  `ApplyInboxCommand` и в `ADR-0005-STORAGE-MODEL.md` ссылка на интерфейс
  `store.CanonicalPersistence` теперь указывают имена без номеров строк.
- (документация) Ссылки на другие файлы (`agentsession/session.go`,
  `store/canonical_result.go`, `fsm/fsm.go`, `task/event.go`, transport и прочие)
  не менялись; нормы, статусы ADR и строки «Проверка:» не тронуты.

## 2026-10-09 — Нормативная запись решения OWNER по #13 ([#15](https://github.com/naghuale/coord/issues/15))

- (документация) OWNER подтвердил номер **R125** для решения по вопросу #13
  (вариант A). Ротация ключа или отзыв `AgentSession` при активном Run
  разрешены; такой Run огорожен, его команды отклоняются до replay, readiness не
  блокируется, переназначение идёт только через recovery transfer на текущую
  активную привязку. R125 относится к Run, у которого привязка и поколение были
  записаны при назначении, а затем устарели или привязка была отозвана; R123 — к
  Run без записанной привязки. Уточнение фиксирует различие, уже заложенное в
  коде `78a6049` и в вопросе #13, и не вводит новой нормы.
- (документация) R125 записан в ADR-0001 (CM-13) и TASK-MODEL (TM-27) с номером и
  в проект границы команд агентов: шапка решений (§ R125), §5.1 и §5.3. Отклонение
  команд до replay помечено как часть 4 и указано как не реализованное.
- (документация) Статусы ADR и строки «Проверка:» не менялись — CM-13 и TM-27
  остаются «не реализовано»; новых правил сверх текста решения OWNER не
  введено.

## 2026-10-09 — Статус AgentSession после привязки Run к привязке и поколению ([#14](https://github.com/naghuale/coord/issues/14))

- (документация) Строка AgentSession в TARGET-ARCHITECTURE обновлена по коду части 3
  (`78a6049`), по-прежнему одна физическая строка таблицы. Добавлено: `fsm.Run`
  `AgentSessionGeneration` входит в `SameDomainState` (`fsm/fsm.go`); проверки
  привязки Task-scoped Run на хранении — `validateCanonicalAgentSessionBindings` и
  `validateRunAgentSessionBinding`, на назначении и recovery transfer —
  `validateCanonicalAgentSessionAssignments` (`store/canonical_agentsession_binding.go`),
  вызываются из `ValidateCanonicalState` и `ValidateCanonicalTransition`
  (`store/canonical.go`). Ссылки на store-файлы даны именами без номеров строк.
- (документация) Зафиксировано поведение: при назначении требуется текущая активная
  привязка, текущее поколение и совпадение `AgentID`; recovery transfer идёт только
  на текущую активную привязку; `AgentID` не меняется в том же `OwnerSession`; при
  startup отвергаются generation выше привязки, отсутствующая привязка и чужой
  `AgentID`, а stale или отозванная привязка допускается (OWNER
  [#13](https://github.com/naghuale/coord/issues/13), вариант A). По R123 активный,
  то есть нетерминальный, включая `STALLED`, Task-scoped Run без generation
  оставляет `coordd` в diagnostic-only режиме с `/readyz` 503 и без переписи файла;
  Run без `TaskID` не затрагиваются.
- (документация) В списке проверок добавлены все 11 тестов части 3:
  `TestCanonicalStartupRequiresGenerationAndBindingForActiveTaskRun`,
  `TestCanonicalStartupAllowsStaleGenerationAndRevokedExistingBinding`,
  `TestCanonicalStartupRejectsForgedRunGenerationAndAgentID`,
  `TestLegacyRunDoesNotRequireAgentSessionGeneration`,
  `TestCanonicalAssignmentTakesCurrentActiveAgentSessionBinding`,
  `TestCanonicalRunGenerationCannotChangeWithoutRecoveryTransfer`,
  `TestCanonicalRecoveryTransferChangesAgentWithOwnerAndHistory`,
  `TestCanonicalRecoveryTransferRequiresCurrentActiveAgentSession`,
  `TestCanonicalRecoveryTransferCannotChangeAgentWithoutRoleHistoryAppend`,
  `TestFSMRunDomainStateIncludesAgentSessionGeneration`,
  `TestCoorddReadinessFailsClosedForActiveTaskRunWithoutAgentSessionBinding`.
- (документация) Из «не реализовано» убраны запись поколения в `Run` и readiness
  fail-closed по R123 — они реализованы частью 3. Остались: подключение регистрации и
  ротации к операторскому command path и OwnerCredential, отдельная операция отзыва
  без новой привязки, `AuthenticatedFrame` и порядок проверок R121 (CM-15, TM-29),
  отказ команд stale или отозванной привязки до replay (часть 4). Обязательный
  непустой `AgentID` (R116, CM-14, TM-28) теперь проверяется на хранении для
  Task-scoped Run с привязкой, но не на входящих командах.
- (документация) Нормы не менялись: CM-13 и TM-27 остаются «Проверка: не
  реализовано», статусы ADR и зрелость не завышены.

## 2026-10-08 — Статус AgentSession в карте реализации ([#11](https://github.com/naghuale/coord/issues/11))

- (документация) TARGET-ARCHITECTURE разошёлся с кодом: после части 1
  (`93f36b0`) в нём не было строки AgentSession. Добавлена строка «AgentSession
  (CM-13, TM-27)» со статусом «частично» и со ссылками на код, сверенными по
  файлам: проекция `agentsession.Session` и append-only история
  `agentsession.Event` в типе `CanonicalState` schema v1, поля `AgentSessions` и
  `AgentSessionEvents` (`store/canonical.go`),
  нормализация пустых коллекций при загрузке старого файла
  (`NormalizeCanonicalState` в `store/canonical.go`), проверка истории при
  startup (`agentsession.ValidateHistory` в `ValidateCanonicalState`) и на переходе
  (`agentsession.ValidateTransition` в `ValidateCanonicalTransition`), правила
  проверки в `agentsession/session.go:28-54`, `:61`, `:141`. После части 2
  (`11c36e8`) в строку добавлены store-level операции `RegisterAgentSession`,
  `RotateAgentSessionKey` и `ReplaceAgentSessionAddress`
  (`store/canonical_agentsession.go`), каждая как одна durable запись через
  `UpdateCanonical`, и `NewCanonicalWithClock` (`store/canonical.go`); ссылки на
  store-файлы даны именами, номера строк в `agentsession/session.go` сверены заново и
  не изменились. Перечислены все 12
  проверок `agentsession/session_test.go` —
  `TestValidateHistoryRebuildsRegisteredRotatedAndRevokedSession`,
  `TestValidateHistoryRejectsProjectionThatDisagreesWithEvents`,
  `TestValidateHistoryRejectsProjectionWithDifferentPublicKey`,
  `TestValidateHistoryRejectsEventsWithoutCurrentProjection`,
  `TestValidateHistoryRejectsSkippedGeneration`,
  `TestValidateHistoryRejectsRevocationWithWrongGeneration`,
  `TestValidateHistoryRejectsRotationWithoutChangingPublicKey`,
  `TestValidateHistoryRejectsEventTimeBeforePriorAddressEvent`,
  `TestValidateHistoryRejectsEventsAfterRevocation`,
  `TestValidateHistoryAllowsOneAgentAcrossMultipleAddresses`,
  `TestValidateTransitionAcceptsRevocationAndRejectsProjectionChangeWithoutEvent`,
  `TestValidateTransitionRejectsRewrittenOrDeletedHistory`, а также
  `TestCanonicalValidationChecksAgentSessionHistory` и
  `TestCanonicalFileReadsAndRewritesCreationOnlyV1WithoutFenceField`, и все 7
  проверок части 2 `store/canonical_agentsession_test.go` —
  `TestCanonicalAgentSessionRegistrationPersistsOperatorBinding`,
  `TestCanonicalAgentSessionKeyRotationIncrementsGenerationAtomically`,
  `TestCanonicalAgentSessionRotationRejectsRevokedBindingWithoutWrite`,
  `TestCanonicalAgentSessionAddressChangeRevokesAndRegistersInOneCommit`,
  `TestCanonicalAgentSessionAddressChangeFailureDoesNotPartiallyRevoke`,
  `TestCanonicalAgentSessionAddressChangeRejectsInactiveOrExistingAddressWithoutWrite`,
  `TestNewCanonicalWithClockRejectsNilClock`.
- (документация) В той же строке перечислено, что не реализовано: подключение
  регистрации и ротации к операторскому command path и OwnerCredential, отдельная
  операция отзыва без новой привязки, запись поколения в `Run`, `AuthenticatedFrame`
  и проверка входящих команд по `AgentSession` в порядке R121 (CM-15, TM-29),
  обязательный непустой `AgentID` для любого command-capable Task-scoped Run
  (R116, CM-14, TM-28), readiness fail-closed для старого canonical state без
  действующей `AgentSession` (R123); повторная регистрация отозванного адреса
  отмечена как открытый вопрос
  [#8](https://github.com/naghuale/coord/issues/8). Заголовок строки и колонка
  «Норма» ограничены CM-13 и TM-27: часть 1 не реализует CM-14/CM-15 и
  TM-28/TM-29, поэтому зрелость не завышается.
- (документация) В TARGET-ARCHITECTURE исправлены сдвинутые коммитом `93f36b0`
  ссылки на `store/canonical.go` (в HEAD было `:67-74`, `:87-94`, `:206`, `:818`)
  и затем номера строк в этом файле заменены именами, чтобы вторая часть
  AgentSession снова не сдвигала ссылки: тип `CanonicalState` с полями
  `AgentSessions`/`AgentSessionEvents`, интерфейс `CanonicalPersistence`,
  нормализация в `NormalizeCanonicalState`, проверки истории —
  `agentsession.ValidateHistory` в `ValidateCanonicalState` и
  `agentsession.ValidateTransition` в `ValidateCanonicalTransition`, вызовы
  `validateCanonicalRoleIndependence` в `ValidateCanonicalState` и
  `ValidateCanonicalTransition`. Номера строк в `agentsession/session.go` и
  остальных файлах оставлены и сверены; другие документы не трогались.
- (документация) Нормы не менялись: CM-13 и TM-27 остаются «Проверка: не
  реализовано», строки ADR-0001, TASK-MODEL и статусы ADR не тронуты.

## 2026-10-08 — Каноническая история AgentSession ([#5](https://github.com/naghuale/coord/issues/5))

- (`TestValidateHistoryRebuildsRegisteredRotatedAndRevokedSession`, `TestValidateHistoryAllowsOneAgentAcrossMultipleAddresses`, `TestValidateHistoryRejectsProjectionWithDifferentPublicKey`, `TestValidateHistoryRejectsEventsWithoutCurrentProjection`, `TestValidateHistoryRejectsRevocationWithWrongGeneration`, `TestValidateHistoryRejectsRotationWithoutChangingPublicKey`, `TestValidateHistoryRejectsEventTimeBeforePriorAddressEvent`, `TestCanonicalValidationChecksAgentSessionHistory`, `TestCanonicalFileReadsAndRewritesCreationOnlyV1WithoutFenceField`) добавлены типы текущей `AgentSession` projection и её append-only event history; проверяются согласованность projection с событиями, поколение и монотонность времени; schema v1 принимает старые snapshots без новых полей и записывает их при следующем сохранении.
- (документация) Ограничения части 1: schema остаётся v1, старые файлы читаются; команд регистрации и ротации нет; generation в `Run` нет; аутентификации команд через `AgentSession` нет; readiness при отсутствии привязки пока не блокируется; CM-13 и TM-27 остаются «не реализовано». Повторное использование отозванного адреса — открытый вопрос до части 4 (#8).
- (`TestCanonicalAgentSessionRegistrationPersistsOperatorBinding`, `TestCanonicalAgentSessionKeyRotationIncrementsGenerationAtomically`, `TestCanonicalAgentSessionRotationRejectsRevokedBindingWithoutWrite`, `TestCanonicalAgentSessionAddressChangeRevokesAndRegistersInOneCommit`, `TestCanonicalAgentSessionAddressChangeFailureDoesNotPartiallyRevoke`, `TestCanonicalAgentSessionAddressChangeRejectsInactiveOrExistingAddressWithoutWrite`, `TestNewCanonicalWithClockRejectsNilClock`) добавлены store-level мутации регистрации AgentSession, ротации ключа и смены адреса через отзыв старой привязки и регистрацию новой; проверяются одна durable запись на операцию, transition validation и отказ без записи для неактивной привязки/занятого адреса; время контролируется fake clock.
- (документация) Ограничения части 2: schema остаётся v1; мутации доступны через CanonicalStore, но регистрация/ротация ещё не подключены к операторскому command path и OwnerCredential; отдельная revoke-only операция не добавлена; generation в `Run`, аутентификация агентских команд и блокировка readiness по отсутствующей привязке остаются следующими частями; CM-13 и TM-27 по-прежнему отмечены «не реализовано». Отозванный адрес не переиспользуется; политика такого переиспользования остаётся открытым вопросом #8.
- (`TestCanonicalStartupRequiresGenerationAndBindingForActiveTaskRun`, `TestCanonicalStartupAllowsStaleGenerationAndRevokedExistingBinding`, `TestCanonicalStartupRejectsForgedRunGenerationAndAgentID`, `TestLegacyRunDoesNotRequireAgentSessionGeneration`, `TestCanonicalAssignmentTakesCurrentActiveAgentSessionBinding`, `TestCanonicalRunGenerationCannotChangeWithoutRecoveryTransfer`, `TestCanonicalRecoveryTransferChangesAgentWithOwnerAndHistory`, `TestCanonicalRecoveryTransferRequiresCurrentActiveAgentSession`, `TestCanonicalRecoveryTransferCannotChangeAgentWithoutRoleHistoryAppend`, `TestFSMRunDomainStateIncludesAgentSessionGeneration`, `TestCoorddReadinessFailsClosedForActiveTaskRunWithoutAgentSessionBinding`) добавлены поколение привязки в Task-scoped `Run`, проверка generation/AgentID при назначении и recovery transfer, и fail-closed readiness для активного `Run` без generation; stale generation и уже отозванная, но сохранённая привязка допустимы при загрузке. Смена `AgentID` в пределах того же `OwnerSession` отклоняется даже при append в `RoleHistory`: адрес привязан к одному `AgentID`; `AgentID` меняется только при recovery transfer на другой `OwnerSession`.
- (документация) Ограничения части 3: команды и production assignment path не подключены; проверка AgentSession generation при приёме команд остаётся частью 4. Активный `Run` со stale generation или отозванной привязкой проходит startup validation, по решению OWNER (#13, вариант A) такой Run огорожен: его команды будут отклоняться до replay в части 4, readiness не блокируется, переназначение — только recovery transfer на текущую активную привязку; автоматического backfill и переписи snapshot нет. Schema остаётся v1; CM-13 и TM-27 ещё не полностью реализованы.

## 2026-10-08 — Устранение дрейфа документации: ссылки, дубли, размещение статуса ([#3](https://github.com/naghuale/coord/issues/3))

- (документация) Устаревшие line-ссылки заменены на ID правил и названия
  разделов: проект границы команд агентов, планы P0-2 и P0-daemon, проект
  границы команд Task. Сверка с кодом: `store/store.go:106-133`,
  `daemon/daemon.go:53-79`, `integration/telepathy/main.go:346-366`,
  `integration/telepathy/checks.go:86-100`.
- (документация) В ROADMAP вместо `OwnerAuthSession` указан `OwnerCredential`
  (R110) со ссылками на проект границы команд Task и CM-12; строка P0-3 вместо
  упоминания «принятых проектов» содержит ссылки на оба принятых проекта. В
  проекте границы команд Task устаревшее «файл ещё не создан» заменено ссылкой на
  принятый проект границы команд агентов.
- (документация) Ревизия Crewflow в плане интеграции прямо названа ревизией
  внешнего репозитория: в истории coord такого объекта нет, поэтому проверить
  её локально нельзя.
- (документация) Сведения о состоянии реализации перенесены из ADR-0004,
  ADR-0005 и ADR-0007 в TARGET-ARCHITECTURE, который остаётся единственным
  источником статуса: текущий `/readyz` библиотечного daemon host, Run-only
  storage слой, канонический persistence, платформы snapshot lock, TaskID adoption
  (`taskid.New`, `taskid.ID`, `TestCreateTaskGeneratesCanonicalTaskID`), канонический
  Task aggregate с `store/canonical.go:67-74` и проверками
  `TestCanonicalStateValidatesTaskEventAndRunRelations`,
  `TestCanonicalStoreRejectsTaskMutationWithoutEvent`, порядок владения в
  `cmd/coordd` (`TestCoorddTakesNamespaceLockBeforeStores`), отсутствие read-only
  API для Task и статус Project/Task Brief. В ADR оставлены краткие строки и
  ссылки. Статусы ADR, строки «Проверка:» и решения OWNER не менялись.
- (документация) ADR-0005 больше не определяет правило единственного активного
  writer: оно отсылает к ADR-0001 и TASK-MODEL, сохраняя обязанность storage
  проверять его fail-closed до выдачи владения.
- (документация) Каталог runtime-файлов в ROADMAP заменён ссылкой на
  ARCHITECTURE; недостающие детали (`canonical.json.lock`, `roles.json.lock`,
  `.coord-history`, append-only `daemon.log`, demo и тестовые пути) сначала внесены
  в ARCHITECTURE. Фраза об отсутствии CI в ROADMAP синхронизирована с CI
  `6eb78e3`: в CI есть только общий `make check` на ubuntu-latest и macos-latest,
  задачи с механизмом изоляции по-прежнему нет, норма P0-5 не ослаблена.

## 2026-10-08 — Учёт работ в GitHub Issues ([#10](https://github.com/naghuale/coord/issues/10))

- (документация) AGENTS.md: каждая единица работы ведётся в issue репозитория
  `naghuale/coord` наравне с CHANGELOG; commit и запись CHANGELOG ссылаются на
  issue. Issue — трекер работ и внешняя ссылка (ADR-0003), а не источник истины;
  опубликованная история ради ссылок не переписывается.

## 2026-10-08 — CI: make check на Linux и macOS ([#2](https://github.com/naghuale/coord/issues/2))

- (не проверено) Добавлен `.github/workflows/ci.yml`: на push и pull request выполняется
  `make check` на `ubuntu-latest` и `macos-latest` (`fail-fast: false`), Go
  берётся из `go.mod`, права workflow — только чтение содержимого.
- (документация) Проверка изоляции P0-5 (fail-closed на Linux) в этот workflow не входит и
  остаётся отдельной частью P0-5.

## 2026-10-08 — Нормативная запись решений OWNER (NORM-AS)

- (документация) Три решения OWNER от 2026-10-08 записаны как нормативная
  детализация.
- (документация) **R122** — смена адреса сессии агента. ADR-0001 CM-13 и TASK-MODEL TM-27 дополнены:
  поколение означает только ротацию ключа в той же привязке; смена адреса
  ротацией не маскируется и регистрируется как новая `AgentSession`; старая
  привязка отзывается отдельным событием и не авторизует команду, поэтому повтор
  команды через неё отклоняется до replay; история хранит адрес, ключ, время
  действия и причину отзыва. В
  `docs/plans/p0-2-task/AGENT-COMMAND-BOUNDARY.md` решения добавлены в шапку
  (§5.1, §5.2) и в §3.2.1 как детализация R121 шагов 1–2: transport проверяет
  подпись и формирует `AuthenticatedFrame`, затем идёт поиск `AgentSession` по
  зарегистрированной привязке, проверка её активности, сверка адреса, отпечатка
  ключа и поколения, и только после этого — trusted `AgentID`, Run и `Task.Fence`,
  роль и Stage, replay, а для новой команды `Revision` и переход, и один durable
  commit. ADR-0001 CM-15 и TASK-MODEL TM-29 дополнены ссылкой на детализацию;
  аннотации «Проверка: не реализовано» сохранены, код не менялся.
- (документация) **R123** — старое состояние без `AgentSession`. Новая recovery-процедура не
  вводится: активный command-capable Run без действующей привязки блокирует
  readiness fail-closed с понятной диагностической причиной; файл автоматически не
  переписывается; `AgentID`, generation, ключ и адрес задним числом не
  заполняются. До подключения канонического agent command path такие состояния
  штатным путём не создаются. Owner recovery command (stop proof, завершение
  старого Run, новое назначение из свежей `AgentSession`) — запланированное
  продолжение P0-3, не реализуется до отдельного проекта и решения. Записано в
  CM-13, TM-27 и в §5.1 проекта границы.
- (документация) **R124** — переключение Executor RESULT, уточняющее R117. Legacy RESULT закрыт
  для всех Run с непустым `TaskID`; для Task-scoped Run канонический command path —
  единственный, fallback в legacy при ошибке запрещён. Run без `TaskID` пока
  используют legacy-путь как временную совместимость, что не является вторым
  production write path для Task. Полное удаление legacy RESULT требует отдельного
  решения после появления работающей замены. Записано в §5.5 проекта границы.
- (документация) Статус Accepted у ADR-0001 не менялся; новых правил вне решений
  OWNER не введено.
- (документация) OWNER подтвердил номера R122–R124, поэтому все пометки
  «номер подтверждается OWNER» и «номера предварительные» удалены из ADR-0001
  (CM-13), TASK-MODEL (TM-27), проекта границы команд агента и этого раздела.
  Записанный текст сверен с формулировками OWNER: R122 — смена адреса означает
  отзыв прежней `AgentSession` и регистрацию новой привязки, поколение меняется
  только при ротации ключа в пределах той же привязки; R123 — старый canonical
  state с активным command-capable Run без действующей `AgentSession` блокирует
  readiness fail-closed, автоматического backfill и новой recovery-процедуры нет;
  R124 — при подключении канонического Executor RESULT legacy-путь закрывается
  для всех Run с непустым `TaskID`, для Run без `TaskID` временно сохраняется как
  совместимость, а не второй production write path для Task, а полное удаление
  требует отдельного решения после появления работающей замены.
  Новых правил не добавлено; уже записанные детали (история привязки,
  отклонение до replay, диагностическая причина not-ready) сохранены.

## 2026-10-08 — Документация: устранение дрейфа (DOC-DRIFT-1)

- (документация) ADR-0005, контекст: Run-only `store.Persistence` и
  `checkpoint.File` описаны как сохранённый слой с путями в коде
  (`store/store.go:18-21`, `:106-133`), добавлен канонический слой —
  `store.CanonicalPersistence` (`store/canonical.go:87-94`) и
  `checkpoint.CanonicalFile` (`checkpoint/canonical.go:25-63`, `:65-74`), который
  открывает `coordd` (`cmd/coordd/main.go:107`). В решении уточнено, что
  Run-only persistence остаётся недостаточным контрактом, а требуемый контракт
  реализован отдельно. Нормы и статус Accepted не менялись.
- (документация) ADR-0007, MVP: «ни одно из требований не реализовано» заменено на
  фактическое состояние на дату — TaskID adoption и канонический Task aggregate
  реализованы (`TestCreateTaskGeneratesCanonicalTaskID`,
  `TestCanonicalStateValidatesTaskEventAndRunRelations`), приёмка standalone daemon
  не закрыта. Причина, по которой MVP Brief остаётся контрактом, теперь указана
  верно: отсутствует read interface, ADR-0006 не написан. Статус Proposed и
  зрелость не менялись.
- (документация) UX_AUDIT помечен как исторический снимок с датой 2026-10-07 и
  исходным commit `480df45`; отмечено, что этот commit не разрешается в текущей
  истории репозитория и приведённые file:line относятся к тому срезу. Содержимое
  аудита не переписывалось.
- (документация) ROADMAP: удалён дубль строки P0-3, осталась строка, содержащая
  ссылку на решение OWNER R100. Статусы этапов не менялись, P0-1 остаётся OPEN.
- (документация) TARGET-ARCHITECTURE: строка про `cmd/coordd` больше не помечена
  Run-only — daemon открывает канонический store и валидирует его до готовности,
  но write path ещё не подключён; строка ReviewResult/AuditResult переведена из
  «нет» в «частично» с точным описанием RA-1 (`a32acc6`) и RA-2 (`2d0cf60`):
  типизированные результаты и приёмка одним commit, startup validation,
  координаторские finding ID, refs только TASK/RUN/DOMAIN_EVENT и независимость
  Reviewer/Auditor по стабильному AgentID
  (`TestReviewResultCommitsWithTransitionEvent`,
  `TestAuditResultCommitsWithTransitionEvent`,
  `TestReviewAuditResultStartupValidationRejectsInvalidAssociations`,
  `TestReviewerRejectsExecutorAgentIDAcrossHistory`). Явно перечислено не
  реализованное: command path, supervisor stop proof, stage scheduler, Discussion
  Graph, Artifact metadata, read-only API.
- (документация) ADR-0007, «Назначение»: утверждение «Coord уже отдаёт
  производные представления: `RunViewStatus` считается на лету» разделено на три
  части. `RunViewStatus` в коде отсутствует — идентификатора нет ни в одном Go-файле,
  он остаётся только нормой TASK-MODEL TM-14 с аннотацией «не реализовано».
  Фактически вычисляемыми остаются проекция сессии в legacy rooms
  (`internal/rooms/sessions.go:265-274`, `:315-335`;
  `TestSessionRecoveryFencesAndScopesContext`,
  `TestAvailabilitySeparatesStaleGeneration`), причина not-ready в daemon
  (`daemon/daemon.go:107-115`; `TestDaemonReadinessLifecycle`) и фильтрация
  stalled-результатов в control endpoint `/runs` (`control.go:27-36`) — ни одно из
  них не является `RunViewStatus`. Read-only API для Task отсутствует, ADR-0006 не
  написан; при этом внешний control endpoint daemon (`control.go:20-36`, bearer
  token, `GET /runs`) существует, но отдаёт только stalled-результаты для
  supervisor recovery и проекцией Task не является. Legacy rooms не является
  опорой для Brief: по ADR-0001
  «Миграция от `internal/rooms`» (`docs/ADR-0001-CANONICAL-COORDINATION-MODEL.md:504-529`)
  rooms объявлены legacy, их данные не становятся Task facts автоматически, а
  перенос используемых операций должен сохранить capability scope и generation
  checks. Статус Proposed, нормы Brief и зрелость не менялись.

## 2026-10-08 — RA-2: независимость Reviewer и Auditor

- Canonical Task startup и переходы проверяют stable `AgentID` Reviewer/Auditor
  против истории Executor assignments по EventSeq. Reviewer следует TASK-MODEL
  TM-23; Auditor сравнивается с Executor согласно ADR-0001 CM-03, без нового
  запрета Auditor против Reviewer. Ошибки `NO_INDEPENDENT_REVIEWER` и
  `NO_INDEPENDENT_AUDITOR` типизированы, rejected transition не сохраняется
  (`TestReviewerRejectsExecutorAgentIDAcrossHistory`,
  `TestAuditorRejectsExecutorAgentIDAcrossTaskRuns`,
  `TestCanonicalTransitionRejectsRoleIdentityConflict`,
  `TestSameAgentMayHoldRolesInDifferentTasks`,
  `TestExecutorWithoutStableSelectionFailsClosedBeforeReviewer`).
- Durable blocker reason, Attention projection и stage-scheduler переход не
  реализованы; требуют решения OWNER. При отсутствии DomainEvent, точно
  связывающего RoleHistory selection и событие, extra Executor selection
  получает самый ранний возможный EventSeq, extra Reviewer/Auditor — самый
  поздний. Ограничения и
  тесты fail-closed: `TestNoIndependentReviewerOrAuditorFailsClosed` и
  `TestCanonicalRoleIndependenceFailsClosedWithoutStableAgentID`.
  Отсутствие post-transfer anchors проверяет
  `TestRoleHistoryExtraSelectionRequiresEventAfterTransfer`; отсутствие transfer
  marker проверяют `TestCanonicalStartupRejectsTaskScopedRunWithoutTransferMarker`
  и `validateTaskOwnershipTransfers`. Подробности —
  [RA-2](docs/plans/p0-review-audit/RA-2.md).

## 2026-10-08 — RA-1: Review/Audit result core

- Добавлены типизированные `ReviewResult` и `AuditResult` в DomainEvent schema v1
  с закрытыми verdict sets, Coord-generated Finding IDs и валидацией при старте.
  Принятие результата атомарно фиксирует Task в `BLOCKED` на текущем Stage,
  terminal role Run, DomainEvent, Inbox receipt и `RESULT_ACK` Outbox. Ожидание
  stop proof проецируется из отсутствующего proof; сам supervisor path вне среза
  (`TestReviewResultCommitsWithTransitionEvent`,
  `TestAuditResultCommitsWithTransitionEvent`,
  `TestReviewVerdictWaitsForSupervisorStopProof`,
  `TestAuditVerdictWaitsForSupervisorStopProof`).
- Evidence refs на Task, Run той же Task и более ранние DomainEvent проверяются
  fail-closed. Входные и выходные Artifact refs принимаются только пустыми:
  канонические Artifact metadata и правила владения требуют отдельного решения OWNER
  (`TestReviewResultRejectsInvalidAndOutOfScopeReferences`,
  `TestResultArtifactReferenceFieldsRemainEmptyUntilMetadataExists`).
- Методы внутренней записи RA-1 пока не принимают входные Task Fence, Attempt и
  sender session, поэтому сами не ограждают поздний результат прежнего владельца
  после recovery-передачи того же RunID. До production exposure это обязан
  обеспечивать daemon command boundary по [Task-wide Fence и ownership](docs/TASK-MODEL.md#task-wide-fence-и-ownership),
  с проверкой текущего Task.Fence до записи (TASK-MODEL.md:216–218). (не проверено)
- Проверки: `TestReviewResultIsStructuredAndTaskScoped`,
  `TestAuditResultIsStructuredAndTaskScoped`,
  `TestReviewResultCommitsWithTransitionEvent`,
  `TestAuditResultCommitsWithTransitionEvent`,
  `TestReviewVerdictWaitsForSupervisorStopProof`,
  `TestAuditVerdictWaitsForSupervisorStopProof`,
  `TestReviewAndAuditVerdictSetsAreClosed`,
  `TestFindingSchemaAndSeverityDoNotDriveTransitions`,
  `TestReviewAuditResultStartupValidationRejectsInvalidAssociations`,
  `TestReviewResultRejectsInvalidAndOutOfScopeReferences`,
  `TestResultArtifactReferenceFieldsRemainEmptyUntilMetadataExists`,
  `TestReviewAuditResultRejectsNonWorkingRoleRunsWithoutMutation`,
  `TestReviewAndAuditRecordRejectWrongRoleRun`,
  `TestReviewResultRejectsTimestampBeforeTaskUpdate`,
  `TestReviewAuditResultStartupValidationRejectsInvalidTerminalProjection`,
  `TestReviewAuditResultStartupValidationRejectsEventMetadataMismatch`,
  `TestReviewAuditResultsRequireFindingsAndEvidence`,
  `TestRecordReviewResultRejectsNilArtifactReferenceLists` and
  `TestDomainEventRejectsReviewOrAuditPayloadForUnrelatedEventType`.

## 2026-10-08 — P0-2, срез 22c: атомарность, восстановление и проверки canonical state

- Добавлены acceptance-тесты, подтверждающие один durable commit для Task, Run,
  DomainEvent, Outbox и Inbox receipt; проверяются сохранённый snapshot и
  восстановление Task-scoped состояния с replay Outbox после перезапуска.
  Неуспешная первая доставка сохраняет EventID; успешный transport replay не
  добавляет DomainEvent, не меняет Task Revision и не создаёт Inbox receipt
  (`TestCanonicalTransitionCommitsStateEventOutboxAtomically`,
  `TestCanonicalCommitFailureDoesNotPublishOrContinue`,
  `TestCoorddRestartRestoresCanonicalTaskStateAndOutbox`).
- coordd остаётся not-ready при структурно невалидном, но декодируемом
  canonical snapshot; transport не запускается, исходный файл не меняется
  (`TestCoorddReadinessRejectsInvalidCanonicalState`).
- Startup validation отвергает Inbox receipt, ссылающийся на события вне
  `CommittedRevision`, и Task-scoped Run, чья текущая RoleSelection не совпадает
  с последней записью RoleHistory
  (`TestCanonicalStartupRejectsInboxEventOutsideCommittedRevision`,
  `TestCanonicalStartupRejectsRoleSelectionNotMatchingLatestHistory`).
- Canonical uncertain-commit regression подтверждает отказ старого handle,
  повторную проверку snapshot при reopen и успешную следующую mutation только
  через новый store; process lock helper удерживает владельца flock до конца
  subprocess (`TestCanonicalCommitFailureRequiresReopenAndRevalidatesDurableState`,
  `TestStoreSecondProcessCannotOpenWhileStoreOwnerRunning`).
- По решению OWNER R100 проверки входного Task Fence, передачи между Runs,
  Assign/Tick/recovery с несколькими writers и Fence в Outbox envelope
  перенесены из критериев P0-2 в P0-3. Полнота Profile и реестр EventType
  остаются открытыми вопросами контракта, а не условиями завершения P0-2
  (документация).

## 2026-10-08 — P0-2, R75b: снимок Fence в истории событий и проверка при загрузке

- DomainEvent schema v1 теперь сохраняет `task_fence`. Startup валидирует единственный
  `TASK_CREATED` на revision 1 с Fence 0, непрерывное изменение Fence по transfer
  marker, итоговый `Task.Fence`, соответствие каждого Task-scoped Run последнему
  marker своего RunID и принадлежность `Event.RunID` той же Task (`TestCanonicalStartupRejectsWrongFenceInEarlyRevision`, `TestCanonicalStartupRejectsWrongFenceInLatestRevision`, `TestCanonicalStartupAcceptsFenceSnapshotsAcrossTransferAndRecovery`).
- Creation-only snapshot schema v1 без `task_fence` читается, проходит обычную
  запись и повторную загрузку с тем же номером схемы; новые JSON записи содержат
  ключ `task_fence`. История с передачей владения без снимка Fence отклоняется
  без изменения файла; legacy backfill не выполняется (`TestCanonicalFileReadsAndRewritesCreationOnlyV1WithoutFenceField`, `TestCanonicalFileRejectsTransferredV1WithoutFenceAndPreservesSource`).
- Добавлены startup-регрессии для ошибочного Fence в истории, пропущенного и
  лишнего bump, нескольких transfer marker на revision, overflow, повторного или
  позднего `TASK_CREATED`, несоответствия итогового Fence и чужого RunID.
  Проверки revision 1, обязательного RunID transfer marker и полного пути recovery
  добавлены отдельно. Это покрыто тестами `TestCanonicalStartupRejectsMissingFenceBumpOnTransfer`, `TestCanonicalStartupRejectsExtraFenceBumpWithoutTransfer`, `TestCanonicalStartupRejectsTwoTransferMarkersInOneRevision`, `TestCanonicalStartupRejectsSecondEventInCreationRevision`, `TestCanonicalStartupRejectsTransferMarkerWithoutRunID`, `TestCanonicalStartupRejectsFenceOverflow`, `TestCanonicalStartupRejectsFinalFenceDifferentFromTask`, `TestCanonicalStartupRejectsSecondTaskCreated`, `TestCanonicalStartupRejectsLateTaskCreated`, `TestCanonicalStartupRejectsEventRunIDOwnedByAnotherTask`, `TestCanonicalStartupRejectsRunFenceDifferentFromLatestTransfer`, `TestCanonicalStartupRejectsTaskScopedRunWithoutTransferMarker` и `TestCanonicalStartupAcceptsFenceSnapshotsAcrossTransferAndRecovery`. Production command path и supervisor stop proof остаются за пределами R75b.

## 2026-10-08 — P0-2, R75a: инварианты передачи владения в хранилище

- Переходы canonical Task требуют увеличивать `Task.Fence` ровно на единицу при
  каждой передаче владения, сверять его со снимком Fence активного Run и
  фиксировать DomainEvent `TASK_OWNERSHIP_TRANSFERRED`. Recovery сохраняет
  RunID/Attempt и требует stalled recovery claim; для нового Run прежний writer
  должен быть terminal уже в предыдущем snapshot. Это проверяет структуру
  storage-перехода; `Run.Fence` существующего Task-scoped Run меняется только при
  recovery transfer того же Run. Подтверждение остановки supervisor и
  авторизация production command path пока не реализованы
  (`TestCanonicalTransitionRecoveryPreservesAttemptAndIncrementsTaskFence`,
  `TestCanonicalTransitionRejectsSecondActiveWriter`,
  `TestCanonicalTransitionRejectsTerminalRunFenceChangeWithoutRecovery`,
  `TestCanonicalTransitionRejectsActiveRunFenceChangeWithoutRecovery`,
  `TestCanonicalTransitionRejectsRunFenceChangeWithoutRecovery`).
- Добавлены регрессионные проверки отсутствующего и лишнего изменения Fence,
  transfer event, recovery claim, нескольких writers, правил Attempt и
  переполнения Fence
  (`TestCanonicalTransitionRejectsOwnerChangeWithoutTaskFenceBump`,
  `TestCanonicalTransitionRejectsFenceBumpWithoutOwnershipTransfer`,
  `TestCanonicalTransitionRejectsFenceJump`,
  `TestCanonicalTransitionRequiresOwnershipTransferEvent`,
  `TestCanonicalTransitionRequiresRunFenceToMatchTaskFence`,
  `TestCanonicalTransitionRejectsRecoveryWithoutStalledRecoveryClaim`,
  `TestCanonicalTransitionRejectsFenceOverflow`,
  `TestCanonicalTransitionRoleHandoffPreservesWorkflowAttempt`,
  `TestCanonicalTransitionReworkIncrementsAttemptAndFence`,
  `TestCanonicalTransitionRetryPreservesAttemptAndTaskFence`).
- Ужесточена граница переходов: идентичность роли Task-scoped Run нельзя
  переписать, выбранный профиль должен соответствовать требуемой роли,
  terminal Run не может сменить владельца или снова стать активным, а recovery
  обязан очистить состояние claim. Новая `RoleSelection` должна без изменений
  добавляться в конец `RoleHistory`; снятие выбора или смена его идентичности без
  записи в историю отклоняются. Commit передачи содержит ровно один transfer
  marker для нового writer. Существующие записи `RoleHistory` добавляются только
  в конец; их `agent_id`, роль и порядок сохраняются как свидетельство для
  проверки независимости AUDITOR. Политика выбора ролей не меняется
  (`TestCanonicalStateRejectsMismatchedSelectedRole`,
  `TestCanonicalStateRejectsSelectedRoleSessionMismatch`,
  `TestCanonicalTransitionRejectsTaskRunRoleMutation`,
  `TestCanonicalTransitionRejectsTerminalRunReactivation`,
  `TestCanonicalTransitionRejectsOwnerChangeWhenRunBecomesTerminal`,
  `TestCanonicalTransitionRejectsRecoveryWithUnclearedState`,
  `TestCanonicalTransitionRejectsTransferMarkerWithoutTransfer`,
  `TestCanonicalTransitionRejectsTransferMarkerForWrongRun`,
  `TestCanonicalTransitionRejectsExtraTransferMarker`,
  `TestCanonicalTransitionRoleHistoryIsAppendOnly`,
  `TestCanonicalTransitionRequiresRoleSelectionHistory`).

## 2026-10-08 — P0-2: fail-closed для неисполняемого StagePlan

- Сохранили полную грамматику StagePlan, но `taskservice.Create` теперь до
  генерации TaskID отвергает этапы, которые текущий runtime ещё не исполняет;
  отказ не меняет Task, Revision или DomainEvent; обязательное Owner decision
  также отклоняется (`TestTaskCreateRejectsUnsupportedStageWithoutCommit`,
  `TestTaskCreateRejectsRequiredOwnerDecisionWithoutCommit`).

## 2026-10-08 — P0-2: ревизии событий одной транзакции

- Переход Task теперь требует, чтобы все новые Domain Events этой транзакции
  ссылались на новую Revision, а не только последнее событие
  (`TestCanonicalTransitionRejectsSameCommitEventAtPreviousRevision`).
- Ссылка на решение OWNER R61 перенесена в критерий compatibility lock ADR-0005.
  P0-1 writer-path inventory дополнен auxiliary localchat state и legacy Rooms
  history export. (документация)

## 2026-10-08 — P0-2, срез 22b: canonical schema v1 и Inbox receipts

- Переход отвергает изменение Task без увеличения Revision даже при добавлении
  корректного события с прежней Revision; регрессия проверена мутацией
  (`TestCanonicalTransitionRejectsTaskMutationWithoutRevisionIncrement`).
- Canonical validation отвергает Outbox-сообщение, связанное с DomainEvent
  другого Run (`TestCanonicalStateRejectsOutboxReferenceToDifferentRunEvent`).
- Начальная canonical schema v1 сохраняет typed Inbox receipts с ключом из стабильного
  actor ID и message ID, ссылкой на Task и DomainEvent. `ApplyInboxCommand`
  фиксирует receipt с изменением состояния, событием и Outbox; повтор возвращает
  сохранённый receipt и не вызывает handler повторно
  (`TestApplyInboxCommandCommitsReceiptWithTaskEventAndDeduplicates`,
  `TestApplyInboxCommandRequiresSameCommitEventAndPreservesStateOnFailure`,
  `TestCanonicalStateRejectsDanglingInboxEventReference`).
- Неизвестная версия canonical snapshot отвергается без изменения исходного
  файла (`TestCanonicalFileRejectsUnknownSchemaWithoutModifyingFile`). Будущий
  переход схемы требует отдельного решения и backup-процедуры по ST-05.

## 2026-10-08 — P0-2, срез 22b: durable Outbox

- Единственная durable Outbox очередь теперь
  находится в верхнем уровне canonical state; Run API гидратирует прежние поля
  для coordinator, не сохраняя вторую дисковую копию. Envelope order, message ID,
  retry timestamp и retry count сохраняются
  (`TestCanonicalRunStorePersistsSingleTopLevelOutbox`).
- Старый `runs.json` не импортируется.
- Task-scoped Outbox record ссылается на DomainEvent той же durable transaction;
  Run-only legacy envelopes сохраняют идентичность без выдумывания Task/Event
  (`TestCanonicalTaskOutboxRequiresEventFromSameCommit`,
  `TestCanonicalOutboxExistingMessageIdentityAndOrderAreImmutable`).

## 2026-10-08 — P0-2, срез 22b: Task aggregate foundation

- Добавлены typed Task, StagePlan, Task status и DomainEvent; проверяются TaskID,
  версии, Stage/Status и последовательность EventSeq
  (`TestTaskValidationSeparatesStageAndStatus`,
  `TestTaskValidationRejectsInvalidPlanAndTerminalStage`,
  `TestCanonicalStateValidatesTaskEventAndRunRelations`).
- Внутренний taskservice создаёт `task_<ULID>` и `TASK_CREATED` одной транзакцией
  canonical snapshot; столкновение с уже сохранённым ID повторяет генерацию
  (`TestCreateTaskGeneratesCanonicalTaskID`,
  `TestTaskIDCollisionRetriesBeforeCommit`). Public create API не добавлен (R52).
- `fsm.Run.TaskID` отделён от прежнего `Run.Task`. Startup validation требует
  существующую Task, историю события Run, Task-wide active-writer uniqueness и
  согласованный Fence и целостную persisted Run FSM (`TestCanonicalStateRejectsMultipleActiveRunsPerTask`,
  `TestRunValidateRejectsPersistedFSMInvariantViolations`,
  `TestCanonicalStateRejectsInvalidRunFSMState`,
  `TestCanonicalTransitionRequiresRevisionEventAndMonotonicFence`,
  `TestCanonicalTransitionRequiresEventForTaskRunMutation`).
- `CanonicalStore.UpdateCanonical` валидирует весь candidate state и transition
  до одного durable Save; Task events append-only, Revision и Fence не убывают.
  Технические transport updates не создают DomainEvent, а доменное изменение
  Run требует события на новой Task Revision
  (`TestCanonicalTransitionRequiresRevisionEventAndMonotonicFence`,
  `TestCanonicalCommitFailureDoesNotPublishOrContinue`,
  `TestCanonicalTransitionRequiresTaskRevisionAndEventForRunDomainChange`,
  `TestCanonicalTransitionAllowsTransportOnlyRunUpdateWithoutDomainEvent`).
  Полная P0-2 command transaction для Task/Run и Inbox receipts, а также
  production Task workflow остаются в работе.

## 2026-10-08 — P0-2, срез 22a: каноническая граница Run storage

- `coordd` хранит текущие Runs в versioned `canonical.json` через отдельную
  `CanonicalState` persistence boundary; commit публикует память только после
  durable Save, а ошибка Save запрещает следующие записи до reopen. Срез
  сохраняет текущие Run и вложенный Outbox без импорта legacy данных
  (`TestCanonicalCommitFailureDoesNotPublishOrContinue`,
  `TestCoorddProcessRestartRestoresAndReplaysOutboxBeforeReady`).
- До открытия canonical store daemon удерживает compatibility lock старого
  `runs.json`. Активный или невалидный legacy snapshot, неизвестная canonical
  schema и занятый legacy lock оставляют процесс в живом diagnostic-only
  `NOT_READY`; transport, Tick и replay не запускаются. Завершённые legacy Runs
  остаются неизменными и не импортируются
  (`TestCoorddActiveLegacyRunKeepsNotReadyWithoutStartingTransport`,
  `TestCoorddTerminalLegacyRunIsNotImported`,
  `TestCoorddLegacyWriterLockKeepsDaemonNotReady`,
  `TestCoorddUnknownCanonicalSchemaKeepsNotReady`).
- P0-1 process fixtures переведены на canonical persistence boundary. Сценарии
  проверяют retry/replay после рестарта, активный и terminal legacy state,
  занятый compatibility lock с обеих сторон, неизвестную schema и отказ
  durable commit (`TestCoorddProcessRestartRestoresAndReplaysOutboxBeforeReady`,
  `TestCoorddActiveLegacyRunKeepsNotReadyWithoutStartingTransport`,
  `TestCoorddTerminalLegacyRunIsNotImported`,
  `TestCoorddLegacyWriterLockKeepsDaemonNotReady`,
  `TestCoorddSecondProcessCannotOpenOwnedNamespace`,
  `TestCoorddUnknownCanonicalSchemaKeepsNotReady`,
  `TestCanonicalCommitFailureDoesNotPublishOrContinue`).
- Проверяемые критерии среза 22a в `docs/plans/p0-2-task/DESIGN.md` теперь
  ссылаются на реализованные сценарии; Task aggregate, Event Store и
  Task-wide Fence остаются последующими срезами. (документация)

## 2026-10-08 — production Run daemon (P0-1)

- Добавлен `coordd` с единственным `-config`, строгим JSON schema v1,
  обязательным Telepathy transport и exit codes 0/1/2; некорректная config
  отклоняется до state side effects (`TestCoorddInvalidConfigExitsTwoBeforeSideEffects`,
  `TestLoadRequiresKnownSchemaVersion`, `TestLoadValidatesCompleteTelepathyConfiguration`).
- `coordd` берёт namespace lock раньше Store/RoleRegistry; transport startup
  выполняется после lock, а отказ освобождает всё открытое. Process-level сценарии
  запускают реальную coordd composition в дочерних OS-процессах и проверяют
  блокировку второго writer, release/restart после kill, восстановление Run,
  replay outbox до readiness, сохранение committed result и штатный SIGTERM
  shutdown
  (`TestCoorddRefusesStartWithoutTransportBeforeLock`,
  `TestCoorddTakesNamespaceLockBeforeStores`,
  `TestCoorddTransportStartupFailureReleasesOwnership`,
  `TestCoorddProcessKillReleasesNamespaceAndRestarts`,
  `TestCoorddSecondProcessCannotOpenOwnedNamespace`,
  `TestCoorddProcessRestartRestoresAndReplaysOutboxBeforeReady`,
  `TestCoorddProcessRestartPreservesResultAndReceipt`,
  `TestCoorddProcessGracefulShutdownReleasesNamespace`).
- Общий Telepathy factory теперь используется daemon и standalone host; readiness
  остаётся закрытой при supervisor recovery, а shutdown закрывает handlers/loop,
  transport и durable ownership в этом порядке
  (`TestWorkerDuplicateExecutesOnceAndRequiresReceipt`,
  `TestDaemonReadinessKeepsMutationsClosedDuringRecovery`,
  `TestDaemonGracefulShutdownReleasesNamespaceAfterHandlers`).
- В ROADMAP добавлена модель интеграции агентов после P0-3: используется
  существующий envelope и структурированные Review/Audit results. (документация)
- Уточнены критерии P0-3/4/5 для проверки worktree в протокольных путях,
  per-Task worktree/writer lock и обязательной fail-closed OS isolation
  executor и требуемых доказательств из Linux CI. (документация)

## 2026-10-08 — fail-closed snapshot ownership

- Snapshot ownership uses the shared `filelock` implementation. On platforms
  without a supported process lock, opening the store is rejected before state
  is loaded; the system no longer proceeds without writer ownership
  (`TestRefusedOwnershipFailsBeforeLoadingState`,
  `TestLockOwnsSnapshotOrRefusesWithoutRelease`).
- `make check` runs cross-platform `go vet` for Windows; Solaris and AIX builds
  are neither checked nor guaranteed. (не проверено)
- `telepathy.Process.Close` no longer reports a false `context canceled` when an
  MCP server exits cleanly after stdin EOF; a parent-context deadline still
  returns an error (`telepathy.TestCloseAfterCleanExitIsNotAnError`; this race
  previously made `TestRPCChecksVersionAndToolFailure` fail intermittently).
- Run deadline handling (`failDeadline`) clears `ResumeStatus` with recovery
  state, so a terminal Run has no continuation point
  (`TestRunInvariantsHoldThroughLifecycle`).
- Выход run из `STALLED` очищает `ResumeStatus` в единственной точке перехода
  (`fsm.Run.Apply`), независимо от того, терминальный это переход или нет:
  раньше поздний heartbeat возвращал run в работу с сохранённой точкой
  продолжения, а RESULT после пропущенного probe давал `DONE` с
  `ResumeStatus` (`TestStalledExitClearsResumeStatus`).
- Ошибки разбора JSON в localchat HTTP (включая unknown field, хвост и
  превышение лимита тела) возвращаются как `400` с `code=invalid_input`, как
  и другие некорректные входы; сценарии проверяют неизменность rooms snapshot
  (`TestMalformedPOSTBodiesReturn400InvalidInputWithoutChangingRoomsState`,
  `TestSessionBlockInvalidFieldsReturn400WithoutChangingRoomsState`).
- По решению owner от 2026-10-08 автоматическая проверка содержимого checkpoint
  исключена из UX-1; перед `unblock` владелец проверяет checkpoint вручную,
  программная проверка остаётся в P1 ROADMAP. (документация)

## 2026-10-07 — coordinator daemon host runtime

- Добавлен библиотечный `daemon` runtime для запуска coordinator loop,
  control/metrics listeners и `/readyz`; обычная readiness учитывает pending
  supervisor recovery, а recovery control остаётся доступен для его завершения.
- Runtime выполняет startup tick до readiness, затем запускает transport receive;
  readiness остаётся закрытой при незавершённом supervisor recovery, а recovery
  endpoints доступны после инициализации receive. Shutdown использует единый
  timeout, закрывает HTTP handlers и освобождает Store/RoleRegistry; listeners
  ограничены loopback. Ошибки shutdown и закрытия durable stores возвращаются
  вызывающему коду, а не теряются в defer.
- Это host runtime текущего Run coordinator, а не завершённая canonical Task
  daemon или отдельный executable.

## 2026-10-07 — срез B UX-1: карточка состояния, блокер, `coord tui`

- `GET /availability` различает attachment, transport reachability, readiness,
  processing cursor, provider availability, blocker, ожидание и поколение.
  `stale_generation` виден отдельной строкой; provider без внешнего сигнала
  остаётся `unknown` — Coord не делает выводов о чужой модели.
- Блокер (`blocked_provider_quota` и другие известные причины) несёт
  reason/source/observed_at/retry_at, останавливает новое назначение роли на
  любом пути assign(), не трогает checkpoint и журнал, снимается только
  явным `coord session unblock` / командой TUI.
- Новая команда `coord tui`: кадр owner с раздельными полями и slash-командами
  (`/help /models /connect /sessions /rooms /join /leave /status /block
  /unblock`), те же эндпоинты, что и CLI; сетевой listener не открывается.
- Метод команды определяется одной таблицей для handler и client: рассинхрон
  двух списков раньше отдавал 405 на `/availability` и ломал запуск TUI.
- HTTP различает плохой вход (400, `code=invalid_input`) и конфликт состояния
  (409); ошибки указывают следующую допустимую команду. Некорректные reason,
  source и retry_at тоже относятся к 400, а не к конфликту.
- TUI: доступ к разделяемому состоянию идёт через мьютекс (`/join`, `/leave`,
  `/help`, `/status`, `/block`, `/unblock` больше не пишут поля без него);
  курсор `/watch` переносится в переподключение, поэтому обрыв потока не
  возвращает уже показанные строки; карточки перечитываются по тику, и внешний
  blocker виден без ввода команды.
- TUI: событие попадает в очередь кадра до продвижения курсора, поэтому
  переполнение очереди не теряет запись навсегда; горутина ввода, ожидающая
  отправки строки, освобождается при `/quit`; ожидание чтения stdin завершается
  вместе с процессом (`internal/localchat.TestQuitWithTrailingInputLeavesNoGoroutine`).
- TUI: подписка `/watch` живёт на одну комнату, поэтому `/join` другой комнаты
  и `/leave` рвут активный запрос; раньше поток прежней комнаты оставался
  открытым и события новой не приходили. Курсор комнаты двигает только цикл
  отрисовки — после того как событие реально показано, — поэтому события,
  доставленные в переполненную очередь покинутой комнаты, не теряются и не
  дублируются при возврате.
- `.gitignore` восстановлен: `/state/`, `/bin/`, credentials и логи снова
  не попадают в Git.
- Дедлайн делает run терминальным и очищает всю recovery-метаданную, включая
  `RecoveryClaim`: снимок больше не показывает активную блокировку у мёртвого
  run.
- Срез B одобрен owner. Срезы A (per-context профили) и C (discover/onboarding)
  не реализованы и требуют отдельного approval owner.
- Добавлены owner-команды `coord session block/unblock`; `block` принимает
  обязательный `--reason` и необязательный RFC3339 `--retry-at`. Повтор
  идентичного блокера теперь no-op без нового события/сдвига `observed_at`;
  просроченный `retry_at` отвергается как invalid input до поиска сессии.
- Успешные CLI-команды печатают машинно-читаемый результат; аргументы
  проверяются до чтения owner capability. Контракт проверяют
  `cmd/coord.TestSessionBlockAndUnblockUseOwnerCapability`,
  `cmd/coord.TestSessionBlockRejectsInvalidArgsBeforeRequest` и
  `cmd/coord.TestSessionUnblockReferencesRemainExecutable`.
- Перед `unblock` владелец должен вручную проверить checkpoint; CLI не
  подтверждает и не валидирует эту проверку.

## 2026-10-07 — согласован первый UX-сценарий (документация, без кода)

- docs/plans/ux-first-scenario/ALIGNED_UX.md: согласованное целевое поведение
  owner-карточек (раздельные attachment/reachability/readiness/cursor/activity/
  availability), per-context профиля с привязкой к generation и запрет
  auto-select newest credential, машинный блокер blocked_provider_quota с
  reason/source/observed_at/retry_at, discover с availability=unknown.
- docs/plans/ux-first-scenario/UX_TASK.md: задача UX-1 на реализацию
  (PENDING APPROVAL); код не менялся, команды профиля ещё не существуют.
- docs/plans/ux-first-scenario/PLAN.md: сводный план UX-1 в формате
  «что важно → что нужно → как сделаем → что будет»; порядок срезов
  B → A → C, LongCat recovery вне приёмки до снятия подтверждённой квоты.
- README и ROADMAP ссылается на план; приёмка раздела «от начала до
  восстановления» планируется с Codex Auditor и Nemotron; LongCat исключён
  до снятия подтверждённой provider quota.

## 2026-10-07 — устойчивые локальные ролевые сессии

- Общий coord CLI, owner-чат и независимый от TUI Unix API.
- Предварительное назначение/приглашение и WAITING_ASSIGNMENT.
- Устойчивые session_id, agent_id, context bindings, generation CAS и ротация capability.
- Checkpoint, explicit ack, идемпотентные операции и unknown внешние действия.
- Версионирование регламента координатором и подтверждение readiness.
- Раздельный owner API и scoped агентский контекст; доверие в пределах пользователя ОС.
- Локальная Git-проекция с export policy, backlog и recovery commit/cursor.
- Стандартные промпты четырёх ролей: границы полномочий, Task Contract, verdict и независимость.
- Общий coord prompt; сценарные/CLI проверки и opt-in тест отдельного реального AI.
- Crewflow не интегрируется; автоматического push runtime history нет.


## 2026-10-07 — standalone Telepathy host/bridge

- Добавлены пакет telepathy и cmd/coord-telepathy: реальный MCP stdio 0.8.0,
  pinned Ed25519 frames, host/worker с checkpoint, writer/session locks и receipt.
- Command worker отказывается повторять interrupted work без durable RESULT;
  готовый RESULT replay не запускает команду.
- Учтены Telepathy rate/duplicate limits и ограниченный archive replay.
- Добавлены make test-telepathy-host, инструкции настройки и отдельная
  спецификация Crewflow integration; CF-013A не изменяется.


## 2026-10-06 — агентские инструкции и расширенная отладка

- Добавлен RESULT_ACK: durable receipt в canonical outbox, terminal replay,
  executor.Receipt и идемпотентная повторная отправка RESULT. Обновлять обе стороны вместе.
- Deadline проверяется при получении событий; delayed heartbeat/progress regression
  не откатывают run, повтор ACK не откатывает STARTED.
- Executor проверяет полную identity до изменения checkpoint; duplicate TASK
  после собственного commit не требует исходного HEAD и не запускает работу заново.
- Store/Registry изолируют входные ссылки; registry имеет process lock и Close.
  После ErrPersistence дальнейшие записи прекращаются до close/reopen.
  Отвергаются revision overflow, неперечитываемые snapshots и неизвестные FSM states.
- Watchdog выполняет bounded cleanup при cancellation, обрабатывает все candidates
  после ошибки и удерживает claim при unknown/failed replacement cleanup.
- Atomic snapshot writer общий для checkpoint/registry/harness; ошибки close
  и cleanup обрабатываются. make check запускает установленный golangci-lint.
- Byte/signature adapters проверяют route/configuration, strict frame JSON; retry
  не выдаёт zero delay и не переполняет time.Duration.
- Добавлен tested Unix CLI/subprocess host examples/agent, инструкции обоих
  вариантов подключения, regression tests и fuzz targets.

## 2026-10-06 — durable executor replay и безопасный cleanup recovery

- ACK, heartbeat и RESULT сохраняются со состоянием до первого transport send;
  FIFO outbox сохраняет готовность перед результатом и не заменяет старые сообщения.
- RESULT_READY не откатывается при outage; успешный replay восстанавливает статус.
  Просроченные lease responses обновляются как новые durable protocol messages.
- Новый fence очищает сообщения, результат и blocker старого writer, сохраняя прогресс.
- Watchdog не освобождает claim при неподтверждённой остановке replacement.
- Store.Get копирует только запрошенный run вместо всего snapshot; изоляция вложенных данных сохраняется.
- Добавлены regression tests и сквозной coordinator/executor restart scenario.

## 2026-10-06 — уточнение границ экспериментов

- В EXPERIMENTS описаны проверяемые метрики и ограничения свидетельств после рестарта.
- Явно перечислены непроверенные сценарии с LLM, production bridge и транспортом.
  Runtime-код и lease-протокол не менялись.

## 2026-10-06 — fence diagnostics и live crash experiments

- Существующая точная проверка fence выделена перед проверкой owner/deduplication.
  Добавлены ErrStaleFence, stale/future rejection counters и защита от uint64 overflow.
- Unit tests подтверждают отклонение старого/будущего поколения, ABORT correlation,
  неизменность canonical run и сохранение owner/fence при failed recovery commit.
- Версионируемый test-only harness использует реальный Telepathy MCP 0.8.0.
  Проверены owner transfer после SIGKILL/flock release, coordinator restart,
  queued heartbeat во время outage и Python subprocess resume с SQLite idempotency.
- Добавлены `make test-telepathy`, инструкции воспроизведения и границы доказательств.
  Production bridge/watchdog/новые transports/lease enforcement не добавлялись.

## Начальная локальная версия — 2026-10-06

### Реализовано

- Go-модуль `coord` без внешних зависимостей.
- Типизированный wire protocol: TASK, ACK, HEARTBEAT, RESULT, PING/PONG,
  ABORT, ROLE_UPDATE/ROLE_ACK; строгая проверка payload и correlation.
- FSM, канонический owner, immutable run identity, retry и fencing generation.
- Атомарные snapshot, durable outbox, deduplication и Unix process lock.
- Checkpoint исполнителя, сохранение прогресса/результата и повторная доставка.
- Внешний watchdog: recovery claim, остановка writer, проверка HEAD,
  запуск replacement и CAS transfer.
- Четыре роли, требования к профилю/capabilities, атомарные ROLE_UPDATE,
  журнал событий и запрет аудита своей задачи по стабильному agent_id.
- Подпись Ed25519, Prometheus text endpoint и примеры alert rules.
- Проверки lifecycle, таймаутов, split-brain, восстановления, CAS и ролей.
- Демо, пример смены ролей, Makefile и документация эксплуатации/разработки.

### Границы версии

Реальный Telepathy bridge и команды управления агентскими процессами подключаются
отдельно. Приложение пока предоставляет ядро, демо и watchdog CLI; здесь нет
готового production daemon, распределённого consensus или аттестации модели.
Подробнее: [дорожная карта](docs/ROADMAP.md).
