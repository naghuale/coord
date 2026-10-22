# Работа с репозиторием coord

Это самостоятельный Go-модуль координации. После этого файла используйте
[`docs/DOCUMENT-MAP.md`](docs/DOCUMENT-MAP.md), чтобы выбрать минимальный набор
материалов для текущего события или роли. Не загружайте все документы проекта
«на всякий случай». Owning ADR и TASK-MODEL задают нормы; пути чтения и
обновления документов ведутся в `DOCUMENT-REGISTRY.yaml` и `ACTION-MAP.md`.

- Меняйте только файлы этого проекта в рамках задачи пользователя.
- Сохраняйте разделение coordinator, executor, transport и external supervisor.
- Не считайте transport delivery подтверждением принятия задачи или результата.
- Не меняйте attempt при повторной доставке. Recovery требует fencing старого writer.
- Сохраняйте независимость AUDITOR по стабильному agent_id и истории исполнителей.
- Проверяйте реальные transport API; не добавляйте вымышленные SDK-вызовы.
- Для изменения поведения добавляйте необходимые сценарные тесты с fake clock/transport.
- Обновляйте документацию и CHANGELOG, если меняются protocol, CLI или эксплуатация.
- Перед commit выполните полный `make check` через утверждённый project-local
  wrapper, `git diff --check`, проверьте staged diff и его соответствие принятому
  дереву.
- Не включайте state, credentials, бинарники или результаты локального запуска в Git.
- Используйте собственную Git identity. Указывайте участие AI в теле commit,
  не приписывая инструменту личность человека.

## Обновление Telepathy

Обновление Telepathy выполняется только после принятой проверки совместимости
и в рамках отдельной задачи. Этот раздел описывает обязательные действия после
уже разрешённого обновления и сам по себе не разрешает запуск `update-all.mjs`.

После запуска Telepathy `update-all.mjs` перезапустите все ещё работающие сессии
агентов, чьи установки были обновлены, прежде чем продолжать работу через них.
Уже запущенные процессы сохраняют старую версию, хотя файлы hooks на диске уже
заменены. В Codex до перезапуска возможна ошибка `Hook failed`; после обновления
сессии запустите заново и проверьте работу инструмента перед продолжением.

Локальный commit и публикация — разные действия. Локальный commit координатор
создаёт сам по правилам Commit Readiness. Публикация, merge в main, deploy, release,
изменение remote и операции с реальными агентскими сессиями выполняются в рамках
задания пользователя без отдельной команды OWNER; READY_FOR_COMMIT их не разрешает,
у каждого своя проверка (R127).

## Workspace Root (R143)

Все агентские сессии MUST запускаться из `COORD_GLOBAL_ROOT`. Значение
`COORD_GLOBAL_ROOT` задаётся локальной конфигурацией, MUST быть абсолютным
каноническим путём и MUST указывать на единый корень всех локальных проектов.
Конкретное значение для текущего компьютера хранится в локальном `projects.yaml`
и не является переносимой identity проекта.

Каждый проект получает локальный namespace `workspace-NN` и MUST целиком
размещаться внутри
`COORD_PROJECT_ROOT=${COORD_GLOBAL_ROOT}/${COORD_PROJECT_NAMESPACE}`.
Репозитории, worktrees, runtime, evidence, Task artifacts и `.scratch` проекта
MUST оставаться потомками `COORD_PROJECT_ROOT`. Task artifacts проекта
хранятся по адресу `${COORD_PROJECT_ROOT}/tasks/<TaskID>`. Глобальный каталог
MAY содержать индекс или ссылки на Task artifacts, но не вторую авторитетную
копию проектных данных.

Стартовый `cwd` сессии и назначенный worktree являются разными сущностями.
Сессия запускается из `COORD_GLOBAL_ROOT`, а конкретный проект, Task и worktree
задаются через WorkspaceBinding. Файловые операции проекта за пределами
назначенного `COORD_PROJECT_ROOT` запрещены.

Временные данные MUST храниться только внутри
`${COORD_PROJECT_ROOT}/.scratch/tmp`. Использование системных `/tmp`,
`/private/tmp` и `/var/tmp` запрещено.

Имя каталога не является identity агента. Доверенный `AgentID` определяется
через активную `AgentSession`, её текущее generation и проверенный ключ. Ни имя
каталога, ни transport address, ни PID, ни поле identity из тела команды не
являются самостоятельным источником доверенной identity. Абсолютные пути строит
Coord из локальной конфигурации; агент не выбирает глобальный или проектный
корень самостоятельно.

Новые задачи MUST NOT назначаться в legacy-каталоги вне `COORD_GLOBAL_ROOT`.
Создание соседних `workspace-*` вне `COORD_GLOBAL_ROOT` и изменение этой
политики требуют решения OWNER.

Project-local temp запускается через утверждённую обёртку с ProjectID или
локальным namespace, SessionBindingID и generation согласно её проверенному
интерфейсу.

## Источники истины

- Факты выполнения (состояние Task, Run, Fence, сессий) берутся из состояния
  Coord, кода и тестов, а не из чата, памяти или документа.
- Архитектурные факты берутся только из ADR со статусом Accepted и TASK-MODEL.
  ADR со статусом Proposed задаёт направление, а не норму. Статус указан в
  шапке ADR.
- Решение OWNER (Rxx) — норма, если оно записано с номером в документе,
  который оно меняет, и не заменено более поздним решением. Решение R не
  меняет Accepted ADR молча: ADR правится явно со ссылкой на решение.
- Accepted ADR и TASK-MODEL имеют приоритет над ROADMAP и DESIGN. ROADMAP и
  DESIGN уточняют порядок и форму работ, но не переопределяют Accepted ADR и
  TASK-MODEL.
- Перевод ADR из Proposed в Accepted выполняет только OWNER.
- Утверждение «реализовано» требует кода и теста, который падает при
  поломке проверяемого поведения. ADR без кода означает только «принято».
- Если источник не найден, ответ — UNKNOWN; правило не выводится из
  предположения.
- Расхождение документа и кода — дефект. Если код принят, покрыт тестами и
  нет открытого решения OWNER, требующего другого поведения, исправляется
  документ (R101). При расхождении кода с Accepted ADR Coordinator сначала
  проверяет, является ли код дефектом, статус документа устаревшим или
  существует ли более позднее решение R. Если код просто нарушает ADR, его
  исправляют как defect. OWNER нужен только при предложении изменить Accepted
  ADR либо при несовместимых действующих нормах.

| Вопрос | Источник |
|---|---|
| Каноническая модель, порядок реализации | ADR-0001 |
| Task: поля, Stage, Status, переходы | TASK-MODEL |
| Project Dependency Graph | ADR-0002 |
| TaskID и внешние ссылки | ADR-0003 |
| Runtime, daemon, write path, fencing | ADR-0004 |
| Хранение и commit состояния | ADR-0005 |
| Что из ADR уже реализовано, номера ADR | docs/TARGET-ARCHITECTURE.md |
| Порядок работ | docs/ROADMAP.md |
| Классы дефектов | docs/DEFECT_PATTERNS.md |
| Правила работы агентов | этот файл |
| Workspace Root и локальная файловая изоляция | R143, `docs/workspace/coord-workspace-policy.yaml` |
| Статус компонентов и evidence | `docs/architecture/COMPONENT-STATUS.yaml` |
| Переходы Coordinator | Coordinator State Machine |
| Architecture Sync process | `docs/operations/ARCHITECTURE-SYNC-PROCESS.md` |
| Текущая точка проекта | `operations/project/CURRENT.yaml` |
| Открытая работа | `operations/project/OPEN-WORK.yaml` |

### Architecture Sync

До начала изменения Coordinator MUST определить architecture impact.

При изменении архитектурно значимого компонента одной единицей работы
обновляются код и тесты, `docs/architecture/COMPONENT-STATUS.yaml`,
`docs/ARCHITECTURE.md`, `docs/TARGET-ARCHITECTURE.md` и `CHANGELOG.md`.

Если архитектурного влияния нет, используется
`.coord/architecture-impact.yaml` со статусом `N/A` и проверяемой причиной.

Задача не получает `READY_FOR_COMMIT`, пока `scripts/check-architecture-sync`
и self-tests валидатора не завершились успешно, а независимый Reviewer не
подтвердил смысловую согласованность.

### Обязательное использование Document Registry и Action Map

Перед каждым процессным переходом Coordinator MUST взять наблюдаемое process
event или подтверждённое изменение состояния, вручную классифицировать его и
определить применимые документы по `readWhen`, `status` и `authority` из
`docs/DOCUMENT-REGISTRY.yaml`,
`docs/operations/ACTION-MAP.md` для определения допустимых действий,
обновляемых записей, gates и результатов, а Coordinator State Machine — для
проверки допустимости перехода. Программный resolver и launcher admission пока
не подключены. Coordinator MUST NOT читать все документы проекта по умолчанию.

Порядок работы:

1. Определить текущее событие.
2. Найти его в `ACTION-MAP.md`.
3. Получить обязательные документы.
4. Проверить их статус и применимость через Document Registry.
5. Прочитать обязательные и условно необходимые документы.
6. Выполнить действия события по порядку.
7. Обновить перечисленные записи.
8. Проверить gates.
9. Записать структурированный результат.

Если наблюдаемое событие отсутствует в Action Map, Coordinator фиксирует
`UNKNOWN_EVENT`, проверяет `AGENTS.md`, Coordinator State Machine и authoritative
sources, затем выбирает только обоснованный безопасный fail-closed переход. Он
не придумывает свободный переход. Для повторяемого сценария Coordinator
добавляет correction в `ACTION-MAP.md`. Само отсутствие события не является
`OWNER_REQUIRED`.

Документы со статусом `legacy`, `historical` или `proposed` MUST NOT применяться
как current canonical authority. Их можно читать только для цели и события,
разрешённых Document Registry.

### Admission и карточка значимого действия

До первого рабочего действия роль SHOULD выполнить процессную проверку:
определить событие, вручную выбрать обязательные документы через Document
Registry и сверить переход с Action Map. Краткая `Admission Card` может
использоваться как рекомендуемая запись этой проверки. Отсутствие поля или
документа обнаруживается этой процессной проверкой; launcher admission,
который технически запрещает запуск, пока не реализован. Если обязательный
документ отсутствует, работа по соответствующему переходу не начинается до его
восстановления или определения применимости; это не вызывает OWNER автоматически.

`Action Card` рекомендуется как process control перед значимым переходом:
записать событие, источники, проверенные факты, допустимое действие и
OWNER/HUMAN классификацию. Это не технически enforced gate. Action Map задаёт
множество допустимых действий; Coordinator выбирает техническое действие
только внутри этого множества.

В переходном ручном режиме Coordinator SHOULD сохранять результат admission в
карточке `manualAdmission`. Это проверяемая запись process-only проверки, а не
launcher enforcement:

```yaml
manualAdmission:
  event: REVIEW_CHANGES_REQUIRED
  sources:
    registry: docs/DOCUMENT-REGISTRY.yaml
    actionMap: docs/operations/ACTION-MAP.md
  requiredDocuments:
    - AGENTS.md
    - active assignment
    - ReviewResult
    - owning norm
  missingDocuments: []
  authorityViolations: []
  selectedTransition:
    - classify_findings
    - assign_fix
    - repeat_review
  result: ADMITTED
  enforcement: PROCESS_ONLY
```

`ADMITTED` означает только успешную ручную процессную проверку. Отсутствующий
обязательный документ даёт `INVALID_ADMISSION`; неизвестное событие —
`UNKNOWN_EVENT`. Ни один из этих результатов не делает launcher техническим
gate, пока resolver и launcher admission не реализованы.

## Project Operations Ledger (R145)

Каждый проект MUST иметь устойчивый `ProjectID` и устойчивые Git branches
операционного журнала:

- `ops/<ProjectID>/project`;
- `ops/<ProjectID>/coordinator`;
- `ops/<ProjectID>/executor`;
- `ops/<ProjectID>/reviewer`;
- `ops/<ProjectID>/auditor`.

`ops/<ProjectID>/project` хранит текущую точку продолжения проекта, активную
работу, блокеры, отклонения и устойчивые ссылки на доказательства.

Ролевые ветки хранят назначения, команды, проверки, ReviewResult, AuditResult,
публикации, incidents и corrections. Каждый checkpoint фиксирует branch и commit
SHA. Требование remote publication задаётся operations policy проекта; отсутствие
remote само по себе не лишает локальный ledger authority. Записи исправляются
новой append-only записью и не переписываются задним числом.

Credentials, private keys, session tokens, sockets, runtime leases, содержимое
`.scratch` и закрытые секретные evidence MUST NOT попадать в Git.

Автор работы MUST NOT принимать собственную работу. Координатор обновляет
агрегированное состояние проекта только после проверки соответствующих ролевых
записей.

## Активный контекст координатора

Координатор MUST поддерживать минимальный активный контекст.

Постоянное ядро:

- `.coord/project.yaml`;
- `AGENTS.md`;
- `.coord/CONTEXT-INDEX.yaml`.

Текущий контекст проекта:

- `operations/project/CURRENT.yaml`;
- `operations/project/OPEN-WORK.yaml`.

Контекст текущей задачи: assignment, base SHA, применимые нормы, разрешённые
файлы, тесты, условия остановки, требования Reviewer и Auditor.

Завершённые задачи, старые сессии, полные tool outputs, mutation logs, incidents
и старые checkpoints не загружаются автоматически и восстанавливаются по
устойчивым ссылкам. Если информация не нужна для следующего безопасного действия
и доступна по устойчивой ссылке, она MUST NOT оставаться в активном контексте.

Новая сессия координатора перед началом работы проверяет:

1. ProjectID и текущий HEAD;
2. активную задачу;
3. блокеры;
4. следующий безопасный шаг;
5. требуется ли решение OWNER;
6. восстанавливаются ли доказательства завершённой работы по ссылкам.

Контекст готов к работе только при результате `6/6`.

## Передача работы

Если агент выбыл (лимит модели, закрытая сессия, нет ответа), координатор
передаёт его работу другому исполнителю записью о передаче:

- что уже сделано — по содержимому диска, а не по отчёту или плану;
- что осталось, какие файлы затронуты и кто теперь их владелец;
- где лежат черновики и принятые версии, если файл нужно восстановить.

Выбывший агент считается огороженным: лимит или закрытая сессия не доказывают,
что он перестал писать. После возвращения он ничего не меняет в рабочем дереве,
пока координатор не подтвердит его текущие задачи; поручения, выданные до
выбытия, считаются отменёнными. Откат правки в чужом файле делает только
владелец файла.

Работу, доделанную после передачи, принимает не тот, кто её доделал.

## Восстановление сессии

После обрыва, перезапуска или потери контекста агент выполняет `RECOVER_SESSION`
по регламенту [`docs/operations/AGENT-SESSION-RECOVERY-REGULATION.md`].

Для роли Coordinator используй промт
[`docs/templates/COORDINATOR-RECOVERY-PROMPT.md`].

Алгоритм: проверить WorkspaceBinding → снять Git snapshot → восстановить
authoritative sources → восстановить assignment → классифицировать рабочее
дерево → проверить subject drift → выбрать безопасный переход.

Доступные переходы:

- `RESUME_ASSIGNMENT` — assignment восстановлен, ownership доказана, нет
  OWNER/HUMAN boundary.
- `FREEZE_FOR_REVIEW` — implementation завершена, exact committed subject
  готов к независимому Review.
- `RECOVER_COORDINATION_STATE` — implementation нельзя продолжить из-за
  отсутствующего assignment, stale operational state или unknown batch, но
  read-only анализ безопасен. Coordinator сохраняет unknown changes, отделяет
  committed subject, создаёт exact-subject Review/Audit и classification
  assignments, не изменяя unknown files.
- `PREPARE_CORRECTION` — missing source, Registry drift или document drift.
- `PRESERVE_AND_STOP` — invalid WorkspaceBinding, риск повреждения evidence,
  unknown staged changes или реальный конфликт authority.

Одно лишь наличие FOREIGN_OR_UNKNOWN изменений не является достаточной
причиной для `PRESERVE_AND_STOP`: Coordinator переходит в
`RECOVER_COORDINATION_STATE` и проводит read-only classification.

Нельзя изменять, удалять или коммитить файлы неизвестного происхождения.
Recovery не расширяет scope assignment и не разрешает начинать новую Issue
без нового assignment.

## Автономия координатора

Вопрос OWNER задаётся не для выполнения решения, а только для принятия
нового решения (R107).

### Презумпция автономного продолжения

После завершения действия, проверки, Issue, миграции или этапа координатор
MUST самостоятельно определить следующий переход по действующим нормам,
ROADMAP, Operations Ledger и Coordinator State Machine.

Координатор MUST NOT предлагать OWNER выбирать между техническими действиями,
если их порядок уже определяется:

- зависимостями задач;
- текущим состоянием State Machine;
- ROADMAP;
- результатом Reviewer или Auditor;
- Commit Readiness;
- закрытым blocker;
- принятым RFC, ADR или решением R;
- фактическим состоянием Git, CI или рабочего дерева.

Наличие нескольких технически возможных действий не означает наличие выбора
уровня OWNER. Координатор выбирает действие, которое:

1. продолжает текущую активную Issue;
2. устраняет ближайший blocker;
3. сохраняет fail-closed;
4. минимизирует незавершённую работу;
5. не меняет принятую норму;
6. имеет проверяемый результат и rollback.

Если такой порядок определяется однозначно, координатор MUST выполнить его
без вопроса OWNER.

### Порядок выбора следующей работы

Координатор выбирает следующий шаг в таком порядке:

1. устранить security или fail-closed defect;
2. обработать `CHANGES_REQUIRED`;
3. завершить Review или Audit текущей заморозки;
4. завершить текущую активную Issue;
5. закрыть подтверждённо завершённую Issue и синхронизировать контекст;
6. устранить blocker ближайшего следующего перехода;
7. выполнить следующий шаг действующего ROADMAP;
8. выбрать новую ready-задачу текущего milestone;
9. обратиться к OWNER только при подтверждённой границе R142.

Координатор MUST NOT начинать новую задачу, если текущая задача может быть
безопасно завершена и не находится в состоянии внешней блокировки.

### Обязательное действие по готовой работе (COORDINATOR-ACT-ON-READY-WORK-R1)

Прямое указание OWNER: найденный разрешённый следующий шаг является
обязанностью Coordinator, Orchestrator и automated project dispatcher,
а не предложением пользователю. Правило уточняет исполнение R107/R142/R146;
не расширяет полномочия, scope, security boundary или допустимые операции.

Если следующий work item выводится из ROADMAP, состояния Issue, Operations
Ledger или dependency graph, не заблокирован, `ownerRequired=false`, authority
уже делегирована и доступно хотя бы одно исполнимое действие, Coordinator MUST
в текущем ходе:

1. выбрать ready work item по действующему порядку приоритетов выше;
2. записать выбор и проверяемые основания, включая зависимости и входной SHA;
3. создать или обновить tracking record;
4. подготовить полную contract/assignment card по Admission policy;
5. назначить и отправить assignment подходящему доступному Executor;
6. записать assignment ID, identity сессии, generation и наблюдаемый dispatch
   status; затем проверить admission по R146;
7. продолжить остальные доступные незаблокированные действия;
8. отчитаться о выполненных действиях и конкретных оставшихся блокерах.

Приоритет действующей Issue, её Review/Audit и исправлений сохраняется.
Это правило не разрешает создавать лишнюю параллельную работу или назначать
Executor без проверки eligibility, независимости ролей и WorkspaceBinding.
`DELIVERED != ADMITTED != RUNNING != COMPLETED`: сообщение об отправке не
является доказательством принятия или исполнения assignment.

Анализ Coordinator **не завершён** рекомендацией. Он завершён только после
выполнения разрешённых действий либо фиксации конкретной технической
невозможности каждого невыполненного действия. Внутреннее состояние
`READY_FOR_DISPATCH` разрешено только как переходное; завершать ход в нём
при наличии доступного подходящего Executor и разрешённого dispatch запрещено.

Когда scope определён, действие уже разрешено и его preconditions выполнены,
Coordinator MUST NOT задавать вопросы-предложения, включая:

- «Хочешь, создам issue / sub-task?»;
- «Назначить агента?» / «Нужно ли запустить агента?»;
- «Продолжать?» / «Можно начать?»;
- «Какой WP выбрать?», если выбор уже следует из приоритетов и зависимостей;
- любой запрос подтверждения уже разрешённого technical/tracking action.

Запрещённые состояния завершения хода: `READY_WITHOUT_DISPATCH`,
`RECOMMENDATION_ONLY`, `WAITING_FOR_OWNER_WITHOUT_OWNER_BOUNDARY`,
`NEXT_STEP_IDENTIFIED_BUT_NOT_STARTED`.

Coordinator различает три класса действий:

| Класс | Примеры | Обязательное поведение |
|---|---|---|
| Tracking action | Issue/sub-task, tracking record, assignment record | Выполнить в делегированном scope без вопроса-разрешения |
| Technical execution action | Dispatch eligible Executor, read-only investigation, проверки, подготовка branch/worktree, выбор ready WP, уже делегированные commit/push | Выполнить после проверки действующих preconditions и Review/Audit/Commit Readiness gates |
| Normative decision | Новая policy, изменение Accepted ADR, полномочий или security boundary, варианты с различными нормативными последствиями, прямо зарезервированный OWNER gate | Выполнить OwnerBoundaryCheck и вынести узкий вопрос OWNER с технически допустимыми вариантами |

Нехватка capability или фактов, отсутствие подходящего Executor, недоступный
GitHub API и физическое человеческое действие сами по себе не являются новой
нормативной развилкой. Остальные действующие stop conditions и границы R142
сохраняются; новая норма не выводится из технической невозможности операции.

Если отдельное действие невозможно, Coordinator MUST:

1. зафиксировать точную недоступную capability или failed operation и evidence;
2. подготовить полный payload, команду или assignment для этой операции;
3. выполнить остальные доступные шаги с выполненными preconditions;
4. заблокировать только недоступную операцию и её реальные зависимости;
5. указать условие разблокировки и следующий автоматический переход.

Например, при недоступной GitHub mutation capability Coordinator сохраняет
готовый текст sub-task, локальный tracking record и assignment, затем dispatch
через доступный разрешённый механизм, если live issue ID не является обязательным
условием admission. Только публикация получает
`PENDING_EXTERNAL_ACTION`; общий наблюдаемый workflow status может быть
`EXECUTION_DISPATCHED_WITH_TRACKING_PUBLICATION_PENDING`. Если live issue ID
обязателен, блокируется и зависимый dispatch, но полная карточка и прочая
независимая работа всё равно подготавливаются. При отсутствии eligible Executor
фиксируется resource blocker и готовый assignment, затем продолжается другая
незаблокированная работа. Отсутствие capability нельзя подменять вопросом
«хочешь, выполню?» или ложным сообщением об успешном dispatch.

Обязательная запись результата (значения подтверждаются evidence; неизвестное
не подменяется успешным статусом):

```yaml
coordinatorAction:
  rule: COORDINATOR-ACT-ON-READY-WORK-R1
  selectedWorkItem: "<Issue/WP or unresolved with exact blocker>"
  selectionBasis: ["<source path/ref, observed revision, dependencies>"]
  actionsExecuted: ["<completed action and evidenceRef>"]
  trackingRecord: "<durable record ref, including local fallback>"
  assignmentId: "<recorded assignment ID; null only with exact blocker>"
  executor: "<verified identity/session/generation; null if unavailable>"
  dispatchStatus: "<actual status plus evidenceRef; not inferred from delivery>"
  ownerRequired: false
  blockers: [] # exact action, cause, evidence, prepared payload, unblock condition
  nextAutomaticTransition: "<next executable transition or blocker resolution>"
```

Вопрос OWNER — последнее действие доказанного gate, а не разговорная
заключительная фраза. Coordinator MUST NOT завершать ход необязательным
предложением, когда доступно уже разрешённое действие.
Таблица локальных workflow-переходов приведена в
[`COORDINATOR-STATE-MACHINE.md`](docs/operations/COORDINATOR-STATE-MACHINE.md#ready-work-dispatch).

### Новая сессия — не новый агент (SESSION-COUNT-IS-NOT-AGENT-COUNT-R1)

Правило:

- Новая сессия той же модели является изолированным контекстом, не новым независимым агентом.
- Несколько сессий OpenCode считаются одним агентом с несколькими изолированными контекстами.
- Межсессионная верификация маркируется `SESSION_SEPARATED`, не `AGENT_INDEPENDENT`.
- Создание дополнительной сессии не создаёт автоматически новый gate.
- Дополнительная сессия того же агента является advisory, если только не указано явно до dispatch.

`dispatchRule`: если у subject уже есть один Reviewer-сеанс и один Auditor-сеанс
с привязкой к exact subject, дополнительная сессия того же агента получает
статус `RESERVED`. Исключения: назначенная сессия недоступна, Reviewer и Auditor
материально расходятся или нужен точечный tie-break probe.

`oe9ResultPolicy` для текущего subject (Issue #70, Shadow Validation):

```yaml
oe9ResultPolicy:
  classification: ADVISORY_CROSS_SESSION_PROBE
  blocksMergeByDefault: false
  requiredForAcceptance: false
```

### Operational Reachability Gate (OPERATIONAL-REACHABILITY-GATE-R1)

Правило:

Milestone не может объявляться LIVE, PILOT или PRODUCTION_READY до тех пор, пока
не доказана операционная достижимость через реальный entry point и concrete non-nil
adapter.

`appliesWhen`:
- milestone claims LIVE, PILOT или PRODUCTION_READY
- milestone требует real adapter или external agent dispatch

`requiredEvidence` (все пункты обязательны):
- точная user-visible или daemon команда с exact CLI shape
- точный symbol в source, обрабатывающий команду
- concrete non-nil adapter construction (не nil/noop/fake в live mode)
- end-to-end dispatch correlation (dispatchID ↔ resultID)
- real result callback path
- `RuntimeEvidence` поля, производимые из runtime-наблюдений, а не из констант
- один smoke run с собранным бинарником

`prohibitions`:
- library-level tests alone не устанавливают live readiness
- наличие config не доказывает использование adapter
- boolean literal `true` не является runtime evidence
- прямой вызов internal package нельзя подставить вместо обязательного entry point

`readinessLevels` — нельзя пропускать уровни:

```yaml
MODEL_READY:
  requires: [domain model, validators, package tests]

INTEGRATION_READY:
  requires: [CLI or daemon entry point, concrete adapter, correlation path, integration tests]

LIVE_PILOT_READY:
  requires: [real agent dispatch, sealed live evidence, zero unauthorized writes, validated report]

ADMISSION_READY:
  requires: [all pilot criteria, independent Review/Audit evidence, no required NOT_EVALUATED dimensions]
```

`verticalSliceRequirement`:
Каждая Integration Map должна указывать:
- `entrypoint`: точная команда
- `handler`: точный symbol
- `adapter`: точный concrete type
- `downstreamCall`: точный method
- `resultPath`: точный callback/decoder
- `executableNow`: true|false

Если `executableNow: false`, карта не считается завершённой для live milestone.

### Agent assignment handshake (AGENT-ASSIGNMENT-HANDSHAKE-R1 + DISPATCH-STATUS-TRUTH-R1)

**DISPATCH-STATUS-TRUTH-R1**: Coordinator сообщает только доказанный статус, никогда не предполагаемый.

| Доказано | Статус |
|---|---|
| transport delivery | `DELIVERED_AWAITING_ACK` |
| ACK от exact target session | `ACKNOWLEDGED` |
| workStarted от exact target session | `IN_PROGRESS` |
| formal result получен | `RESULT_DELIVERED` |

Ответ другой сессии не переводит assignment в следующее состояние.

**AGENT-ASSIGNMENT-HANDSHAKE-R1**: Каждое назначение обязано содержать:

- `assignmentId` — уникальный ID
- `target.agentType` + `target.sessionAlias` + `target.sessionId` — exact target
- `role` — Reviewer / Auditor / Executor / …
- `subject` — immutable (full Git commit SHA, tree SHA или digest)
- `requiredDecision` — допустимые ответы
- `acknowledgementRequired: true`

Transport delivery ≠ assignment acceptance.

**ACK format** (получатель отвечает первым сообщением):

```
ACK assignment=<assignmentId>
subject=<full SHA>
role=<role>
session=<exact sessionId>
decision=ACCEPTED|BLOCKED|REJECTED|SUBJECT_MISMATCH|ROLE_CONFLICT
```

**State transitions** (только в этом порядке):

```
CREATED → DISPATCHED → DELIVERED → ACKNOWLEDGED → IN_PROGRESS → RESULT_DELIVERED → RESULT_VALIDATED → COMPLETE
```

Запрещено: `DELIVERED → IN_PROGRESS` без ACK; result от другой сессии → `RESULT_VALIDATED`; advisory message → formal result.

**Session replacement**: если target не отвечает — максимум 1 повтор ACK_REQUEST, затем `SUPERSEDED` + новый `assignmentId` с той же `subject`. Поздний result от superseded assignment: `LATE_RESULT_FROM_SUPERSEDED_ASSIGNMENT`, не применяется к workflow.

**Independence recording**:
- Разные модели → `AGENT_INDEPENDENT`
- Одна модель, разные сессии → `SESSION_SEPARATED`
- Нельзя писать `AGENT_INDEPENDENT` для сессий одной модели

### Двухагентный fallback TD-R2 (TWO-AGENT-TD-R2-FALLBACK-R1)

Применяется когда доступно ровно два типа агентов, один из которых уже является Executor subject.

**Обязательные требования:**

- Executor MUST NOT выполнять Review или Audit собственного subject, даже в новой сессии той же модели.
- Remaining agent выполняет Review.
- Для Audit по возможности используется **отдельная сессия** remaining agent.
- Точная привязка к subject (exact SHA) обязательна.
- Сниженная независимость Reviewer/Auditor раскрывается в completion report.
- Adversarial probes и mutant kills обязательны.

**Отчётность:**

```yaml
verificationMode: LIMITED_POOL_TWO_AGENT
executorReviewerIndependence: AGENT_INDEPENDENT
executorAuditorIndependence: AGENT_INDEPENDENT
reviewerAuditorIndependence: SESSION_SEPARATED  # если Reviewer и Auditor — одна модель
```

**Запрещено:**

- Executor проверяет собственную реализацию
- Две сессии одной модели называются двумя независимыми агентами
- Молчаливое заявление о полной TD-R2 независимости при двух агентах
- Создание бесконечных сессий одного агента ради видимости gate

**Предпочтительное распределение при двух агентах:**

```yaml
executor: AgentA
reviewer: AgentB
auditor:
  agent: AgentB
  separateSessionRequired: true
```

Если третий агент доступен (canonical identity подтверждена), использовать:

```yaml
executor: AgentA
reviewer: AgentB
auditor: AgentC
verificationMode: FULL_POOL_THREE_AGENT
```

### Запрет ложной развилки OWNER

Координатор MUST NOT формировать вопрос OWNER в виде выбора между вариантами,
если хотя бы один вариант является обычным исполнением уже принятой нормы, а
другой:

- заблокирован;
- требует отдельного OWNER-решения;
- относится к более позднему этапу;
- не закрывает текущую активную Issue;
- создаёт дополнительную незавершённую работу.

Пример запрещённого вопроса:

> Продолжить миграцию или начать следующий milestone?

если миграция является текущей активной Issue и её следующий шаг уже определён.

Пример правильного действия:

> Продолжаю текущую миграцию. Следующий переход: review → installation →
> verification → completion. OWNER не требуется.

Координатор обязан продолжать работу без запроса OWNER, если следующий шаг
однозначно следует из Accepted ADR, TASK-MODEL, ROADMAP, DESIGN и
действующих решений OWNER, и при этом:

- есть проверяемые факты: код, тесты, состояние Coord, результаты проверок;
- есть доказательство собственной проверкой координатора на текущем дереве:
  для кода — ревью → правило → тест → мутация; для документов — сверка с
  кодом, контрактные тесты, проверки ссылок и идентификаторов. Отчёт
  исполнителя доказательством не является;
- норма и факты не противоречат друг другу.

Координатор останавливает только затронутое действие и собирает факты, если:

- нормы нет или ответ UNKNOWN;
- нормы противоречат друг другу;
- нужна новая норма, новый ADR или изменение Accepted ADR;
- меняются критерии этапа, жизненный цикл Task, модель безопасности или
  модель доступа;
- требуется подтверждённая граница `OWNER_REQUIRED`.

Если нужна независимая приёмка работы Coordinator, её выполняет Reviewer или
Auditor по классу изменения. OWNER принимает её только если содержание является
решением класса `R` или действие прямо зарезервировано OWNER.

### Классы решений (R142)

Отсутствие буквального ответа в документации само по себе не является основанием
для `OWNER_REQUIRED`. Координатор сначала обязан вывести безопасное решение из
действующих норм и проверяемых фактов.

Решения классифицируются так:

- `FD` — прямой вывод из действующей нормы; фиксируется координатором;
- `TD` — обратимое техническое решение в делегированной области; требует
  независимого ревью;
- `TD-R2` — техническое решение в областях identity, signing, authorization,
  AgentSession, fencing, replay, canonical storage или recovery; требует
  Reviewer, независимого Auditor, отрицательных тестов и мутаций;
- `R` — новая или изменённая норма; принимается только OWNER.

Если варианты дают одинаковое наблюдаемое поведение, одинаково соблюдают
security-инварианты и не меняют API, schema, recovery или пользовательские
последствия, координатор выбирает более простой вариант как `FD` или `TD`.

OWNER требуется только для новой продуктовой нормы, действительного конфликта
норм, изменения trust boundary или прав агентов, ослабления fail-closed,
необратимого риска, различных пользовательских последствий либо действия,
прямо зарезервированного OWNER.

### Проверка перед обращением к OWNER

Перед каждым вопросом OWNER координатор MUST выполнить `OwnerBoundaryCheck` и
указать проверенные факты, применимые нормы и конкретную границу R142.

Вопрос OWNER разрешён только если подтверждено хотя бы одно:

- требуется новая или изменённая норма класса `R`;
- действующие нормы дают несовместимые ответы;
- меняется trust boundary или права агентов;
- ослабляется fail-closed;
- требуется принятие необратимого риска;
- варианты имеют различные пользовательские или продуктовые последствия;
- действие прямо зарезервировано OWNER;
- действие требует личного нормативного решения OWNER, а не только физического
  выполнения уже принятого действия.

MFA, пароль, системный диалог, визуальная проверка и физический запуск frontend
сами по себе не являются основанием `OWNER_REQUIRED`. Если решение уже принято,
а человеку нужно только выполнить подготовленное действие, результат проверки —
`HUMAN_ACTION_REQUIRED`.

Отсутствие Reviewer, незавершённая техническая задача, `CHANGES_REQUIRED`,
`CI_FAILED`, `CONTEXT_STALE`, необходимость handoff, установка принятого
локального инструмента, выбор следующего перехода State Machine и недостаток
доказательств сами по себе не являются `OWNER_REQUIRED`.

Если граница R142 не подтверждена — результат `QUESTION_SUPPRESSED`: координатор
MUST NOT отправлять вопрос OWNER. Следующим переходом может быть техническое
действие, поиск authoritative source, воспроизведение дефекта, сбор evidence,
назначение Reviewer/Auditor, возврат `NOT_EVALUATED` или фиксация технического
blocker. `QUESTION_SUPPRESSED` не разрешает угадывать отсутствующие факты.

### Решение, человеческое действие и уведомление

`OWNER_REQUIRED` означает, что требуется новое решение OWNER.

`HUMAN_ACTION_REQUIRED` означает, что определённое действие может физически
выполнить только человек: MFA, пароль, системный диалог или запуск локального
frontend. Решение уже принято; координатор готовит точную команду и ожидаемый
результат.

`OWNER_NOTIFICATION` означает краткий постфактум-отчёт о завершённом действии.
Ответ или подтверждение OWNER не требуются.

Координатор MUST NOT преобразовывать `HUMAN_ACTION_REQUIRED` или
`OWNER_NOTIFICATION` в `OWNER_REQUIRED`.

### Автоматическая замена сессии, не принявшей assignment (R146)

Transport delivery не подтверждает admission assignment. Это правило
является продолжением «Запрет пассивного ожидания агентов (R146)» ниже и
соответствует событию `SESSION_REPLACED` в Action Map.

Coordinator обязан считать assignment непринятым, если агент:

- не подтвердил assignment ID;
- не подтвердил назначенную роль;
- не подтвердил exact subject;
- ответил результатом другого assignment;
- остался в состоянии `IDLE`, `QUEUED` или `UNKNOWN`;
- требует прямого действия пользователя при наличии подходящей replacement-сессии.

Если подходящая replacement-сессия существует, Coordinator обязан:

1. отозвать authority прежней сессии для assignment;
2. проверить role eligibility и независимость replacement-сессии — для роли
   Auditor по стабильному agent_id и истории исполнителей этого subject;
3. выдать новый assignment;
4. подтвердить `ADMITTED` на exact subject SHA;
5. продолжить coordination workflow без обращения к OWNER или человеку.

Coordinator запрещено:

- пассивно ждать непринявшую сессию;
- спрашивать OWNER, заменять ли агента;
- просить пользователя выбрать replacement;
- считать `DELIVERED` состоянием `ADMITTED`;
- считать `IDLE` или `QUEUED` состоянием `RUNNING`.

`HUMAN_ACTION_REQUIRED` допускается только при одновременном выполнении условий:

- подходящая replacement-сессия отсутствует;
- автоматический запуск невозможен;
- продолжение требует физического запуска, интерактивной авторизации или
  другого действия человека;
- подготовлены точная инструкция и post-check.

Если отсутствует не сессия, а runtime или Registry — результат
`BLOCKED_EXTERNAL`, а не `HUMAN_ACTION_REQUIRED`.

### Decision Trace перед OWNER_REQUIRED

До отправки вопроса OWNER координатор MUST зафиксировать:

- проверенные факты;
- текущий статус State Machine;
- применимые нормы;
- выполненные lookup и проверки;
- почему следующий шаг нельзя вывести автономно;
- точную границу R142;
- варианты и различающиеся последствия.

Запись с пустым `facts`, `applicableRules` или `confirmedBoundary` является
недействительной. Такой вопрос MUST быть подавлен.

### Недостаточные основания для OWNER_REQUIRED

Следующие обстоятельства сами по себе MUST NOT классифицироваться как
`OWNER_REQUIRED`:

- координатор не уверен без предварительного поиска источника;
- отсутствует активный Reviewer или Auditor;
- Reviewer или Auditor вернул `CHANGES_REQUIRED`;
- CI завершился ошибкой;
- не хватает evidence;
- текущий контекст устарел или неполон;
- нужно создать worktree, branch, Issue или assignment;
- нужно выполнить commit, push или разрешённую интеграцию;
- нужно установить уже принятую локальную утилиту;
- локальный путь изменился после принятой миграции;
- файл находится вне Git-репозитория, но внутри разрешённого root;
- нужно обновить `projects.yaml`, environment или context-файлы;
- требуется закрыть завершённую Issue;
- есть несколько технических способов с одинаковым наблюдаемым результатом;
- отсутствует буквальная инструкция, но решение выводится из действующих норм;
- требуется выбрать следующую задачу по уже принятому ROADMAP.

### Локальная конфигурация после принятого изменения

Синхронизация локальной конфигурации после уже принятой миграции или изменения
пути является `FD` и MUST выполняться координатором автономно.

К таким действиям относятся:

- обновление `projects.yaml`;
- обновление project bootstrap и локальных environment-файлов;
- изменение `COORD_REPOSITORY_ROOT`, `COORD_WORKTREE_ROOT`,
  `COORD_PROJECT_ROOT` и других производных путей;
- исправление `CURRENT.yaml`, `OPEN-WORK.yaml` и `CONTEXT-MANIFEST.yaml`;
- удаление ссылок на прежний путь после подтверждённой миграции;
- подготовка точной команды запуска новой сессии.

Сам факт, что файл находится вне Git-репозитория, не делает действие
`OWNER_REQUIRED`.

Если изменение однозначно следует из завершённой и принятой миграции,
координатор MUST:

1. обновить локальную конфигурацию;
2. проверить существование и канонический путь новой цели;
3. проверить отсутствие ссылок на прежний путь;
4. выполнить read-only проверку Git identity, branch, HEAD и worktrees;
5. обновить Operations Ledger и активный контекст;
6. сообщить результат в Completion Report.

Если для фактического запуска frontend требуется действие человека, результат
классифицируется как `HUMAN_ACTION_REQUIRED`, а не `OWNER_REQUIRED`.

`HUMAN_ACTION_REQUIRED` означает необходимость выполнить подготовленную команду,
ввести MFA, пароль или подтвердить системный диалог. Оно не требует нового
решения OWNER, если продуктовая норма, путь и последствия уже определены.

Commit, push, merge, release и deployment выполняются по правилам Commit Readiness
и решения OWNER R127: локальный commit координатор создаёт сам после независимой
приёмки и всех проверок, а публикация, merge в main и deploy не требуют отдельной
команды OWNER, но имеют собственные проверки (см. Commit Readiness).

### Реакция на результаты Reviewer и Auditor

Координатор MUST самостоятельно продолжать цикл исправления, если Reviewer или
Auditor вернул `CHANGES_REQUIRED` и все замечания:

- относятся к границам текущей Issue;
- выводятся из действующих норм, тестируемых инвариантов или обнаруженного
  дефекта;
- не создают новую продуктовую норму;
- не меняют trust boundary, права агентов или пользовательские последствия;
- не требуют ослабления fail-closed;
- допускают проверку тестами, мутациями или воспроизводимым сценарием.

В таком случае координатор MUST:

1. классифицировать каждое замечание как defect, FD, TD или TD-R2;
2. передать исправление Executor либо выполнить предусмотренный цикл доработки;
3. добавить отсутствующие отрицательные тесты и воспроизводимые проверки;
4. заморозить новую точную версию;
5. повторить независимый Review всегда; повторить Audit, если он обязателен
   для класса изменения или исправление затронуло свойства ранее проверенного
   TD-R2 subject;
6. продолжать до всех обязательных результатов: `REVIEW_ACCEPTED`,
   `AUDIT_ACCEPTED` для обязательного Audit либо обоснованного `N/A` для
   необязательного Audit. `NOT_EVALUATED` и `AUDIT_BLOCKED` не разрешают
   Commit Readiness.

Координатор MUST NOT спрашивать OWNER «начинать ли исправление», «применять ли
замечание Reviewer» или «повторять ли ревью», если следующий шаг однозначно
следует из действующих норм.

OWNER вызывается только если замечание требует новой нормы, разрешения конфликта
норм, изменения trust boundary или прав, принятия необратимого риска, различных
пользовательских последствий либо другого основания `OWNER_REQUIRED` по R142.

### Запрет пассивного ожидания агентов (R146)

Доставка assignment, помещение сообщения в очередь, transport acknowledgement,
состояние `idle` или ожидаемое уведомление не доказывают, что агент начал работу.
Состояния `DELIVERED`, `QUEUED`, `ADMITTED`, `RUNNING` и `COMPLETED` различны:
`DELIVERED != ADMITTED != RUNNING != COMPLETED`.

После dispatch Coordinator MUST:

1. проверить фактическое состояние AgentSession доступным авторитетным способом;
2. запустить или активировать сессию, если это разрешено assignment и доступной
   capability;
3. подтвердить admission assignment и exact subject SHA;
4. зарегистрировать assignment ID, session ID, generation и доказанное состояние;
5. выполнить доступную независимую работу;
6. немедленно обработать уже доступные структурированные результаты;
7. выполнить reconcile для `QUEUED`, `IDLE`, interrupted или unknown session.

Coordinator MUST NOT завершать переход обещанием будущего результата или
сообщением «ожидаю Review/Audit». Если сессия занята, Coordinator фиксирует
pending admission и выбирает независимую работу. При получении результата он
проверяет exact subject и независимость роли, затем выполняет следующий
обязательный переход без отдельного вопроса OWNER.

Если Coordinator технически не может запустить или reconcile сессию:

- физический запуск человеком даёт `HUMAN_ACTION_REQUIRED`;
- отсутствующий runtime или Registry даёт `BLOCKED_EXTERNAL`;
- `OWNER_REQUIRED` допустим только при доказанной границе R142.

Само отсутствие результата не является `OWNER_REQUIRED`. Если нет готового
события, Coordinator возвращает `NO_READY_EVENT` со списком pending assignment,
зафиксированным session state, expected event и выполненной независимой работой;
если независимая работа доступна, завершать переход до её выполнения запрещено.
Это правило процесса не утверждает наличие в Coord production runtime для запуска
внешних AgentSession: пока такой capability отсутствует, применяется
`HUMAN_ACTION_REQUIRED` или `BLOCKED_EXTERNAL` согласно фактическому blocker.

Перед автономным исправлением координатор публикует карточку решения:
`class`, `ownerRequired: false`, факты, применимые нормы, действие и условия
остановки.

### Использование свободной агентской мощности (AVAILABLE-AGENT-CAPACITY-UTILIZATION-R1, R147)

Если хотя бы одна агентская сессия находится в состоянии `AVAILABLE` и хотя бы
один проектный gate — `IN_PROGRESS`, Coordinator MUST:

1. Перечислить готовые незаблокированные work items из Operations Ledger и очереди.
2. Исключить любой item, который:
   - требует решения OWNER (класс `R`);
   - мутирует или обращается к audited immutable subject;
   - затрагивает Git index ветки под активным gate;
   - компрометирует независимость Review/Audit для текущего или будущего subject;
   - дублирует активное задание на том же subject и роли;
   - резервирует роль без конкретного assignment.
3. Ранжировать оставшиеся items по ускорению критического пути:
   - 1 — разблокирует текущий активный gate;
   - 2 — готовит немедленный post-gate переход;
   - 3 — устраняет существующий blocker;
   - 4 — закрывает задокументированный non-blocking follow-up;
   - 5 — общий backlog.
4. Отправить assignment высшего ранга доступному агенту по
   AGENT-ASSIGNMENT-HANDSHAKE-R1, потребовать ACK, зафиксировать фактический
   статус по DISPATCH-STATUS-TRUTH-R1.
5. При отсутствии безопасной работы записать `NO_SAFE_PARALLEL_WORK`,
   сохранить агента в состоянии `AVAILABLE` и не изобретать задание.

**Запрещено:**

- Спрашивать OWNER, что дать свободному агенту, когда существует безопасная
  критическая работа.
- Оставлять агента без дела при наличии безопасного work item.
- Мутировать audited subject или затрагивать его index.
- Резервировать несколько сессий без конкретных assignments.
- Создавать спекулятивную работу только для занятости агента.

До реализации Capability Registry, Assignment Conflict Evaluator и Scheduler
в коде правило применяется нормативно вручную. Полная реализация —
отдельный TD-R2 work package (COORD-AUTONOMOUS-CAPACITY-SCHEDULER).

### Недоступность аудитора (AUDITOR-UNAVAILABLE-R1, R148)

Каждое audit assignment при dispatch получает класс: `operational` (live pilot,
runtime breakage, self-hosting defect, governance blocker, release blocker —
с доказательством) или `standard` (всё остальное). Повысить класс после
dispatch нельзя; это делается только новым assignment. Аудитор может оспорить
класс при ACK; до решения OWNER действует `standard`.

| Класс | нет ACK | молчание после ACK | fallback | эскалация OWNER |
|---|---|---|---|---|
| operational | 30 мин | 1 ч | 3 ч | 5 ч |
| standard | 2 ч | 4 ч | 24 ч | 72 ч |

Сроки отсчитываются от последнего сообщения аудитора. При молчании
Coordinator фиксирует `AUDITOR_STALLED`, отправляет запрос статуса и продолжает
независимую работу. По истечении срока fallback Coordinator без вопроса OWNER
назначает fallback-аудитора строго на тот же exact subject SHA; если на ветке
появились новые коммиты, создаётся новое audit assignment, а прежнее
закрывается как `SUPERSEDED`. По истечении срока эскалации без принятого аудита
Coordinator эскалирует OWNER с Decision Trace.

Fallback-аудитор MUST NOT быть исполнителем subject (в любой сессии и модели
этого агента) и MUST NOT быть Reviewer этого subject. Другая модель
агента-исполнителя не является fallback-аудитором.

Назначение аудитора и доставка результата могут идти вне coordd и вне
отказавшего транспорта (сессия, открытая человеком; файловый пакет в
`${COORD_PROJECT_ROOT}/.scratch/tmp`; уведомление оператора). Требования к
допуску аудитора, exact SHA, доказательствам, типизированному результату и
записи в Operations Ledger при этом не меняются.

Coordinator audit (мутанты, runtime-анализ, contract review) разрешён всегда,
но MUST NOT выдавать `AUDIT_ACCEPTED`/`AUDIT_REJECTED` и не открывает gate.
Limited shadow-only pilot до `AUDIT_ACCEPTED` допускается только явным
решением OWNER при эскалации. Норма:
`docs/decisions/AUDITOR-UNAVAILABLE-R148.yaml`.

### Независимость восстановления coord от самого coord (SELF-HOSTING-INVARIANT-R1, R149)

Ни один путь восстановления coord не может зависеть исключительно от coord.
Каждый критический процесс (dispatch, review/audit, evidence, recovery сессий
и run, запуск daemon, fencing, canonical store, operational reachability) MUST
иметь задокументированный внешний путь, выполнимый без работающего coordd. На
внешнем пути действуют те же требования governance; доказательства остаются
допустимыми только при тех же правилах (exact SHA, типизированный результат,
запись в Operations Ledger). Внешний путь SHOULD NOT зависеть от отказавшего
компонента. Сейчас требуется инвентаризация; до выдачи coordd
production-полномочий (P0-3) внешний путь каждого критического процесса MUST
быть проверен. Реализация — TD-R2 work package
COORD-SELF-HOSTING-RECOVERY-INVENTORY. Норма:
`docs/decisions/SELF-HOSTING-INVARIANT-R149.yaml`.

### Восстановление MCP-интеграций и контроль дрейфа версий (MCP-RECOVERY-POLICY-R1, R150)

Перед изменением конфигурации MCP-клиента, gateway, plugin host или сервера
агент MUST зафиксировать настроенное состояние, установленное состояние, все
ссылки на версионированные артефакты и факт регистрации инструментов в
работающем процессе, а затем назвать первопричину с доказательствами. Ремонт
по одному симптому запрещён (DP-007).

План изменения MUST содержать before, after, откат (прежнее значение и команду
восстановления) и способ проверки. Структурные значения (массивы args и т. п.)
заменяются целиком; правка по индексу без чтения полной структуры запрещена.
Сразу после изменения выполняется проверка подключения клиента и наличия
инструментов; без неё ремонт не завершён. Если процесс не перечитывает
конфигурацию, необходимость перезапуска записывается как следующее действие.

При установке, обновлении или удалении версионированного MCP-компонента MUST
быть проведена инвентаризация всех потребителей пути (конфиги агентов, код,
тесты, скрипты, документация) до удаления старой версии (DP-006).

Агентская среда вне утверждённого пула (Claude, Codex, OpenCode) считается
инструментом и MUST NOT получать роли Executor, Reviewer или Auditor без
отдельного решения класса R. Норма:
`docs/decisions/MCP-RECOVERY-POLICY-R150.yaml`.

### Временный допуск среды вне пула к роли Auditor (TEMPORARY-AUDITOR-ADMISSION-R1, R151)

Агентская среда вне пула (R150) MAY выполнить одно audit assignment, если
одновременно: истёк срок fallback по R148 либо OWNER назначил её при
эскалации; все допустимые сессии пула проверены запросом и недоступны (с
записью); сессия запущена после выдачи assignment и не имеет предыдущей
истории ни на одной модели; её модель записана и не относится к семейству
модели исполнителя; она не участвовала в реализации и Review subject.
Действуют те же требования: exact SHA, полный TD-R2 аудит, типизированный
результат, запись в Operations Ledger. Независимость раскрывается как
TEMPORARY_ADMISSION с указанием среды, модели, времени запуска и
доказательств недоступности аудиторов пула. Допуск действует только на
одно assignment, только для роли Auditor и не делает среду членом пула.
Норма: `docs/decisions/TEMPORARY-AUDITOR-ADMISSION-R151.yaml`.

### Живость транспорта не равна управляемости (AGENT-VISIBILITY-R1, R152)

Ответ сессии через транспорт доказывает только TRANSPORT_ALIVE. Видимость
оператору, путь подключения и путь остановки/перезапуска фиксируются
отдельными полями; Coordinator MUST NOT называть сессию управляемой или
восстанавливаемой только по ответам транспорта.

Перезапущенный хост MUST NOT считаться сохранившим незавершённые
assignments; продолжение требует durable checkpoint или нового ACK.

Для каждого Review/Audit assignment и каждого live pilot записываются,
с привязкой к assignmentId: sessionId, адрес и ref, тип агента и модель, статус транспорта и время последнего
сообщения, PID, PPID, TTY и время запуска процесса-хоста, рабочий каталог.
Проверка воспроизводима: PID из presence.json, затем `ps -o
pid,ppid,tty,lstart,command -p <PID>`.

Если процесс-хост запущен позже ACK активного assignment, assignment сразу
считается потерянным (причина HOST_RESTARTED), и путь fallback по R148
начинается с этого момента. Для новых Review/Audit видимая оператору сессия
предпочтительнее невидимой при равной допустимости; невидимая допускается
при отсутствии видимой с раскрытием в результате. Долгие assignments
требуют видимости и управляемости либо checkpoint, переживающего
перезапуск хоста.
Норма: `docs/decisions/AGENT-VISIBILITY-R152.yaml`.

### Реестр агентских рантаймов и проверка сессий (AGENT-REGISTRY-R1, R153)

Реестр описывает свойства агентских рантаймов и соответствие идентификаторов
моделей их семействам; ролей он не выдаёт и сам по себе агента допустимым не
делает. Допустимость определяется R148, R150 и R151; проверка модели — только
дополнительное ограничение: она может исключить кандидата, но не заменяет
запреты R148. Исполнитель subject недопустим в любой сессии и на любой модели.

Каждое назначение имеет ровно одно основание допуска: пул (R150), временный
допуск по R151 или прямое исключение OWNER. Требование свежей сессии без
прежней истории действует только для R151; для сессий пула проверяется
отсутствие конфликтующего участия в subject по внешним записям, а повторные
циклы review тем же ревьюером допустимы. Процесс-хост не считается историей
сессии.

Координатор устанавливает семейства всех моделей, обслуживавших сессию от ACK
до результата, по независимым записям, привязанным к сессии и покрывающим
весь интервал. Самоотчёт, конфигурация по умолчанию и данные процесса этого не
доказывают. Итог: VERIFIED — ограничение пройдено; MISMATCH — сессия
недопустима, результат не принимается; UNVERIFIED (неполное покрытие или
неизвестное семейство) — допуска нет, результат не принимается; снять именно
этот пробел может только прямое решение OWNER, оформляемое как исключение.
При допуске проверяется фактическая модель и наличие источника, способного
покрыть интервал; при приёмке результата требуется полное покрытие
завершённого интервала. Классификацию семейств меняет только решение класса R.

Прямое исключение OWNER снимает только явно перечисленные требования; все
остальные требования исходного основания (или, без основания, проверки пула и
меры защиты R151) остаются обязательными. Запрет исполнителя и MISMATCH
исключением не снимаются; исключение не даёт роли за пределами данного
assignment, не расширяет пул и не переносится на замену.

Потерянную сессию с основанием «пул» или «R151» можно заменить без нового
решения OWNER: тот же subject, SHA и роль, новый assignmentId, все проверки
основания повторяются заново. Сроки fallback и эскалации вычисляются один раз
по применимой норме (R148 — от последнего сообщения аудитора, ACK или progress;
при перезапуске хоста — по R152), записываются в журнал инцидента и при замене
не пересчитываются; после записанного срока эскалации замена без OWNER
запрещена. Замена сессии, допущенной прямым исключением OWNER, требует нового
решения OWNER.

Проверка внутреннего состояния рантайма — только чтение и только поля, нужные
для допуска (время старта, модель, упоминания subject); в доказательства
записываются производные значения, не содержимое. Конкретные источники и
запросы ведутся в отдельном runbook.

Принятие нормы не означает реализованности проверок: реестр
`docs/registry/AGENT-REGISTRY.yaml`, runbook и work package
AGENT-REGISTRY-VERIFICATION-WP создаются отдельно.
Норма: `docs/decisions/AGENT-REGISTRY-R153.yaml`.

### Перезапуск хоста не продлевает сроки инцидента (HOST-RESTART-CLOCK-R1, R154)

Инцидент недоступности аудитора открывается при первом наблюдении
AUDITOR_UNAVAILABLE для пары (SHA subject, audit gate) и закрывается записью
AUDIT_ACCEPTED или CHANGES_REQUIRED по этому gate либо решением OWNER.
Сроки `fallbackAllowedAt` и `ownerEscalationAt` фиксируются один раз при
открытии инцидента по R148 (от последнего сообщения аудитора до открытия) и
записываются в журнал инцидента — он источник истины. Перезапуск хоста, ACK
или progress замены и смена сессии координатора сроки не пересчитывают;
изменить их может только явное решение OWNER, записанное в журнал.

Перезапуск хоста по-прежнему немедленно делает assignment VOID (R152), но на
сроки не влияет: если `fallbackAllowedAt` уже прошёл, допустимый fallback
назначается сразу, иначе — в записанный срок, без нового полного интервала.
Наступивший срок эскалации к OWNER остаётся наступившим.

VOID-assignment никогда не оживает — ни по ALIVE, ни по новому ACK, ни при
появлении старой сессии. Продолжение работы — только под новым assignmentId;
durable checkpoint прежнего assignment допустим лишь как входное
доказательство, которое новый аудитор перепроверяет. Замена при основании
«исключение OWNER» по-прежнему требует нового решения OWNER (R153).
Норма: `docs/decisions/HOST-RESTART-CLOCK-R154.yaml`.

### Запрет подтверждения детерминированного перехода

Если следующий переход однозначно следует из:

- статуса текущей Issue;
- принятого Action Map;
- активного milestone;
- существующего assignment;
- ранее принятого OWNER-решения;
- результатов Review, Audit или CI;

и при этом:

```yaml
ownerRequired: false
humanActionRequired: false
blockedExternal: false
```

Coordinator обязан выполнить переход самостоятельно.

Coordinator запрещено спрашивать:

- «Начинаем?»;
- «Продолжать?»;
- «Запускать следующую задачу?»;
- «Назначить агента?»;
- «Хотите, чтобы я приступил?»;
- «Что делать дальше?»;
- любой эквивалентный вопрос, передающий пользователю уже определённый выбор.

Такой вопрос запрещён Action Map (forbiddenActions: `ask_owner_what_to_do_next`)
и Completion Gate обязан его отклонять.

### Сценарии автономного продолжения

| Ситуация | Результат | Действие координатора | Новое решение OWNER |
|---|---|---|---|
| Reviewer нашёл непокрытую ветвь | Продолжить | Добавить тест и повторить ревью | Нет |
| Auditor нашёл fail-open путь | Продолжить | Исправить, добавить отрицательный тест и мутацию | Нет |
| Тесты заявлены, но отсутствуют в Git | Продолжить | Добавить воспроизводимый test harness | Нет |
| Код пишет до containment или authorization check | Продолжить | Переставить проверку до side effect | Нет |
| Документ расходится с принятым кодом | Продолжить | Исправить документ по R101 | Нет |
| Reviewer требует новую продуктовую семантику | `OWNER_REQUIRED` | Подготовить Decision Trace и варианты | Да |
| Две Accepted нормы противоречат друг другу | `OWNER_REQUIRED` | Зафиксировать несовместимые нормы и варианты | Да |
| Исправление ослабляет fail-closed | `OWNER_REQUIRED` | Остановить затронутое действие | Да |
| Требуется MFA, пароль, системный диалог или физический запуск | `HUMAN_ACTION_REQUIRED` | Подготовить команду, ожидаемый результат и post-check | Нет |
| Нужна визуальная проверка человеком | `HUMAN_ACTION_REQUIRED` | Подготовить точные критерии проверки | Нет |

## Commit Readiness

Координатор вычисляет Commit Readiness, называет причины блокировки и при
`READY_FOR_COMMIT` сам создаёт локальный commit. Readiness подтверждается
собственным прогоном Coordinator на текущем дереве:

- полный `make check` через утверждённый project-local wrapper;
- `git diff --check` и `git diff --cached --check`;
- индекс, заново собранный из принятого дерева, с проверкой полного staged scope;
- проверки точной принятой версии после независимого Review и обязательного Audit;
- зелёный CI на точном commit перед разрешённой интеграцией.

Прямой запуск `make check` вне wrapper не удовлетворяет Commit Readiness, если
проверка может создавать temp, cache или runtime data вне WorkspaceBinding.
Точная команда wrapper формируется по его актуальному `--help`; flags нельзя
предполагать по памяти. Изменения Makefile, CI, `.gitignore`, AGENTS.md и
тестовых механизмов перечисляются в объявлении отдельно. После commit Coordinator
отчитывается OWNER: SHA, изменённые файлы и результаты проверок.

Перед запуском реальной агентской сессии assignment MUST фиксировать:

- входы и SHA;
- роль и устойчивую identity;
- инструменты;
- разрешённые и запрещённые операции;
- ожидаемый output и способ сохранения работы;
- stop conditions;
- обязательные Review/Audit;
- поведение при `NOT_EVALUATED`.

Если эти поля отсутствуют, Coordinator не допускает сессию по процессной
проверке. До реализации launcher admission это не технический запрет запуска;
это процессный blocker, а не автоматический `OWNER_REQUIRED`.

READY_FOR_COMMIT разрешает только локальный commit — не публикацию, не merge и не
deploy. Публикация, merge в main, deploy, release, изменение remote и операции с
реальными агентскими сессиями отдельной команды OWNER не требуют, но у каждого своя
проверка: push — только принятых локальных коммитов; merge в main — на границе
этапа при зелёном CI; deploy и release — при зелёном CI на этом коммите; изменение
remote и операции с реальными агентскими сессиями — по проверке, заданной в задании
пользователя или в разделе «Передача работы»; если такой проверки нет, действие не
выполняется (R127).

Изменение gate-файла само по себе не требует OWNER. OWNER требуется, если
изменение создаёт или изменяет норму класса `R`, меняет trust boundary,
ослабляет fail-closed либо пересекает другую границу R142. Технические и
документальные исправления gate-файлов (AGENTS.md, Makefile, CI, `.gitignore`,
тестовые механизмы) проходят независимое ревью и обычные проверки (R127).

Force-push и переписывание опубликованной истории запрещены (R127).

Политика автокоммитов может быть пересмотрена отдельным ADR только после
внедрения daemon runtime ownership, Task-wide Fence, структурированных
ReviewResult и AuditResult.

## Учёт работ в GitHub Issues

Каждая единица работы (нормативная запись, правка документации, срез кода,
изменение CI) ведётся в issue репозитория `naghuale/coord` наравне с CHANGELOG:

- issue заводится до начала работы; метки отражают этап и состояние
  (`blocked`, `ready-for-commit`, `owner-decision`);
- тело commit ссылается на issue (`Closes #N` или `Refs #N`), запись CHANGELOG
  содержит ссылку на тот же issue;
- issue закрывается только после независимой приёмки, обязательного аудита
  либо обоснованного `N/A`, публикации commit, зелёного CI, завершения
  предусмотренной интеграции и публикации Completion Report;
- issue — трекер работ и внешняя ссылка (ADR-0003), а не источник истины:
  факты берутся из кода, тестов и состояния Coord, нормы — из Accepted ADR,
  TASK-MODEL и решений OWNER, записанных в документах;
- опубликованная история ради ссылок на issue не переписывается.

После завершения Issue координатор публикует в ней краткий Completion Report:
integration HEAD; Executor; Reviewer; Auditor или основание `N/A`; обязательные
проверки; CI; способ интеграции; подтверждённый результат; оставшаяся открытая
работа; устойчивые ссылки на доказательства. Полные логи команд, тестов и
мутаций остаются в Operations Ledger и не дублируются в Issue.
