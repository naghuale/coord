# ADR-0008: Autonomous Acceptance & Commit

- **Статус:** Proposed
- **Редакционная пометка:** 2026-10-10: FD — AC-09 скорректирован до R127/R142 (gate-файл ≠ OWNER_REQUIRED автоматически); AC-02 уточнён (push/merge/deploy вне области); AC-06 разделяет Git-операцию executor и process lifecycle supervisor; AC-12 расширен до primary + ordered reasons; Commit Readiness отделён от execution state; prerequisites расширены до capability-списка; ссылка на RFC-0001 для workspace layout; "future capability" формулировка. Протокол CommitIntent, reconciliation, Fence-to-ref binding, replay, Git identity/signing — TD-R2, требуют отдельного Reviewer + Auditor; здесь отмечены как не определены. Документ остаётся Proposed.
- **Область:** runtime-управляемый локальный commit после структурированной приёмки в ветку задачи
- **Основание:** требования владельца от 2026-10-08 (AG-001…AG-010 и чек-лист)
- **Связанные решения:** ADR-0001 Canonical Coordination Model (шаг 8, структурированные Review/Audit; порядок реализации); ADR-0004 Runtime and Daemon Model (Process Ownership, command path); ADR-0005 Storage Model (ST-01); ADR-0007 Project Brief и Task Brief

## Назначение

Coord сегодня не создаёт commit сам: по AGENTS.md (R127) локальный commit после
`READY_FOR_COMMIT` создаёт координатор-агент, а не runtime Coord. Этот ADR описывает
условия, при которых та же фиксация выполняется без явного решения владельца на
каждой задаче.

Документ остаётся **Proposed**, потому что каждое его условие упирается в ещё не
реализованный слой. Он не разрешает автокоммит в текущем дереве.

## Prerequisites

Autonomous commit не включается до тех пор, пока не реализованы следующие capabilities:

- canonical command path (ApplyAgentCommand в production daemon);
- Task-wide Fence с проверкой на каждом transition;
- exclusive Task worktree с гарантированной изоляцией;
- доказанный writer stop или exclusive writer lock с внешним supervisor evidence;
- structured ReviewResult и AuditResult (шаг 8 ADR-0001);
- commit intent и reconciliation protocol (не определены — TD-R2).

По ROADMAP-milestone это соответствует всем пяти этапам P0 и шагу 8 ADR-0001:

- P0-1 — production daemon skeleton;
- P0-2 — Task aggregate, TaskID adoption и Task-wide Fence;
- P0-3 — canonical Executor RESULT path и command boundary;
- P0-4 — production executor lifecycle и writer lock;
- P0-5 — supervisor конкретной установки и end-to-end stop proof;
- шаг 8 ADR-0001 — структурированные ReviewResult и AuditResult.

Этапы названы вместе с номером, потому что нумерация P0-x уже перенумеровывалась
один раз. Ссылка только на номер без названия сделала бы такую ошибку
незаметной при чтении.

После принятия и реализации этот ADR вводит runtime-managed local commit protocol. До этого действующим остаётся R127 для разработки репозитория.

Ограничения AC-01…AC-13 и критерии соответствия ниже описывают целевое поведение
runtime Coord по этому Proposed ADR и до его принятия не действуют. Где они
упоминают текущий порядок — ручной коммит, публикация и изменения gate-файлов за
OWNER, — это состояние на момент предложения ADR (2026-10-08). Различие областей
R127 и продукта Coord разобрано в разделе «Отличие от R127».

## Отличие от R127

R127 (AGENTS.md, Commit Readiness) регулирует процесс разработки этого репозитория.
Этот ADR описывает функцию продукта Coord. Это разные области: ADR не пересматривает
R127 и не согласуется с ним, а R127 не является доказательством реализации или принятия
автокоммита продукта.

## Локальный commit не равен публикации

Commit в ветку задачи — это запись в локальный репозиторий. В область этого ADR не
входят push, merge и deploy. Ни один инвариант этого документа не меняет состояние
удалённого репозитория, ветки `main` или окружения исполнения.

## Разрыв круговой зависимости

Входное требование AG-007 требовало, чтобы commit происходил при `Task COMPLETE`,
а чек-лист владельца помещал commit на этап 9, то есть после `COMPLETED`. В таком
виде условие неисполнимо: задача не может завершиться, потому что завершение
требует commit.

Нормативная последовательность вместо этого:

1. Writer Run терминальный, RESULT принят Coord.
2. Review PASS и Audit PASS на одном и том же tree hash. Gates определяются StagePlan:
   если Review предусмотрен — требуется Review PASS; если Audit предусмотрен или
   изменение относится к TD-R2 — требуется Audit PASS. NOT_EVALUATED не разрешает commit.
3. Commit Readiness = `READY_TO_COMMIT`. Task ещё не `COMPLETED`.
4. Executor выполняет Git-операцию: создаёт tree, создаёт commit object, выполняет
   CAS ветки. Process lifecycle (создание, запуск, остановка) принадлежит внешнему
   supervisor, а не daemon. Факт поступает через command path.
5. Daemon получает CommitObserved (не определено — TD-R2, требует отдельного Reviewer
   и Auditor), проверяет SHA, parent и tree, затем **в одной транзакции** записывает
   `COMMIT_CREATED` и переводит Task в `COMPLETED`.

> **CommitIntent, CommitObserved, reconciliation, replay и idempotency не определены.**
> Конкретный протокол — TD-R2: требует отдельного Reviewer, Auditor и отрицательных
> тестов. До определения этого протокола шаги 4–5 являются целевым контрактом, а не
> действующим поведением.

Итак, условие коммита — «Run терминальный и Review/Audit PASS на этом tree hash»,
а не «Task COMPLETE». Этап 9 чек-листа является **следствием** наблюдённого
`COMMIT_CREATED`, а не его условием. Это исправление входного требования AG-007.

Если проверка SHA не прошла, фиксируется `COMMIT_BLOCKED`, и Task остаётся не
`COMPLETED`: провалившийся commit не является основанием завершить задачу.

## Git не знает про Fence

Git оперирует именами веток и SHA, а не Task.Fence, поэтому защита от устаревшего
writer выражается через CAS. Task.Fence является монотонной эпохой права записи;
Git SHA является объектной identity состояния ветки. Они принадлежат разным доменам
и не взаимозаменяемы. Canonical привязка CommitBinding (taskId, runId, attempt,
fence, branchRef, expectedParentSha, expectedTreeHash) — TD-R2, не определена.

При фиксации daemon проверяет одновременно: актуальный Task.Fence, текущий writer
Run, branch ref, expected parent SHA и reviewed tree hash. Commit object создаёт и
CAS `git update-ref` выполняет процесс Executor в назначенном worktree. Git-операция
атомарна: при несовпадении expected-old ref не меняется.

Daemon принимает только SHA с ожидаемым родителем и с деревом, равным
проверенному tree hash. Process lifecycle — создание, запуск и остановку процесса
Executor — обеспечивает внешний supervisor, а не daemon (ADR-0004, Process
Ownership). Факт commit поступает через command path, а не через опрос репозитория.

Git identity, signing policy и возможность Executor использовать signing key — TD-R2,
не определены. До определения этой политики runtime autonomous commit MUST оставаться
выключенным.

## Привязка к tree hash

Все проверки и вердикты Review/Audit привязаны к tree hash. Изменение дерева
после вердикта аннулирует вердикт: он относился к другому содержимому. Поэтому
и вердикт, и commit обязаны называть hash, а проверка hash обязана повторяться в
момент фиксации факта.

## Изоляция

Отдельный worktree и ветка на каждую Task — условие работы автокоммита. Общий
рабочий каталог несовместим с ним: две задачи в одном дереве не могут иметь
разные проверенные hash, и проверка теряет смысл.

Механизм создания worktree и branch naming определяется отдельной операционной
спецификацией с учётом RFC-0001. OWNER требуется только если схема меняет identity,
access boundary, retention semantics или пользовательские последствия. С учётом
того, что daemon процессов не запускает и файлов не создаёт, создателем worktree
и ветки может быть supervisor или executor. В MVP этого ADR механизм не входит.

## Gate self-modification

Изменение gate-файла (`Makefile`, CI, `.gitignore`, `AGENTS.md`, конфигурация
линтеров и тестов-механизмов) не является автоматически `OWNER_REQUIRED`.

`OWNER_REQUIRED` возникает, если изменение:
- создаёт или меняет норму класса R;
- изменяет trust boundary или права агентов;
- ослабляет fail-closed;
- меняет критерии приёмки;
- принимает новый риск.

Техническое изменение gate-файла требует независимого Reviewer. Для
security-sensitive gate — независимого Auditor и отрицательных тестов.
Executor изменённого gate не может единолично подтвердить прохождение этого же gate.

Точная политика Gate self-modification controls является TD-R2 и требует отдельного
Reviewer + Auditor + отрицательных тестов.

## Независимость ролей

REVIEWER и AUDITOR не совпадают с executor по устойчивому `agent_id`. Иначе
проверка того же кода тем же агентом не даёт независимого подтверждения. Это
требование совпадает с независимостью AUDITOR из ADR-0001.

## Commit Readiness

Commit Readiness — проекция того, готов ли commit к запуску. Она не хранится.
Значение и причина детерминированы для одного tree hash, как и Live Brief в ADR-0007.

**Commit Readiness (состояния до начала фиксации):**
- `NOT_READY` — базовые prerequisites не выполнены;
- `WAITING_REVIEW` — ожидание Review PASS;
- `WAITING_AUDIT` — ожидание Audit PASS (если требуется по StagePlan или TD-R2);
- `WAITING_OWNER` — требуется решение OWNER;
- `READY_TO_COMMIT` — все gates пройдены, commit может быть запущен.

**Commit Execution (состояния в процессе и после фиксации):**
- `NOT_STARTED` — фиксация не начата;
- `INTENT_RECORDED` — CommitIntent записан (TD-R2, не определено);
- `OBJECT_CREATED` — commit object создан;
- `REF_UPDATED` — ref обновлён через CAS;
- `RECONCILIATION_REQUIRED` — ref изменён, canonical подтверждения нет;
- `CONFIRMED` — COMMIT_CREATED записан, Task COMPLETED;
- `FAILED` — фиксация не завершена.

Причины `OWNER_REQUIRED`: MFA, пароль, OTP, визуальная проверка, принятие риска,
конфликт Review и Audit, исключение из политики.

Блокирующие причины, при которых commit запрещён и OWNER_REQUIRED не помогает:
несовпадение Fence, конфликт владения, неподтверждённая остановка writer,
открытая находка безопасности или аудита, непройденные инварианты.

## Startup Card и объяснение

Startup Card ORCHESTRATOR получает autonomy, checks и `commit.allowed`. Dashboard
показывает, **почему** commit не состоялся: конкретную блокирующую причину, а не
факт отсутствия коммита.

## Инварианты

- **AC-01 — только после prerequisites.** Пока не выполнены все пять этапов P0
  (daemon skeleton, Task aggregate и Task.Fence, Telepathy write path, executor
  lifecycle, supervisor stop proof) и шаг 8 ADR-0001, автокоммит Coord не действует.
  Проверка: не реализовано
- **AC-02 — только локальный commit.** Push, merge, release и deploy находятся
  вне области этого ADR и регулируются отдельной repository operations policy.
  Проверка: не реализовано
- **AC-03 — фиксация факта до завершения.** Условие коммита: Run терминальный и
  Review/Audit PASS на этом tree hash. `COMPLETED` наступает только после
  наблюдённого `COMMIT_CREATED`, в одной транзакции с его записью.
  Проверка: не реализовано
- **AC-04 — CAS ветки.** Commit выполняется только через `git update-ref` с
  ожидаемым родителем, выданным текущим Task.Fence; несовпадение оставляет ref
  неизменным.
  Проверка: не реализовано
- **AC-05 — SHA проверяется daemon.** Принимается только SHA с ожидаемым
  родителем и деревом, равным проверенному tree hash; иначе `COMMIT_BLOCKED` и
  Task не `COMPLETED`.
  Проверка: не реализовано
- **AC-06 — commit выполняет процесс Executor.** Git-операцию (создание tree,
  commit object, CAS update-ref) выполняет процесс Executor в назначенном worktree.
  Создание, запуск и остановка этого процесса принадлежат внешнему supervisor,
  а не daemon. Факт поступает через command path.
  Проверка: не реализовано
- **AC-07 — вердикты привязаны к tree hash.** Изменение дерева после вердикта
  аннулирует его.
  Проверка: не реализовано
- **AC-08 — изоляция на Task.** Отдельный worktree и ветка на Task; общий рабочий
  каталог несовместим с автокоммитом.
  Проверка: не реализовано
- **AC-09 — gate self-modification требует усиленной независимой проверки.**
  Изменение gate-файла не является автоматически `OWNER_REQUIRED`. `OWNER_REQUIRED`
  возникает, если изменение создаёт или меняет норму класса R, изменяет trust boundary
  или права, ослабляет fail-closed, меняет критерии приёмки или принимает новый риск.
  Техническое изменение gate-файла требует независимого Reviewer; security-sensitive
  gate — независимого Auditor и отрицательных тестов. Executor изменённого gate не
  может единолично подтвердить его прохождение.
  Проверка: не реализовано
- **AC-10 — независимость ролей.** REVIEWER и AUDITOR не совпадают с executor по
  устойчивому `agent_id`.
  Проверка: не реализовано
- **AC-11 — Readiness только проекция.** Commit Readiness не хранится; значение и
  причина детерминированы для одного tree hash.
  Проверка: не реализовано
- **AC-12 — причины объяснимы.** Для состояния без commit возвращается детерминированная
  primary reason и полный упорядоченный список blocking reasons. Одинаковый state даёт
  одинаковый порядок причин.
  Проверка: не реализовано
- **AC-13 — OWNER_REQUIRED не обходится.** Причины OWNER_REQUIRED и запрещённые
  условия различимы, и запрещённое условие не снимается решением OWNER.
  Проверка: не реализовано
- **AC-14 — commit protocol идемпотентен и восстанавливаем (TD-R2, не определено).**
  CommitIntent, CommitObserved и canonical receipt имеют устойчивые идентификаторы.
  Повтор CommitObserved для уже принятого commit возвращает прежний receipt. Если branch
  уже указывает на заявленный SHA, daemon проверяет binding и может завершить ранее
  начатую canonical фиксацию без повторного изменения Git ref. Неоднозначное
  расхождение ref, CommitIntent и canonical state переводит Task в
  `COMMIT_RECONCILIATION_REQUIRED` и запрещает новый commit до разрешения. Конкретный
  протокол — TD-R2, требует отдельного Reviewer + Auditor + отрицательных тестов.
  Проверка: не реализовано

## Последствия

- Автокоммит фиксирует только локальную запись; push, merge, release и deploy
  вне области этого ADR.
- Требуется отдельная операционная спецификация по созданию worktree и именованию веток.
- CommitIntent protocol, reconciliation, Git identity/signing — TD-R2, не определены.
  Autonomous local commit включится только после их принятия.

## Открытые вопросы владельцу

- Механизм создания worktree и правило именования веток на Task.
- Кто создаёт worktree: supervisor или executor.
- Политика срока жизни ветки задачи после коммита.
- Требуется ли автокоммит для задач без Review или без Audit по явной политике.

## Критерии соответствия

Реализация соответствует этому ADR, если:

- все пять этапов P0 и шаг 8 ADR-0001 выполнены (AC-01);
- commit изменяет только локальную ветку задачи (AC-02);
- Task переходит в COMPLETED в одной транзакции с записью COMMIT_CREATED, и
  условием служит терминальный Run с Review/Audit PASS (AC-03);
- commit выполняется через git update-ref с ожидаемым родителем (AC-04);
- daemon отклоняет SHA с иным родителем или иным деревом и фиксирует
  COMMIT_BLOCKED, оставляя Task не COMPLETED (AC-05);
- коммит создаёт executor, а факт фиксируется через command path (AC-06);
- вердикт, пришедший на другом tree hash, отвергается (AC-07);
- у каждой Task свой worktree и своя ветка (AC-08);
- изменение gate-файла требует независимого Reviewer; OWNER требуется только при
  изменении нормы R, trust boundary, fail-closed или критериев приёмки (AC-09);
- REVIEWER и AUDITOR отличаются от executor по устойчивому agent_id (AC-10);
- Commit Readiness вычисляется на запрос и не хранится в snapshot (AC-11);
- для состояния без commit возвращается primary reason и полный упорядоченный список
  blocking reasons (AC-12);
- запрещённое условие не снимается решением OWNER (AC-13).
