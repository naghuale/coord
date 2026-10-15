# Human Action Playbook

Документ определяет порядок передачи человеку действий, которые:

1. уже разрешены принятыми решениями и gates;
2. не требуют нового решения OWNER;
3. не могут быть выполнены автоматическим агентом из-за технического
   ограничения, системного диалога, MFA, credential boundary или ограничения
   auto-mode;
4. могут быть описаны точной командой и проверяемым post-condition.

Human Action не является способом переложить на человека выбор следующего
технического шага.

---

## 1. Классификация

### OWNER_REQUIRED

Используется только когда требуется решение:

- новая норма;
- конфликт Accepted-норм;
- изменение trust/data/provider boundary;
- ослабление fail-closed;
- явно зарезервированное OWNER-действие;
- принятие риска.

### HUMAN_ACTION_REQUIRED

Используется, когда решение уже принято, но человек должен физически выполнить действие:

- MFA;
- ввод credential;
- системный диалог;
- перезапуск frontend;
- ручной merge, заблокированный auto-mode;
- предоставление protected runner;
- операция, требующая интерактивного доступа.

### BLOCKED_EXTERNAL

Используется, когда отсутствует внешний ресурс, но человек не может выполнить готовую команду прямо сейчас:

- каноническая OpenAPI отсутствует;
- Registry недоступен;
- provider не настроен;
- protected storage не создан;
- remote CI недоступен.

---

## 2. Условия допустимости Human Action

Coordinator может сформировать HUMAN_ACTION_REQUIRED только при выполнении всех условий:

```yaml
humanActionAdmission:
  decisionAlreadyMade: true
  ownerDecisionRequired: false
  automationAttemptedOrForbidden: true
  automationBlockerObserved: true
  exactCommandPrepared: true
  expectedResultDefined: true
  postCheckDefined: true
  rollbackOrFailureActionDefined: true
  independentWorkExhaustedOrContinuesInParallel: true
```

Если хотя бы одно поле равно `false`, запрос человеку отклоняется.

---

## 3. Запрещённые формулировки

Coordinator не должен писать:

- Что делать дальше?
- Смержите как-нибудь.
- Проверьте, всё ли хорошо.
- Запустите нужные команды.
- Нужно ваше решение.
- Жду, когда вы закончите.
- После этого сообщите результат.

Такие формулировки не определяют действие и перекладывают координацию на человека.

---

## 4. Обязательная карточка Human Action

```yaml
apiVersion: coord.dev/v1alpha1
kind: HumanActionRequest

metadata:
  actionId: <unique-id>
  projectId: coord
  createdAt: "<ISO-8601>"

classification:
  ownerRequired: false
  humanActionRequired: true
  reasonCode: <REASON_CODE>

subject:
  repository: <url>
  branch: <branch>
  currentHead: <sha>
  expectedInputSubjects: []

decision:
  alreadyAccepted: true
  decisionRefs: []
  reviewRefs: []
  auditRefs: []
  ciRefs: []

blocker:
  type: <BLOCKER_TYPE>
  observedEvidence: []
  automationRetryAllowed: false

action:
  workingDirectory: <absolute-path>
  commands: []
  expectedExitCodes: []
  expectedOutput: ""
  prohibitedCommands: []

postCheck:
  commands: []
  successConditions: []

failureHandling:
  forcePushAllowed: false
  destructiveRecoveryAllowed: false
  preserveOutput: true
  nextTransitionOnFailure: <TRANSITION>

coordinatorContinuation:
  eventOnSuccess: <EVENT>
  autonomousActions: []
  userFollowUpRequired: false
```

---

## 5. Пример — ручной merge

```yaml
apiVersion: coord.dev/v1alpha1
kind: HumanActionRequest

metadata:
  actionId: integrate-47-48-49-to-main
  projectId: coord

classification:
  ownerRequired: false
  humanActionRequired: true
  reasonCode: MANUAL_MERGE_REQUIRED

decision:
  alreadyAccepted: true
  decisionRefs:
    - INTEGRATE-47-48-49-R1

blocker:
  type: AUTO_MODE_ACTION_DENIED
  observedEvidence:
    - Merge Without Review rejected by auto-mode classifier
  automationRetryAllowed: false

action:
  workingDirectory: /Users/andreybabenko/Downloads/workspace/workspace-01/.scratch/tmp/main-integration

  commands:
    - |
      git merge --no-ff 11640e92d5d96f19a414d6141cb0c4e5e17919fb \
        -m "merge: integrate durable orchestration and coordinator progression"
    - git push origin main

  expectedExitCodes:
    - 0
    - 0

  expectedOutput:
    - merge commit created
    - main pushed without force

  prohibitedCommands:
    - git push --force
    - git push --force-with-lease
    - git reset --hard
    - git rebase
    - git clean

postCheck:
  commands:
    - git rev-parse HEAD
    - git ls-remote origin refs/heads/main
    - git merge-base --is-ancestor 68bd4b80 HEAD
    - git merge-base --is-ancestor 11640e92d5d96f19a414d6141cb0c4e5e17919fb HEAD

  successConditions:
    - local HEAD equals remote main SHA
    - p0-2-task subject is ancestor of main
    - durable orchestration subject is ancestor of main

failureHandling:
  forcePushAllowed: false
  destructiveRecoveryAllowed: false
  preserveOutput: true
  nextTransitionOnFailure: RECONCILE_MAIN_DIVERGENCE

coordinatorContinuation:
  eventOnSuccess: MAIN_INTEGRATION_PUSHED

  autonomousActions:
    - discover exact main pipeline
    - verify pipeline SHA
    - process Ubuntu and macOS CI
    - publish Completion Reports
    - close accepted Issues
    - update CURRENT
    - update OPEN-WORK
    - update Operations Ledger

  userFollowUpRequired: false
```

---

## 6. Короткое представление для человека

> Требуется ручное действие — **решение принимать не нужно**.  
> Действие уже разрешено: `<DECISION_REF>`.  
> Причина: `<OBSERVED_AUTOMATION_BLOCKER>`

**Каталог:**
```bash
<WORKING_DIRECTORY>
```

**Выполните:**
```bash
<EXACT_COMMANDS>
```

**Успешный результат:** `<EXPECTED_RESULT>`

**Не выполняйте:** force-push, reset, rebase, clean.

После выполнения Coordinator самостоятельно проверит результат, продолжит CI/gates, обновит состояние и закроет завершённые задачи.  
При ошибке — сохраните вывод команды.

---

## 7. Когда Human Action не нужен

До создания запроса Coordinator обязан проверить альтернативы:

- другой допущенный агент может выполнить действие;
- существует разрешённый automation path;
- действие можно выполнить через durable outbox worker;
- доступна replacement session;
- можно продолжить независимую работу;
- действие является read-only;
- блокер можно устранить техническим reconcile.

**Пример:**

```
Codex требует прямой команды пользователя
+ доступна OpenCode replacement session
→ назначить replacement
→ HUMAN_ACTION_REQUIRED запрещён
```

```
auto-mode запрещает merge всем агентским сессиям
→ точная команда подготовлена
→ HUMAN_ACTION_REQUIRED допустим
```

---

## 8. Coordinator продолжает работу независимо

Human Action не переводит весь Coordinator в состояние ожидания.

```yaml
coordinatorState:
  humanActionPending: true

  allowedIndependentWork:
    - prepare CI binding
    - prepare Completion Reports
    - validate Issue states
    - prepare operational projection update
    - verify branch ancestry
    - reconcile active sessions
```

Coordinator останавливает текущий переход только тогда, когда ни одно следующее действие невозможно без результата от человека.

**Правильный результат:**

```yaml
result: HUMAN_ACTION_REQUIRED
actionId: integrate-47-48-49-to-main
independentWorkPerformed:
  - Completion Report drafts prepared
  - exact pipeline matching rule prepared
  - Issue closure gates verified
nextExpectedEvent: MAIN_INTEGRATION_PUSHED
```

**Неправильный:** `Жду, когда вы выполните merge.`

---

## 9. Completion Gate

```go
func ValidateHumanActionRequest(
    request HumanActionRequest,
) error {
    if request.Classification.OwnerRequired {
        return ErrOwnerAndHumanBoundaryMixed
    }
    if !request.Decision.AlreadyAccepted {
        return ErrDecisionNotAccepted
    }
    if len(request.Action.Commands) == 0 {
        return ErrHumanActionCommandMissing
    }
    if len(request.PostCheck.Commands) == 0 {
        return ErrHumanActionPostCheckMissing
    }
    if request.FailureHandling.NextTransitionOnFailure == "" {
        return ErrFailureTransitionMissing
    }
    if request.CoordinatorContinuation.EventOnSuccess == "" {
        return ErrSuccessEventMissing
    }
    return nil
}
```

---

## 10. Reason codes

Допустимые:

```
MFA_REQUIRED
CREDENTIAL_INPUT_REQUIRED
PROTECTED_RUNNER_REQUIRED
SYSTEM_DIALOG_REQUIRED
PHYSICAL_FRONTEND_LAUNCH_REQUIRED
INFRASTRUCTURE_ACCESS_REQUIRED
MANUAL_MERGE_REQUIRED
MANUAL_PUSH_REQUIRED
MANUAL_SECRET_ROTATION_REQUIRED
```

Запрещённые:

```
UNKNOWN_NEXT_STEP
AGENT_DID_NOT_REPLY
REVIEW_PENDING
CI_PENDING
COORDINATOR_UNCERTAIN
```

---

## 11. Action Map

```yaml
events:
  AUTOMATION_ACTION_DENIED:
    allowedTransitions:
      - TRY_ALLOWED_REPLACEMENT
      - PREPARE_HUMAN_ACTION
      - BLOCKED_EXTERNAL

    requiredActions:
      - verify_decision_already_accepted
      - verify_no_allowed_automation_path
      - prepare_exact_command
      - prepare_post_check
      - prepare_failure_transition
      - perform_independent_work

    forbiddenActions:
      - ask_owner_to_choose_next_step
      - request_unstructured_manual_help
      - passive_wait
      - promise_future_notification

    expectedResults:
      - HUMAN_ACTION_READY
      - REPLACEMENT_AUTOMATION_SELECTED
      - BLOCKED_EXTERNAL
```

---

## 12. Acceptance criteria для issue #47

```yaml
acceptance:
  humanAction:
    decisionAlreadyAcceptedRequired: true
    exactCommandRequired: true
    postCheckRequired: true
    failureTransitionRequired: true
    coordinatorContinuationRequired: true
    vagueRequestRejected: true
    ownerBoundaryNotImplied: true
```

Обязательные сценарии:

| Сценарий | Ожидаемый результат |
|---|---|
| auto-mode denied merge | structured HumanActionRequest, ownerRequired=false |
| replacement agent available | no Human Action, assign replacement |
| command missing | request invalid (ErrHumanActionCommandMissing) |
| post-check missing | request invalid (ErrHumanActionPostCheckMissing) |
| decision not accepted | cannot classify as Human Action |
| action succeeded | Coordinator resumes autonomously |
| action failed | reconcile transition, no force-push |
