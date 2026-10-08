# RA-2 — независимость Reviewer и Auditor

- **Статус:** реализовано локально; commit не создан
- **Нормативные источники:** TASK-MODEL TM-23; ADR-0001 CM-03 и CM-07
- **Область:** canonical TaskID path в `store`; legacy `selectRole` не меняется

## Правило

Перед сохранением canonical state Coord проверяет независимость по стабильному
`roles.Selection.AgentID`, а не по session address или модели. При старте та же
проверка повторяется над сохранённой историей. Для Reviewer действует TM-23:
AgentID не должен совпадать ни с одним Executor AgentID, назначенным ранее или
одновременно в истории этой Task. Для Auditor CM-03 формулирует правило как
«стабильному `agent_id` и истории исполнителей»: проверка сравнивает Auditor
только с Executor Runs этой Task, в том числе завершёнными и предыдущих Attempts.
CM-03 не требует сравнения Auditor с Reviewer; такое ограничение здесь не
добавлено.

История назначения восстанавливается из `RoleHistory` и упорядоченных
Task-scoped DomainEvents. Первые selections привязываются к transfer markers.
RoleHistory может быть длиннее числа markers: `canonical.go` допускает append
selection с отдельным Task event без ownership transfer. Формат DomainEvent не
указывает прямо, какой элемент RoleHistory изменило событие. Для дополнительных
selection Coord использует консервативные границы: Executor получает самый
ранний возможный EventSeq после последнего transfer marker, Reviewer/Auditor —
самый поздний EventSeq истории Run, но не раньше event после marker. Это может
закрыть допустимое назначение при неоднозначной истории, но не должно пропустить
конфликт AgentID. Отдельный DomainEvent для точной привязки selection в этом
срезе не вводится. Будущая Executor assignment не делает недействительной
более раннюю Reviewer/Auditor selection, если её момент назначения однозначно
задан transfer marker; последующая проверка учитывает Executor по EventSeq.

Пустой AgentID у Reviewer/Auditor или у уже назначенного Executor закрывается
типизированной ошибкой `ErrNoIndependentReviewer` или
`ErrNoIndependentAuditor`. Эта проверка сама по себе не требует наличия хотя бы
одного Executor: допустимость Stage и порядок workflow остаются ответственностью
stage scheduler (см. `TestRoleIndependenceDoesNotRequireAnExecutorAssignment`).
Проверка выполняется до durable Save: отклонённая операция не меняет Task, Run,
Fence, Revision или event history.

UNKNOWN для отдельного решения OWNER: может ли бывший Reviewer этой Task позднее
стать её Executor. TM-23 и CM-03 задают независимость относительно истории
Executor на момент назначения Reviewer/Auditor, но не запрещают и не разрешают
Reviewer → Executor. RA-2 не вводит такого запрета.

## Ограничения этапа

Ошибки независимости типизированы, но durable blocker reason и проекция
Attention не реализованы. TASK-MODEL задаёт ожидаемые `NO_INDEPENDENT_REVIEWER`
и аналогичное поведение Auditor, но текущая модель не содержит сохраняемого
поля причины, а transition в `BLOCKED` выполняет stage scheduler вне RA-2.
Причина для Auditor, durable blocker event и отображение Attention требуют
отдельного решения OWNER вместе с Discussion BLOCKER.

RA-1 evidence refs пока не поддерживают Artifact metadata: разрешены только
канонические объекты той же Task, `ARTIFACT` отклоняется с ошибкой о
неопределённой metadata. Полная модель Artifact требует решения OWNER.

## Проверки

- startup и transition отвергают Reviewer/Auditor, совпадающих с Executor по
  стабильному AgentID, включая alias session, завершённые Runs и прошлые Attempts;
- одинаковая модель при разных AgentID разрешена;
- EventSeq соседа (`Executor` на N−1, проверяющий Run на N) входит в проверку;
- прежняя Reviewer assignment остаётся валидной, если тот же агент становится
  Executor только позже; более поздний Reviewer/Auditor с этим AgentID
  отвергается;
- отсутствие selection или стабильного AgentID закрывается fail-closed;
- отказ transition не меняет память или snapshot и не вызывает Save.
- одинаковый AgentID в разных Task не образует конфликт.

Проверки: `TestReviewerRejectsExecutorAgentIDAcrossHistory`,
`TestAuditorRejectsExecutorAgentIDAcrossTaskRuns`,
`TestIndependentAuditorMayUseSameModel`,
`TestNoIndependentReviewerOrAuditorFailsClosed`,
`TestCanonicalStartupRejectsRoleIdentityConflict`,
`TestCanonicalTransitionRejectsRoleIdentityConflict`,
`TestCanonicalRoleIndependenceFailsClosedWithoutStableAgentID`,
`TestReviewerOrAuditorMayOnlyUseAnAgentThatBecameExecutorLaterThanItsPastAssignment`,
`TestExecutorAtPreviousAdjacentEventSequenceIsNotIndependent`,
`TestSameAgentMayHoldRolesInDifferentTasks`,
`TestExecutorWithoutStableSelectionFailsClosedBeforeReviewer`,
`TestRoleHistoryExtraSelectionRequiresEventAfterTransfer`,
`TestRoleIndependenceDoesNotRequireAnExecutorAssignment`,
`TestRoleHistoryWithoutTransferMarkersUsesConservativeEventBounds` и
`TestRoleHistoryRejectsRoleChange`.

Отсутствие transfer marker отдельно отвергается startup-проверкой истории
событий (`TestCanonicalStartupRejectsTaskScopedRunWithoutTransferMarker`) и
проверкой transition transfer (`validateTaskOwnershipTransfers`); RA-2 не
дублирует эти инварианты.

Текущие тесты не доказывают production command authentication, Task Fence в
входном сообщении, stage scheduler, durable blocker/Attention, stop proof или
изоляцию Auditor в отдельном trust domain. Эти границы остаются в P0-3/P0-5 и
отдельных решениях проекта.
