# Регламент восстановления агентской сессии после обрыва или перерыва

```yaml
documentStatus: Proposed
kind: AgentSessionRecoveryRegulation
appliesTo:
  - Coordinator
  - Executor
  - Reviewer
  - Auditor
normativeAuthority: false
ownerAcceptanceRequired: true
defaultBehavior: fail_closed_recovery
```

## 1. Назначение

Регламент определяет обязательный порядок самостоятельного восстановления агента после:

- неожиданного завершения сессии;
- потери контекста;
- перезапуска агента;
- длительного перерыва;
- смены процесса или frontend;
- отмены tool call;
- разрыва соединения;
- получения новой сессией ссылки на незавершённую работу;
- обнаружения рабочего дерева с неизвестными изменениями.

Главная цель: агент должен самостоятельно восстановить проверяемое состояние проекта, сохранить чужую и незавершённую работу, определить прежний assignment и продолжить только с безопасного перехода. Агент не спрашивает OWNER «что делать дальше», если ответ можно получить из authoritative sources, Git и evidence.

## 2. События восстановления

```yaml
events:
  SESSION_INTERRUPTED:
    meaning: текущая сессия оборвалась до нормального завершения

  SESSION_RESUMED:
    meaning: агент продолжает ранее начатую работу с сохранённым контекстом

  SESSION_REPLACED:
    meaning: новый агент или процесс продолжает работу прежней сессии

  CONTEXT_LOST:
    meaning: нет надёжного знания о предыдущем assignment или состоянии

  WORKTREE_STATE_UNKNOWN:
    meaning: найдены staged, modified или untracked changes неизвестного происхождения

  REQUIRED_SOURCE_MISSING:
    meaning: отсутствует обязательный current-state или assignment source

  SUBJECT_DRIFT_DETECTED:
    meaning: текущий tree, HEAD или document digest не совпадает с ранее принятым subject
```

## 3. Основные правила

### 3.1 Recovery предшествует любой новой работе

После события восстановления запрещено сразу:

- изменять файлы;
- запускать новый implementation slice;
- выбирать новую Issue;
- задавать OWNER общий вопрос «что делать дальше»;
- коммитить найденные изменения;
- выполнять reset, checkout, clean или stash;
- удалять unknown files;
- считать старый Review или Audit действительным для нового tree.

Сначала выполняется `RECOVER_SESSION`.

### 3.2 Инструментальные факты имеют приоритет

При восстановлении:

```text
Git / filesystem / CI / authoritative document
→ подтверждённый факт

сообщение предыдущего агента
→ REPORTED

предположение нового агента
→ UNKNOWN
```

`REPORTED` и `UNKNOWN` не проходят readiness gate.

### 3.3 Сохранение работы обязательно

Любые неизвестные изменения считаются потенциально ценной незавершённой или чужой работой.

До установления происхождения запрещено:

```text
git reset
git checkout -- <path>
git restore <path>
git clean
git stash
git commit
удаление файлов
перезапись файлов
```

### 3.4 Отсутствие события или файла не означает OWNER_REQUIRED

Если событие отсутствует в Action Map или обязательный источник не найден:

```text
проверить authority
→ проверить Registry и Git history
→ выбрать fail-closed correction
→ зафиксировать missing source
```

OWNER требуется только при реальной нормативной границе, конфликте норм или зарезервированном решении.

### 3.5 Recovery не расширяет assignment

Восстановление разрешает только:

- чтение authoritative sources;
- чтение Git state;
- безопасные проверки;
- восстановление assignment context;
- классификацию изменений;
- подготовку Recovery Report;
- продолжение прежнего assignment, если оно однозначно допустимо.

Recovery не разрешает начинать соседнюю Issue.

## 4. Минимальный recovery context

Coordinator загружает только:

```text
.coord/project.yaml
AGENTS.md
.coord/CONTEXT-INDEX.yaml
DOCUMENT-REGISTRY.yaml
ACTION-MAP
operations/project/CURRENT.yaml
operations/project/OPEN-WORK.yaml
active assignment
Operations Ledger
последний Completion/Review/Audit result для активной задачи
```

Historical reports и старые transcripts не загружаются без отдельного события.

## 5. Алгоритм восстановления

### Шаг 1. Проверить WorkspaceBinding

Агент устанавливает:

```yaml
workspace:
  globalRoot:
  projectRoot:
  repositoryRoot:
  assignedWorktree:
  requiredLaunchCwd:
  scratchRoot:
```

Правила проекта `coord`:

```text
COORD_GLOBAL_ROOT=/Users/andreybabenko/Downloads/workspace
COORD_PROJECT_ROOT=/Users/andreybabenko/Downloads/workspace/workspace-01
стартовый cwd агента=COORD_PROJECT_ROOT
временные файлы=workspace-01/.scratch/tmp
системный /tmp запрещён
```

Несоответствие binding останавливает recovery с `INVALID_WORKSPACE_BINDING`.

### Шаг 2. Снять неизменяющий Git snapshot

Обязательные проверки:

```bash
pwd
git -C "$REPOSITORY_ROOT" branch --show-current
git -C "$REPOSITORY_ROOT" rev-parse HEAD
git -C "$REPOSITORY_ROOT" status --short
git -C "$REPOSITORY_ROOT" diff --stat
git -C "$REPOSITORY_ROOT" diff --cached --stat
git -C "$REPOSITORY_ROOT" log --oneline -12
```

При необходимости дополнительно:

```bash
git -C "$REPOSITORY_ROOT" diff --name-status
git -C "$REPOSITORY_ROOT" diff --cached --name-status
git -C "$REPOSITORY_ROOT" ls-files --others --exclude-standard
```

### Шаг 3. Восстановить authoritative state

Агент находит источники через Document Registry и Context Index, а не по совпадению имени.

Для каждого источника фиксируется:

```yaml
source:
  path:
  status:
  authority:
  digest:
  found:
```

Копии из `examples/`, `.scratch/`, archived reports и transcripts не используются как current authority.

### Шаг 4. Восстановить assignment

Проверяются:

```text
assignment ID;
цель;
роль;
base SHA;
branch;
разрешённые файлы;
read-only paths;
инструменты;
expected outputs;
stop conditions;
Review/Audit requirements;
зарезервированные действия.
```

Если assignment не найден, агент проверяет CURRENT, OPEN-WORK, Operations Ledger и Git history.

Если активная работа однозначно не восстанавливается:

```yaml
result: REQUIRED_SOURCE_MISSING
nextTransition: PREPARE_CORRECTION
ownerRequired: false
```

### Шаг 5. Классифицировать рабочее дерево

Каждый файл относится к одной категории:

```yaml
categories:
  COMMITTED_ACCEPTED:
    meaning: уже входит в принятый subject

  CURRENT_ASSIGNMENT_UNCOMMITTED:
    meaning: относится к восстановленному assignment

  POST_ASSIGNMENT_SYNC:
    meaning: документация, CI или state-sync после основного implementation

  FOREIGN_OR_UNKNOWN:
    meaning: происхождение не доказано

  GENERATED:
    meaning: воспроизводимый generated output

  SCRATCH:
    meaning: project-local temporary data
```

`FOREIGN_OR_UNKNOWN` запрещено изменять до отдельного assignment или доказанной ownership.

### Шаг 6. Проверить subject drift

Сравниваются:

```text
текущий HEAD;
assignment base SHA;
Review subject SHA;
Audit subject SHA;
CI commit SHA;
document digests;
staged tree;
working tree.
```

Если tree изменился после Review/Audit:

```text
старый verdict не переносится
→ freeze new subject
→ повторить необходимые gates
```

### Шаг 7. Выполнить безопасные проверки

Проверки выполняются только через фактический project-local wrapper и его актуальный интерфейс.

Минимум:

```text
git diff --check
релевантные package tests
полный project check, если доступен
```

Временные data, cache и reports направляются только внутрь project scratch.

### Шаг 8. Выбрать переход

```yaml
transitions:
  RESUME_ASSIGNMENT:
    when:
      - assignment однозначно восстановлен
      - workspace binding valid
      - ownership изменений доказана
      - нет нового OWNER/HUMAN blocker

  FREEZE_FOR_REVIEW:
    when:
      - implementation завершена
      - exact subject определён
      - обязательные проверки пройдены

  PREPARE_CORRECTION:
    when:
      - отсутствует обязательный current-state source
      - event отсутствует в Action Map
      - найден документальный drift

  BLOCKED_EXTERNAL:
    when:
      - требуется внешний artifact, Registry, provider или runner

  HUMAN_ACTION_REQUIRED:
    when:
      - требуется credential, MFA, protected runner или физическое действие

  OWNER_REQUIRED:
    when:
      - требуется новая норма
      - обнаружен реальный конфликт норм
      - решение зарезервировано за OWNER

  PRESERVE_AND_STOP:
    when:
      - unknown changes могут быть повреждены
      - безопасное продолжение не доказано
```

## 6. Самостоятельное продолжение

Агент может продолжить прежний assignment без нового вопроса OWNER, когда:

```yaml
resumeConditions:
  workspaceBindingValid: true
  activeAssignmentRecovered: true
  currentBranchExpected: true
  headCompatible: true
  changesOwnedByAssignment: true
  mandatorySourcesAvailable: true
  ownerBoundaryAbsent: true
  humanActionBoundaryAbsent: true
  stopConditionAbsent: true
```

Перед первым изменением агент публикует краткую Recovery Card:

```yaml
recoveryCard:
  event: SESSION_RESUMED
  assignmentId:
  subject:
  ownedChanges: []
  preservedUnknownChanges: []
  selectedTransition: RESUME_ASSIGNMENT
  ownerRequired: false
  humanActionRequired: false
```

После этого отдельное подтверждение пользователя не требуется.

## 7. Обязательный Recovery Report

Если продолжение не полностью однозначно или найдены изменения, агент формирует отчёт
по схеме `docs/schemas/recovery-report.schema.json` и шаблону
`docs/templates/RECOVERY-REPORT-TEMPLATE.yaml`.

## 8. Stop conditions

Recovery немедленно останавливается без destructive action при:

```text
ProjectID mismatch;
ProjectRoot mismatch;
repository/worktree вне project root;
неизвестных staged changes;
неподтверждённой ownership;
отсутствующем mandatory authority source;
конфликте Accepted norms;
credential/MFA boundary;
subject drift, требующем нового Review/Audit;
невозможности сохранить найденную работу;
подозрении на credential или hidden-case leakage.
```

## 9. Интеграция в процесс

После принятия регламента требуется:

```text
1. Зарегистрировать документ в DOCUMENT-REGISTRY.yaml.
2. Добавить события восстановления в ACTION-MAP.
3. Добавить обязательный recovery entrypoint в AGENTS.md.
4. Добавить Recovery Report schema.
5. Добавить scenario tests.
6. Добавить launcher admission rule для resumed sessions.
7. Добавить CI static checks.
```

Рекомендуемые scenario IDs:

```text
RECOVERY-01 clean interrupted session
RECOVERY-02 owned uncommitted changes
RECOVERY-03 unknown modified files
RECOVERY-04 missing CURRENT/OPEN-WORK
RECOVERY-05 subject drift after Review
RECOVERY-06 wrong workspace root
RECOVERY-07 missing assignment
RECOVERY-08 human action boundary
RECOVERY-09 owner boundary
RECOVERY-10 examples mistaken for authority
```

---

# Универсальный Recovery Prompt

```text
Продолжай как агент роли <ROLE> после обрыва, перезапуска или перерыва сессии.
Не спрашивай пользователя «что делать дальше», пока ответ можно восстановить из
Git, assignment, current-state документов и действующих норм.

Сначала выполни только fail-closed recovery. Не изменяй файлы и не начинай новую
Issue до завершения восстановления.

Правила:

1. Установи WorkspaceBinding из project configuration:
   - global root;
   - project root;
   - repository root;
   - assigned worktree;
   - обязательный launch cwd;
   - project-local scratch root.

2. Проверь, что текущий cwd соответствует обязательному project root. Не создавай
   временные файлы вне project-local scratch. Не используй системный /tmp.

3. Сними неизменяющий Git snapshot:
   - branch;
   - HEAD;
   - status --short;
   - staged diff stat/name-status;
   - unstaged diff stat/name-status;
   - untracked files;
   - последние коммиты.

4. Не выполняй reset, restore, checkout, clean, stash, commit, merge, rebase,
   удаление или перезапись файлов во время recovery.

5. Загрузить только минимальные authoritative sources:
   - project configuration;
   - AGENTS;
   - Context Index;
   - Document Registry;
   - Action Map;
   - CURRENT;
   - OPEN-WORK;
   - active assignment;
   - Operations Ledger;
   - последний релевантный Review/Audit/Completion result.

6. Не используй examples, scratch, historical reports или transcripts как current
   authority. Если обязательный источник отсутствует, зафиксируй
   REQUIRED_SOURCE_MISSING и выбери fail-closed correction. Не объявляй
   OWNER_REQUIRED только из-за отсутствующего файла.

7. Восстанови active assignment:
   - assignment ID;
   - цель;
   - base SHA;
   - роль;
   - разрешённые и запрещённые пути;
   - инструменты;
   - expected outputs;
   - проверки;
   - stop conditions;
   - Review/Audit requirements.

8. Классифицируй каждый changed/untracked файл:
   - COMMITTED_ACCEPTED;
   - CURRENT_ASSIGNMENT_UNCOMMITTED;
   - POST_ASSIGNMENT_SYNC;
   - FOREIGN_OR_UNKNOWN;
   - GENERATED;
   - SCRATCH.

9. Проверить subject binding:
   - HEAD против assignment base;
   - working tree против Review/Audit subject;
   - CI SHA;
   - document digests.
   Старый Review/Audit не переносится на изменённый subject.

10. Если assignment и ownership однозначно восстановлены, mandatory sources
    доступны и OWNER/HUMAN boundary отсутствует:
    - сформируй Recovery Card;
    - выбери RESUME_ASSIGNMENT;
    - продолжай прежний assignment самостоятельно в его исходном scope.

11. Если implementation завершена, выбери FREEZE_FOR_REVIEW, а не новую Issue.

12. Если найдены unknown changes или риск потери работы, выбери PRESERVE_AND_STOP.

13. OWNER_REQUIRED допускается только для новой нормы, реального конфликта норм
    или зарезервированного решения. HUMAN_ACTION_REQUIRED допускается только для
    credentials, MFA, protected runner или физического действия.

Выведи структурированный Recovery Report. Если условия RESUME_ASSIGNMENT
выполнены, после отчёта продолжай исходный assignment без запроса подтверждения.
Если условия не выполнены, остановись на безопасном переходе и ничего не
разрушай.
```
