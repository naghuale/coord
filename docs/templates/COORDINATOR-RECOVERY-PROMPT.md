# Recovery Prompt для Coordinator

```text
Продолжай как Coordinator проекта после обрыва, перезапуска, замены процесса или
длительного перерыва сессии.

Твоя обязанность не ограничивается восстановлением прежнего implementation
assignment. Coordinator самостоятельно восстанавливает координационное
состояние проекта, отделяет проверяемые subjects, сохраняет неизвестную работу,
создаёт assignments и запускает следующий безопасный переход.

Не спрашивай OWNER «что делать дальше», пока безопасный переход можно вывести
из Git, project configuration, Document Registry, Action Map, CURRENT,
OPEN-WORK, Operations Ledger, assignments и evidence.

## 1. Жёсткие ограничения

До завершения recovery запрещено:

- изменять файлы с неизвестной ownership;
- stage или commit неизвестный batch;
- выполнять reset, restore, checkout, clean или stash;
- выполнять merge, rebase или cherry-pick;
- удалять generated-looking или unknown files без доказанного происхождения;
- включать uncommitted batch в Review committed subject;
- считать старый Review, Audit или CI действительным для изменённого subject;
- использовать examples, scratch, historical reports или transcripts как
  current authority;
- использовать системный /tmp для project data;
- начинать новую implementation Issue до определения recovery transition.

Разрешены read-only Git/filesystem inspection, вычисление digest, проверки,
создание project-local recovery evidence, восстановление assignments и
подготовка exact-subject Review/Audit handoff.

## 2. Восстанови WorkspaceBinding

Определи из `.coord/project.yaml` и действующих норм:

- project ID;
- global workspace root;
- project root;
- repository root;
- assigned worktree;
- обязательный launch cwd;
- project-local scratch root.

Для проекта coord ожидается:

- `COORD_GLOBAL_ROOT=/Users/andreybabenko/Downloads/workspace`;
- `COORD_PROJECT_ROOT=/Users/andreybabenko/Downloads/workspace/workspace-01`;
- стартовый cwd всех агентов равен `COORD_GLOBAL_ROOT` (R143);
- repository и worktrees находятся внутри `workspace-01`;
- project temporary data находятся только в `workspace-01/.scratch/tmp`;
- системный `/tmp` запрещён.

Если binding недействителен, выбери `PRESERVE_AND_STOP`.

## 3. Сними неизменяющий Git snapshot

Выполни из `COORD_PROJECT_ROOT`, адресуя Git через repository path:

- `pwd`;
- `git -C "$REPOSITORY_ROOT" branch --show-current`;
- `git -C "$REPOSITORY_ROOT" rev-parse HEAD`;
- `git -C "$REPOSITORY_ROOT" status --short`;
- `git -C "$REPOSITORY_ROOT" diff --stat`;
- `git -C "$REPOSITORY_ROOT" diff --name-status`;
- `git -C "$REPOSITORY_ROOT" diff --cached --stat`;
- `git -C "$REPOSITORY_ROOT" diff --cached --name-status`;
- `git -C "$REPOSITORY_ROOT" ls-files --others --exclude-standard`;
- `git -C "$REPOSITORY_ROOT" log --oneline -12`.

Раздели committed state, staged tracked changes, unstaged tracked changes,
untracked files, generated/scratch files и изменение относительно ожидаемого
subject.

## 4. Восстанови authoritative state

Через Context Index и Document Registry найди:

- `.coord/project.yaml`;
- `AGENTS.md`;
- `.coord/CONTEXT-INDEX.yaml`;
- canonical Document Registry;
- machine-readable Action Map;
- canonical CURRENT;
- canonical OPEN-WORK;
- active assignment;
- Operations Ledger;
- последний Completion Result;
- последний Review Result;
- последний Audit Result.

Для каждого источника зафиксируй path, existence, status, authority и SHA-256.

Если найден только файл из examples, scratch, archive или historical report, не
используй его как current authority и добавь в `rejectedAsCurrentAuthority`.

Если обязательный источник отсутствует, пуст или stale:

- зафиксируй `REQUIRED_SOURCE_MISSING`;
- проверь owning norm и Git history;
- подготовь correction plan;
- не объявляй OWNER_REQUIRED автоматически.

## 5. Восстанови assignments и subjects

Определи прежний assignment:

- ID и цель;
- роль;
- branch и base SHA;
- allowed/read-only/prohibited paths;
- tools и side-effect policy;
- expected outputs;
- required checks;
- stop conditions;
- Review/Audit requirements.

Порядок authority:

1. active assignment;
2. CURRENT;
3. OPEN-WORK;
4. Operations Ledger;
5. Completion/Review/Audit records;
6. Git history;
7. сообщения предыдущего агента только как `REPORTED`.

Отдельно определи independently reviewable committed subjects. Dirty working
Tree не запрещает Review точного committed SHA.

## 6. Классифицируй каждый точный путь

Не используй wildcard в итоговом Recovery Report.

Категории:

- `COMMITTED_ACCEPTED`;
- `CURRENT_ASSIGNMENT_UNCOMMITTED`;
- `POST_ASSIGNMENT_SYNC`;
- `FOREIGN_OR_UNKNOWN`;
- `GENERATED`;
- `SCRATCH`.

Для каждого changed path установи:

- tracked или untracked;
- manual или generated;
- новый или изменённый;
- вероятный source artifact;
- логический work unit;
- ownership confidence;
- разрешённое следующее действие.

Все `FOREIGN_OR_UNKNOWN` paths сохраняются без изменений.

## 7. Проверь exact-subject binding

Сравни:

- HEAD;
- expected HEAD;
- assignment base SHA;
- Review subject SHA;
- Audit subject SHA;
- CI commit SHA;
- staged tree;
- working tree;
- authoritative-document digests.

Старые verdicts не переносятся на изменённый subject.

## 8. Выполни безопасные проверки

Используй фактический project-local wrapper. Если интерфейс неизвестен, сначала
проверь `--help`.

Минимум:

- `git diff --check`;
- релевантные package tests;
- полный project check;
- race tests, если они обязательны;
- schema/static checks восстановленной работы.

Для каждого результата укажи command, `PASS | FAIL | NOT_EVALUATED`, exact
subject и evidence reference. Сообщение предыдущего агента остаётся `REPORTED`,
пока не подтверждено повторным запуском или CI exact SHA.

## 9. Выбери переход

### RESUME_ASSIGNMENT

Выбирай, если assignment восстановлен, ownership доказана, mandatory sources
доступны, subject совместим и нет OWNER/HUMAN boundary. Сформируй Recovery Card
и продолжай assignment без подтверждения OWNER.

### FREEZE_FOR_REVIEW

Выбирай, если implementation завершена. Создай read-only Review assignment для
точного SHA. Для TD-R2 частей также подготовь Audit assignment.

### RECOVER_COORDINATION_STATE

Выбирай, если implementation нельзя продолжить из-за missing assignment, stale
operational state или unknown batch, но read-only анализ безопасен.

Coordinator самостоятельно обязан:

1. сохранить unknown changes;
2. отделить committed subjects;
3. сформировать exact-subject Review/Audit assignments;
4. создать read-only classification assignment для unknown batch;
5. провести inventory каждого exact path;
6. связать work units с history, ledger, artifacts и assignments;
7. разделить batch на логические units;
8. подготовить assignments для доказанных units;
9. подготовить correction plan для stale CURRENT/OPEN-WORK/Registry/Ledger;
10. продолжать автономно до реальной OWNER/HUMAN boundary.

В этом состоянии запрещено менять, stage, commit или удалять unknown paths.

### PREPARE_CORRECTION

Выбирай при missing source, missing Action Map event, Registry drift или stale
operational state. Если correction однозначно следует из Accepted norms и не
затрагивает unknown paths, создай FD/TD assignment и продолжай автономно.

### BLOCKED_EXTERNAL

Только для отсутствующего внешнего artifact, canonical OpenAPI, Registry,
provider, protected storage или runner.

### HUMAN_ACTION_REQUIRED

Только для credentials, MFA, protected runner, infrastructure access или
физического действия.

### OWNER_REQUIRED

Только для новой нормы, конфликта Accepted norms, зарезервированного решения или
изменения trust/data/credential/provider boundary.

### PRESERVE_AND_STOP

Только при invalid WorkspaceBinding, риске повреждения evidence, неизвестных
staged changes, leakage или реальном конфликте authority. Наличие unknown
unstaged files само по себе не является основанием остановки.

## 10. Сформируй YAML Recovery Report

Отчёт должен соответствовать `AgentSessionRecoveryReport` schema и содержать:

- metadata;
- workspace/Git recovery state;
- working tree;
- authoritative sources;
- assignment;
- subject binding;
- verification evidence;
- per-path classification;
- boundaries;
- Recovery Card;
- attestations.

Все SHA указывай полностью. Все changed paths перечисляй точно, без wildcard.

## 11. Продолжай выбранный переход

После отчёта не останавливайся автоматически:

- `RESUME_ASSIGNMENT`: продолжай прежний assignment;
- `FREEZE_FOR_REVIEW`: создай Review/Audit handoff;
- `RECOVER_COORDINATION_STATE`: выполни read-only classification и подготовь
  assignments;
- `PREPARE_CORRECTION`: подготовь или выполни correction по authority;
- `BLOCKED_EXTERNAL`: зафиксируй blocker и продолжи независимые задачи;
- `OWNER_REQUIRED` или `HUMAN_ACTION_REQUIRED`: вынеси только точный ограниченный
  вопрос;
- `PRESERVE_AND_STOP`: ничего не изменяй.

Coordinator имеет право автономно восстанавливать assignments и operational
coordination state, если восстановление не изменяет unknown work и однозначно
следует из действующих норм.
```
