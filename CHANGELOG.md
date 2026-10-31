# История изменений

## 2026-10-31 — AND-15 review recovery: runbook backup wording and NQ-6 act 2 (Refs AND-15)

- v5→v6 runbook: the first mutating commit publishes schema 6 through `atomicfile.Write`; the binary does not create a backup. Operator backup remains precondition 4. (документация)
- Two-act checklist: delegation enablement happens only as part of act 2 (recorded with it); schema 6 does not imply it. (документация)

## 2026-10-26 — AND-15 technical follow-ups (Refs AND-15 / #93b)

- ADR-0010 header records implementation IMPLEMENTED, production DISABLED (`CoordinatorDelegationEnabled=false`), cutover NOT_PERFORMED. Accepted ADR body and norms are unchanged. (документация)
- ApplyInboxCommand comment and command-boundary docs state that Owner enrollment does not disable the Task seam; OwnerCredentials and CoordinatorDelegations still fail closed. Code behaviour is unchanged. (документация)
- COMPONENT-STATUS `updatedFromCommit` is `757d6a1822dfff62546ea504ceb638fde88df8e6`; `coordinator.loss.confirm` is implemented behind the disable const. (документация)
- OWNER cutover NQ-2/3/4/6 recorded append-only; v5→v6 runbook and two-act cutover checklist added. Schema advance is not cutover. Const gates and S0Exit are not declared. (документация)

## 2026-10-26 — Pre-restart loss must reuse restart visibility on seam and reload (Refs #93b)

- A pre-restart loss routed to the acting restarted primary is accepted on the typed seam and on snapshot reload only when the handoff visibility is the restart receipt. A fresh unrelated visibility for the same primary is rejected. Live A-2 (restart digest) and A-3 (fresh post-restart capture) stay accepted. (`TestRecovery8_P1PreRestartLossMustReuseRestartVisibility`)

## 2026-10-25 — Fresh visibility for post-restart coordinator loss (Refs #93b)

- OWNER A-3: losses recorded after the active primary restart require a fresh visibility receipt for that exact primary. The capture consumes it once; siblings of the same loss retain it. Losses recorded before the restart retain the A-2 path without new visibility. Live, typed callback and snapshot replay require both the grant's history order and strictly earlier canonical revisions, and preserve the route to the acting primary. (`TestRecovery7_A3LiveAndReload`, `TestRecovery7_A3SeamAndReload`, `TestRecovery7_A3EqualRevisionDoesNotProveEarlierLoss`, `TestRecovery7_A3PromotedRestartRevisionCannotReorderLossHistory`)
- Routing a pre-restart loss does not erase a fresh sibling context, while a new loss ends it and permits recapture only with another fresh receipt. (`TestRecovery7_A3ContextSurvivesOlderLossRoutingButNotNewLoss`, `TestRecovery7_A3AnyLaterLossEndsFreshSiblingContext`)

## 2026-10-25 — Delegation recovery boundary evidence (Refs #93b)

- Reload rejects handoff and primary restart after OWNER revocation, and capture by a barred coordinator even when its exact triple is on the successor list. Valid revoked snapshots, exact receipt retry and a new grant to the same primary still work. (`TestRecovery6_RevokedGrantCannotRecoverThroughFreshVisibilityOnReload`, `TestRecovery6_RevokeRejectsAuthorityHistory`, `TestRecovery6_ReloadCannotReenterBarredListedPrimary`, `TestRecovery6_RevokePreservesExactRetryAndNewGrantAfterReload`)
- A-2 receipt, callback-seam and restart-context boundaries have isolated positive/negative controls; reload session tests cover lost and barred sessions that were never used for a prior primary restart. (`TestRecovery6_A2CommitSeamChecksEachBoundary`, `TestRecovery6_ReloadRestartContextEndsAtPlannedOrLossCapture`, `TestRecovery6_ReloadRejectsLostOrBarredSessionWithoutPriorRestart`)

## 2026-10-25 — #93b recovery: bind OWNER loss and derive delegation state (Refs #93b / #102)

- Подписанный `coordinator.loss.confirm` теперь несёт точный отсортированный набор затрагиваемых assignment IDs. Receipt, loss events и grant history записываются одним canonical commit и связываются по delegation ID/generation, coordinator triple, assignment set и revision; generic writers не могут подделать или повторно использовать OWNER loss receipt. (`TestRecoveryA93B1_LossReceiptAndEventsShareExactCommit`, `TestRecoveryA93B1_SignedAssignmentSetMustMatchLossEffect`, `TestRecoveryA93B1_GenericWriterCannotReuseOwnerLossReceipt`, `TestRecoveryA93B1_InboxWriterCannotAppendOwnerCredentialLoss`, `TestSnapshotReloadRejectsOwnerCredentialLossWithoutReceipt`)
- Snapshot load сверяет acting coordinator, barred principals, revoke, visibility consumption, restart consumption, grant receipts и assignment bindings с append-only history и OwnerCredential receipts. (`TestRecoveryA93B2_SnapshotControlStateIsDerivedFromHistory`, `TestRecoveryA93B2_SnapshotRejectsDelegationEnvelopeDrift`, `TestRecoveryA93B1_SnapshotRejectsReceiptContextSubstitution`)
- Закреплены exact acting principal, ordered eligible successors, зарегистрированная primary-restart сессия и новое поколение, immutable grant на typed handoff seam, а также отсутствие partial publish при Save failure. Неизвестное поле не может продлить grant при `loss.confirm`. Delegation остаётся выключенной в production. (`TestRecoveryA93B3_DelegatedMutationRequiresExactActingPrincipal`, `TestRecoveryA93B3_HandoffUsesFirstEligibleSuccessor`, `TestRecoveryA93B3_PrimaryRestartRejectsWhileSuccessorStillActing`, `TestF11_HandoffSeamFreezesGrantScopeValidityAndSuccessors`, `TestRecoveryA93B3_PrimaryRestartRequiresRegisteredFreshSession`, `TestRecoveryA93B3_PrimaryRestartGenerationMustAdvanceAcrossLosses`, `TestRecoveryA93B3_PrimaryRestartGenerationMustExceedPriorPrimaryAfterSuccessorLoss`, `TestRecoveryA93B3_LossConfirmRejectsUnknownExpiryWithoutWrite`, `TestRecoveryA93B3_DelegationSaveFailurePublishesNothing`, `TestCoordinatorDelegationHasCompileTimeGate`)

## 2026-10-25 — ACTION-MAP.yaml identity and HEAD-tree publication (Refs #97)

- Architecture checker resolves the Action Map registry row only as `action-map-yaml`. Historical `action-map` (`ACTION-MAP.md`) is never a fallback. Empty path on the YAML row is `EMPTY_PATH` even when the historical row is accepted and committed. (`TestR8_F1_YAMLIdentityNotHistoricalActionMap`)
- Publication requires a regular-file blob (mode `100644`/`100755`) in the checked `HEAD` tree (`git ls-tree`). Staged-only, untracked, HEAD symlink (`120000`), and uncommitted symlink-to-committed-target paths are `FILE_NOT_TRACKED`. A HEAD blob deleted from the worktree is `FILE_MISSING`. The evaluated file is the resolved target: a committed symlink to an untracked regular file is `FILE_NOT_TRACKED`; a committed symlink to a committed regular file publishes. A dangling symlink is classified by its lexical Readlink target: inside the repository `FILE_MISSING`, outside `SYMLINK_TARGET_OUTSIDE_REPOSITORY`. (`TestR8_F2_PublicationIsHEADTreeNotIndex`, `TestA97_R1_DanglingSymlinkToMissingTargetFails`, `TestA97_R2_DanglingSymlinkOutsideFails`)

## 2026-10-24 — Bounded coordinator delegation behind disable const (Refs #93b / #102)

- Schema v6: `CoordinatorDelegations` aggregate; v3/v4/v5 load without writes; first mutating commit publishes 6; v7+ fails closed. `COORDINATOR_LOST` gains omitempty `actorSource` (`AGENT_SESSION` | `OWNER_CREDENTIAL`); legacy events load as `AGENT_SESSION`. (`TestSchemaFiveLoadsReadOnlyAndNextCommitPublishesSix`, `TestSchemaSevenFailsClosed`, `TestSnapshotReloadRejectsOwnerCredentialLossWithoutReceipt`)
- OwnerCredential kinds: `coordinator.delegation.grant`, `coordinator.delegation.revoke`, `coordinator.loss.confirm`, `coordinator.visibility.confirm` (transitional, `authorityCreating=false`). Exact-retry receipts unchanged. (`TestDelegationGrantRevokeExactRetryAndCutoverInert`, `TestLossConfirmBarsStillRegisteredAndRequiresOpenWork`, `TestVisibilityConfirmRejectsOffListAndDoesNotChangeScope`)
- Compile-time `CoordinatorDelegationEnabled=false` (G95-1 pattern). With const false, grant/revoke/loss/visibility reject with no write; existing #90/#93a paths stay byte-identical. Harness enable lives only in `_test.go`. `cutoverAssignmentIds` storable via OWNER grant only and inert while disabled. (`TestCoordinatorDelegationRemainsDisabledByDefault`, `TestDelegationCommandsRejectedWithoutWriteWhenDisabled`, `TestCoordinatorDelegationHasCompileTimeGate`)
- Production merge does not enable delegation or perform cutover. (документация)
- ADR-0010 §4–§11 enforcement behind harness: binding gate, action allowlist, stricter delegated VOID/EXPIRE, ordered successor handoff with visibility skips/exhaustion, primaryRestart, loss.confirm mustNot set, OWNER_CREDENTIAL loss receipt replay. All 46 DESIGN-NOTE scenarios named and green. (`TestDelegationPositiveControlGrantHandoffSucceeds`, `TestDelegationHandoffRejectsSessionOutsideSuccessorList`, `TestSnapshotReloadAcceptsOwnerCredentialLossWithMatchingReceipt`, `TestForgedOwnerCredentialActorWithoutReceiptRejected`)

## 2026-10-24 — COMPONENT-STATUS axes, NV-4 and D24-F3 (Refs #97)

- `COMPONENT-STATUS.yaml` gains the four OWNER axes (`code` / `connection` / `qualification` / `production`), a critical-path `requirements:` bootstrap (ADR-0001, ADR-0004, R152–R154, #90/#93/#95/#96) and NV-4 `actionMapReferenceClassifications`. Subordinate recovery docs/templates are aligned with ACTION-MAP-INTERIM-PROCEDURE-01 without weakening fail-closed. The D24-F3 recovery fixture retargets the `actionMap` source to `AGENTS.md` and keeps `docs/operations/ACTION-MAP.md` only in `rejectedAsCurrentAuthority`. (`TestLiveRegistryPasses`, `TestAuthoritativeActionMapWhileProposedKills`, `TestUnclassifiedActionMapHitKills`, `TestProposedProductionAuthorityKills`, `TestConnectedWithoutEntrypointKills`; recovery: `tests/test_recovery_checks.sh`)

## 2026-10-24 — Go architecture sync checker with golden fixtures (Refs #97)

- `coord-governance check-architecture-sync` and `internal/architecturecheck` become the blocking architecture sync authority. Symbols and tests are resolved through `go/parser` and `go/ast`. Golden fixtures under `internal/architecturecheck/testdata/golden` encode the Python checker findings; Go tests compare against those fixtures and must not invoke Python. Makefile and `architecture-sync.yml` block on Go; the Python scripts remain as evidence only. (`TestLiveRegistryPasses`, `TestGoldenCases`, `TestMissingSymbolKills`, `TestMissingTestKills`, `TestPartialRequiresMissing`, `TestProposedMustBeNonNormative`, `TestConnectedWithoutEntrypointKills`, `TestActiveWithoutRuntimeKills`, `TestProposedProductionAuthorityKills`, `TestMakefileAndWorkflowUseGoBlocking`, `TestASTIndexFindsTypeFuncMethodAndTest`)

## 2026-10-24 — Document registry location and historical provenance (Refs #97)

- Document registry gains `LOCAL` / `TRACKED_IN_OTHER_BRANCH` location, `superseded` status, historical provenance, and `LOCAL` vs `CI_STRICT` modes. Active or proposed LOCAL files are required even when optional. Ops CURRENT/OPEN-WORK move to the allowlisted external location. Historical Action Map readWhen is `HISTORICAL_INVESTIGATION` with `normativeAuthority: none`. (`TestActiveLocalMissingRejectedEvenWhenOptional`, `TestProposedMissingRejected`, `TestUnknownLocationFailsClosed`, `TestTrackedOutsideAllowlistRejected`, `TestExternalRefUnavailableCIStrictFails`, `TestExternalRefUnavailableLocalWarns`, `TestTrackedExternalAcceptedWithEvidence`, `TestEvidencePathInsideRepositoryRejected`, `TestSupersededRequiresSupersededBy`, `TestHistoricalWithoutProvenanceRejected`, `TestHistoricalPurposeProvenanceAccepted`, `TestHistoricalStructuredProvenanceAccepted`)

## 2026-10-24 — Authenticated agent result ingress (Refs #95)

- Added Telepathy signed-envelope intake resolved against active canonical AgentSession keys. Version-2 agent RESULT binds assignment, issuer, coordinator generation, task revision, Run, fence, attempt and subject; v1 remains available only to the legacy path and is rejected by authenticated ingress. (`TestSignedClientIncomingAuthenticatedCarriesResolvedSessionEvidence`, `TestAgentResultV2AcceptanceCommitsTaskInboxOutboxAndAssignmentAtomically`)
- ApplyAgentCommand rechecks current session and assignment, exact task-message correlation, role/stage, subject, fence and issuer. Accepted Task event, Inbox receipt, response outbox and assignment completion share one canonical save. Exact redelivery returns the durable response; altered signed bytes or identity are rejected. NO_ACCEPTANCE verdicts commit a dedupe receipt only; unknown and forbidden verdicts fail closed. (`TestAgentResultV2NegativeCasesDoNotWrite`, `TestAgentResultV2RejectsWithoutAssignmentOrOnCommitFailure`, `TestAgentResultRejectsVoidExpiredAndStaleCoordinatorWithoutWrite`, `TestAgentResultRejectsPayloadMutationAfterSigningWithoutWrite`)
- Production composition remains on the pre-#95 `Receive`/configured-peer path, including legacy v1 RESULT processing; coordd does not configure AgentSession ingress while the compile-time gate is false. The legacy path explicitly rejects v2 without canonical changes; the authenticated handler remains available to the explicit test harness and rejects v1 RESULT there. Schema v5 adds only agent-command Inbox bindings; v3/v4 remain readable, and older writers may not downgrade v5. (`TestProductionCompositionDoesNotConfigureAgentSessionIngress`, `TestRunWithReadinessKeepsLegacyReceiverWhenAuthenticatedIngressIsOff`, `TestRunWithReadinessProcessesLegacyV1ResultAsBefore`, `TestLegacyPeerReceiveRejectsV2WithoutApplyingCanonicalCommand`, `TestAuthenticatedIngressRejectsLegacyV1Result`, `TestAuthenticatedAgentResultRunsOnlyThroughExplicitHarness`, `TestSchemaFourMigrationKeepsLegacyIngressAndDoesNotEnableAgentIngress`, `TestSchemaFourLoadsReadOnlyAndNextCommitPublishesSix`, `TestSchemaFourAssignmentLedgerFreezeAndStatusRemainReadable`)

## 2026-10-24 — ADR-0010 accepted (Refs #102)

- OWNER принял ADR-0010 решением ADR-0010-ACCEPTANCE на ревизии e3b5908. Статус Accepted. Реализация NOT_IMPLEMENTED, production DISABLED, делегация не действует, cutover не разрешён. (документация)

## 2026-10-24 — ADR-0010 OWNER loss confirm boundary (Refs #102)

- Путь (b) `coordinator.loss.confirm` получил явный список mustNot, запрет второго действующего координатора при ещё зарегистрированной сессии и вид актора `AGENT_SESSION` или `OWNER_CREDENTIAL`. Расширение схемы названо для #93b и этим коммитом не добавляется. Статус остаётся Proposed. (документация)

## 2026-10-24 — ADR-0010 loss paths and exact retry (Refs #102)

- Смена поколения не наследует делегацию. Точные тройки и одна запись перезапуска — уже названное применение выдачи. Потерю записывает либо допустимая AgentSession по #90, либо будущая команда OWNER `coordinator.loss.confirm` в #93b. Точный повтор подписанной команды возвращает прежний receipt. Статус остаётся Proposed. (документация)

## 2026-10-24 — ADR-0010 closed draft questions (Refs #102)

- Видимость преемника требуется при выдаче и при захвате. Включение идёт по явному списку OWNER. У первичного координатора одна запись перезапуска по stableAgentId, и он не наблюдает собственную потерю. Делегированные EXPIRE и VOID строже правил хранилища #90 для команд OWNER. Принятие ADR делегацию не включает. Открытых вопросов модели не осталось. (документация)

## 2026-10-24 — Proposed ADR-0010 bounded coordinator delegation (Refs #102)

- Добавлен Proposed ADR-0010 и запись реестра со статусом proposed. Модель D и упорядоченный список преемников записаны как черновик по решению OWNER. Принятые ADR не изменены. Код, схема и действующий ручной процесс координатора не изменены. (документация)

## 2026-10-24 — Disable production orchestration pending acceptance (Refs #96)

- Production coordd composes a read-only disabled workflow facade with `orchestrationEnabled = false`. Recovery/tick/adapter/projection execution stays off, with no environment, config or flag enablement. Production fixture requests are visibly rejected before locks/transport; explicit execution exists only in the `_test.go` harness. Both ADR acceptance and a ready authenticated single write path are required before a separate enablement change (`TestOrchestrationNoProductionEnablementConfig`, `TestOrchestrationExplicitTestHarnessStillExecutes`).
- Preserved orchestration Runs retain OwnsRun fencing against legacy messages/retries; ordinary Runs remain outside that fence. Normal startup/restart preserves canonical bytes and events, Task/Assignment reads remain available, and unsupported schemas fail closed (`TestOrchestrationDisabledTickAndRecoveryAreReadOnly`, `TestOrchestrationDisabledKeepsRunFencesAndOrdinaryRuns`, `TestOrchestrationNormalStartAndRestartPreserveSnapshot`, `TestOrchestrationDisabledPreservesTaskAndLedgerReads`, `TestOrchestrationDisabledStillRejectsUnsupportedSchema`).
- Architecture/status/roadmap and Proposed ADR notes now describe actual disabled composition, retained data, and the two reopening prerequisites. Production no longer publishes the workflow revision bundle; its positive contract remains in `TestExplicitOrchestrationHarnessPublishesCanonicalRevisionBundle`, an intentional production-expectation inversion. No schema, canonical slice, writer or accepted ADR status change. (документация)

## 2026-10-24 — Action Map phrases are not authority (документация)

- Запрет вопроса «что делать дальше» записан как правило этого `AGENTS.md`. `forbiddenActions: ask_owner_what_to_do_next` в proposed `ACTION-MAP.yaml` — только ненормативное зеркало. Правило R146 стоит само по себе; `SESSION_REPLACED` — имя ненормативного события в proposed `ACTION-MAP.yaml`. Документы выбираются по Document Registry и промежуточному порядку. Ссылка на Action Map в списке источников перехода явно будущая: она действует, только если карта позже будет отдельно принята как нормативный документ. Сейчас принятой карты нет. Новых переходов и новой нормы нет. (документация)

## 2026-10-24 — Interim action-map procedure (документация)

- Пока принятая карта действий не опубликована как отслеживаемый авторитетный артефакт, событие сверяется с `AGENTS.md`, Coordinator State Machine, Document Registry и принятыми ADR/R. Отсутствующий `ACTION-MAP.md` не читается и не дополняется. `ACTION-MAP.yaml` остаётся ненормативным checklist и не задаёт production-переход. Новых переходов нет. (документация)

## 2026-10-24 — Documentation reconciliation with accepted reality (Refs #89, #90, #93, #95, #96)

- README больше не называет снимок 2026-10-07 текущей рабочей версией. Обратное направление ролей указывает на уже принятое правило `EXECUTOR_ROLE_HISTORY_CONFLICT` (ADR-0001 CM-02a; `TestFormerReviewerOrAuditorCannotLaterBecomeExecutor`). (документация)
- Порядок реализации в ADR-0001, ROADMAP и TARGET-ARCHITECTURE записывает уже сделанную Shadow Validation, приёмку P0-3 ACCEPTED_WITH_LIMITATIONS и пять измерений NOT_EVALUATED (#89). Assignment Ledger записан как влитый (PR #98, `0c6eaf4`) и неактивный: cutover BLOCKED_ON_WRITE_AUTHORITY (#93, #95). (документация)
- Заголовок ORCHESTRATOR и формулировка «в роли Orchestrator» в ROLE_PROMPTS сохранены и помечены как историческая терминология (D9). Имена COORDINATOR и ORCHESTRATOR не сливаются. (документация)
- Durable orchestration остаётся Proposed. OWNER 2026-10-24 решил отключить её до принятия ADR (#96); до посадки #96 fixture-only runtime всё ещё подключён в coordd. Пометка не меняет код и не принимает ADR. (документация)
- ACTION-MAP.yaml остаётся proposed и ненормативным. Это самостоятельное восстановленное предложение, а не проекция ACTION-MAP.md. (документация)
- В реестре отсутствующие legacy-записи action-map, CI и GitLab, а также RFC-0002 и AGENT-BOOTSTRAP-TEMPLATE, стали historical. Файлы не восстановлены. `currentOperationalAuthority: false` — следствие статуса historical. CURRENT.yaml и OPEN-WORK.yaml остаются active и optional; они лежат в ветке ops/coord/project. (документация)

## 2026-10-24 — Assignment loss and recovery separation (Refs #90)

- Reject self-interested loss observations, including a whole loss batch when an
  affected assignment targets its observer; reject recovery to target/open loss
  observer. No schema, role, credential or authority slice is added.
  (`TestAssignmentLossRejectsCoordinatorAndTarget`,
  `TestAssignmentLossBatchRejectsAnyAffectedTarget`,
  `TestAssignmentRecoveryRejectsTargetAndOpenLossObserver`)
- Derive the open loss observer from existing event history; enforce exclusions
  on replay/load and retain recovery to a distinct successor.
  (`TestAssignmentReplayRejectsSelfInterestedRecovery`,
  `TestAssignmentRecoveryUsesOnlyOpenLossObserver`,
  `TestAssignmentLossRecoveryWithRotatedCoordinator`)
- Record the separate #93 privilege-gate test obligation without implementing it.
  (документация)

## 2026-10-23 — Assignment coordinator recovery authority correction (Refs #90)

- Reject rotated/revoked predecessor handoffs before recorded coordinator loss,
  and require a current active loss observer separate from the lost subject.
  Recovery requires recorded loss, active successor and verified evidence.
  (`TestAssignmentHandoffRequiresCurrentPredecessor`,
  `TestAssignmentLossRequiresCurrentObserver`,
  `TestAssignmentLossRecoveryWithRotatedCoordinator`)
- Preflight and work-started validate the expected immutable issuer.
  (`TestAssignmentPreflightAndWorkStartedRequireIssuer`)
- Prove and document frozen-mode transitions for all assignment states, including
  existing OFFERED/ACKED activation without automatic cancellation.
  (`TestAssignmentFrozenStateTable`)
- Narrow stale-restart coverage to preflight simulation, correct loss/handoff test
  references, and clarify existing ACTIVATE/WORK_STARTED in LEDGER_FROZEN.
  (документация)

## 2026-10-23 — Assignment Ledger MVP on canonical JSON (Refs #90)

- Added exact assignment authority/history inside the existing canonical snapshot,
  including process assignments without Tasks, explicit ACK/ACTIVE, immutable
  IDs/clocks, terminal refusal and observed coordinator loss/evidence-bound handoff.
  (`TestAssignmentNoWorkBeforeActiveAndAckInsufficient`,
  `TestAssignmentTerminalNeverRevives`,
  `TestAssignmentOe4StaleLateDeliveryAfterRestartDoesNoWork`,
  `TestAssignmentCoordinatorLossAndVerifiedHandoff`)
- Result identity/TTL/session/coordinator checks and standalone or typed Run
  completion commit atomically; transport delivery cannot change authority.
  (`TestAssignmentDuplicateResultAcceptedOnlyOnceAtomically`,
  `TestAssignmentExactIdentityGenerationAndSubject`,
  `TestAssignmentTypedResultGateAtomicWithTask`,
  `TestAssignmentTransportCommandCannotChangeAuthority`)
- Read-only `coord assignment status <id> --format json|yaml` distinguishes
  unknown/unavailable/corrupt state without daemon startup or store writes.
  (`TestAssignmentStatusReadOnlyAndFailClosed`,
  `TestAssignmentStatusUnknownUnavailableCorruptAreDistinct`,
  `TestAssignmentStatusYAMLAndCLIBypassDaemon`)
- `LEDGER_FROZEN` launch mode preserves ledger data and existing assignment
  processing while rejecting new offers. Old snapshots stay compatible;
  pre-ledger strict decoders refuse the new field without stripping data.
  (`TestAssignmentLedgerRollbackFrozenMode`,
  `TestAssignmentOldSnapshotCompatibleAndHistoryAppendOnly`,
  `TestAssignmentStatusLaunchRollbackPreservesLedger`)
- Added cutover/rollback and WP-R153-04/06 coverage documents; operational cutover
  remains BLOCKED_ON_WRITE_AUTHORITY. No full R153/receiver enforcement or new
  transport/credential permission is claimed. (документация)

## 2026-10-23 — coord: Telepathy server discovery without a hardcoded 0.8.0 path (Refs #31)

- `cmd/coord` no longer looks only for the absent Codex-cache 0.8.0 path. An
  explicit `--server` or `TELEPATHY_SERVER` still wins; otherwise the Claude and
  Codex plugin caches are scanned. Copies marked orphaned by the plugin manager
  and versions outside the client contract are excluded. Exactly one remaining
  installation is selected; two or more, even byte-identical copies in different
  host caches, fail with `AMBIGUOUS_TELEPATHY_INSTALLATION` and require an
  explicit path; the newest version is never chosen automatically. Only the
  default Claude and Codex plugin caches are scanned: OpenCode packages,
  marketplace distribution copies and non-default `CLAUDE_CONFIG_DIR` or
  `CODEX_HOME` roots need an explicit path, and a single installation found is
  unique only within those two caches.
  (`TestDiscoverSeveralVersionsAreAmbiguousNotNewest`,
  `TestDiscoverSameVersionDifferentBytesAmbiguous`,
  `TestDiscoverIdenticalCopiesAcrossHostsAreAmbiguous`,
  `TestDiscoverExcludesOrphanedAndUnsupportedCopies`,
  `TestDiscoverSingleInstallationSelected`, `TestDiscoverNoInstallationFails`,
  `TestDiscoverOnlyUnsupportedFails`, `TestDiscoverExplicitAndEnvWinOverScan`)
- Launch diagnostics list every candidate with path, declared version, SHA-256
  and exclusion reason, and the selected path, source and SHA-256.
  (`TestReportTelepathySelectionListsCandidatesAndChoice`)
- Not included: pinned active/fallback policy, declaring 0.9.0 the standard,
  rollback checks, plugin-cache changes (#31 follow-up). (документация)

## 2026-10-23 — coordd shadow pilot: observed Telepathy server version in provenance (PV2-F1, Refs #88)

- `telepathy.Process` records the server version returned at the MCP `initialize`
  handshake (`ServerVersion`); the accepted client contract `0.8.0`, `0.9.0` is
  defined once (`AcceptedServerVersions`). (`TestRPCChecksVersionAndToolFailure`,
  `TestAcceptedServerVersionsContract`)
- Shadow-pilot provenance replaces the declared range `mcpClientVersion` with
  `acceptedClientContract`, `actualServerVersion` (taken from the live handshake,
  never from a constant or a declared value) and `mcpProtocolVersion`; a run
  without an observed supported version is refused before dispatch.
  (`TestShadowProvenanceRecordsObservedServerVersion`,
  `TestShadowUnsupportedServerVersionRejected`,
  `TestShadowDeclaredVersionCannotSubstituteObserved`)
- Provenance is validated before sealing: missing, empty, declared-range or
  unsupported `actualServerVersion`, a changed contract and the legacy
  `mcpClientVersion` field are refused, so v1 evidence cannot pass as observed.
  (`TestShadowProvenanceValidatorRejectsMissingDeclaredAndLegacyVersions`)
- Evidence from liveShadowPilot_v2/v2r keeps its OWNER-accepted one-line v1
  contract; it is not re-labelled. The scratch dossier validator contract v2 and
  its cases live in the Operations Ledger. (документация)

## 2026-10-22 — coordd shadow pilot: Telepathy 0.9.0 and explicit refusals (Refs #70)

- The shared Telepathy MCP client (`telepathy.Start`) now accepts server versions
  `0.8.0` and `0.9.0`; the `telepathy` name and MCP protocol `2024-11-05` checks
  are unchanged. This supersedes "the existing version-pinned MCP client remains
  unchanged" in the 2026-10-20 entry. (`TestRPCChecksVersionAndToolFailure`)
- `decodeShadowResult` reports an explicit agent `ShadowRefusal` with the agent's
  own field and reason instead of `unknown field "kind"`; a refusal without a field
  and any other input still fail closed through the strict decoder.
  (`TestShadowDecoderHandlesShadowRefusal`)
- `--telepathy-server` help and provenance client-version metadata reflect the
  accepted versions. (не проверено)
- Found by live shadow pilot v1; merged in PR #85 (`e9f3dbe`) after independent
  Review and Audit (`AUDIT_ACCEPTED` on `e72d7ee`). Live pilot v2 and P0-3
  admission evaluation remain pending. Known test gaps (mutants on protocol
  check, refusal classification, exact version match) are tracked separately.
  (документация)

## 2026-10-20 — coordd read-only shadow pilot entrypoint (WP-SV-07, Refs #70)

- Added exact `shadow-pilot run` CLI mode, isolated from daemon/store/lock startup.
  Typed Telepathy MCP envelopes bind all assignment identities before completion;
  nil/fake adapters, mutable subjects, malformed results and identity mismatches
  fail closed. The existing version-pinned MCP client remains unchanged.
  (`TestShadowEveryIdentityBindingHasTypedRefusal`,
  `TestShadowLiveModeRejectsNilFakeAndNilMCP`)
- Added observed startup/dispatch evidence, sealed artifact manifests and integrity
  digests, ten explicit comparison dimensions and a validated pilot report whose
  admission remains `NOT_EVALUATED`. No production authority is granted.
  (`TestShadowMissingDimensionsAndUnsealedEvidenceCannotReachReport`,
  `TestShadowRuntimeBreachesPreventEvidenceSealing`)
- Added CLI, stdio MCP protocol-fixture, refusal and authority-boundary tests.
  Independent Review/Audit and actual c03 live-run acceptance remain pending.
  (`TestShadowCLIUsesExistingMCPClientEndToEnd`)

## 2026-10-18 — Coordinator: execute authorized ready work

- Added OWNER-requested `COORDINATOR-ACT-ON-READY-WORK-R1` to `AGENTS.md`:
  same-turn selection, tracking, contract/assignment and eligible Executor
  dispatch; recommendation-only completion and permission-offers are forbidden
  for already-authorized actions. Unavailable operations require a complete
  payload and exact blocker while independent work continues. (документация)
- Added the local workflow transition table to
  `docs/operations/COORDINATOR-STATE-MACHINE.md` and the compact obligation to
  the Coordinator recovery prompt. Existing priority, admission, R146 evidence,
  Review/Audit and OWNER/security boundaries remain mandatory. This is a
  process instruction change; no runtime dispatcher implementation is claimed.
  (документация)

## 2026-10-18 — P0-3: ApplyRunRecoveryCommand implementation (WP-P03-02B, Refs #7)

- Added `ApplyRunRecoveryCommand` in `store/canonical_run_recovery.go` (TD-R2):
  `RunRecoveryCommand`, `RunRecoveryReceipt`, `RunRecoveryTransition`; 5 transitions
  (MARK_INTERRUPTED implemented, 4 fail-closed); 7 specific violation sentinels +
  `ErrRunRecoveryTransitionForbidden` for admission/refusal boundaries; KSC-C
  stop-proof validation (Kind==Stop, Completed, Stopped, StopMethod, full Binding,
  parent fence Completed+FENCED); MARK_INTERRUPTED atomically commits Run→Failed +
  3 events. FENCE_ONLY/PREPARE_REPLACEMENT/COMPLETE_EXISTING/RESUME_OBSERVATION
  fail-closed. (`TestApplyRunRecoveryMarksInterruptedAtomically`,
  `TestApplyRunRecoveryExactReplayReturnsOriginalReceiptWithoutWrites`,
  `TestRunRecoveryFailClosedTransitionsHaveNoSideEffects`,
  `TestRunRecoveryPublicEntryMapsAllSevenAcceptedCodes`)
- Test coverage in `store/canonical_run_recovery_test.go`: stop-proof mutation
  matrix with direct binding-field predicates via `mutateStopAndFenceBinding`;
  duplicate-transition guard; guard precedence (14 cases); 26 stop-proof mutations.
  (`TestRunRecoveryStopProofMutationMatrixFailsClosed`,
  `TestRunRecoveryGuardCodesAndPrecedence`,
  `TestRunRecoveryDuplicateTransitionRejected`)
- opencode:workspace-oe3 REVIEW_ACCEPTED (round 2) on 442f1acc;
  opencode:workspace-o42 AUDIT_CHANGES_REQUIRED (F-1: direct binding predicates
  unproven; F-2: duplicate transition guard unproven). Corrective round applied:
  mutateStopAndFenceBinding for A-F-1, direct normalizer test for A-F-2.
  opencode:workspace-oe3 REVIEW_ACCEPTED on corrective commit 508060c;
  opencode:workspace-o42 AUDIT_ACCEPTED on 508060c. Merged to main via PR #77
  (merge commit d72455a), CI green.
  (`TestRunRecoveryStopProofMutationMatrixFailsClosed`,
  `TestRunRecoveryNormalizerRejectsDuplicateTransition`)
- Architecture component `run-recovery-command` registered in
  `docs/architecture/COMPONENT-STATUS.yaml` (partial; MARK_INTERRUPTED implemented,
  4 transitions remaining); anchors added to `docs/ARCHITECTURE.md` and
  `docs/TARGET-ARCHITECTURE.md`. (`TestApplyRunRecoveryMarksInterruptedAtomically`,
  `TestRunRecoveryStopProofMutationMatrixFailsClosed`,
  `TestRunRecoveryDuplicateTransitionRejected`)

## 2026-10-17 — P0-3: LOCAL_GIT family contract + OWNER decision (Refs #7)

- Added `LOCAL_GIT` as third canonical `ExternalActionFamily` per decision
  P0-3-LOCAL-GIT-AND-RUN-RECOVERY-R1 (`internal/externalaction/localgit.go`):
  `LocalGitOperation` enum (6 ops), `LocalGitActionPayload`, `LocalGitMarker`,
  `LocalGitObservation`, `NewLocalGit()`, `validateLocalGit()`. Uses same common
  ExternalAction envelope, Outbox, claim epoch, and recovery loop as AGENT_SESSION
  and GITHUB; no parallel action store. No credentials, no network, no push.
  `TestLocalGitProtectedBranchRejected`, `TestLocalGitSystemTmpRejected`,
  `TestLocalGitIdempotencyKeyStableAcrossTimestamps`,
  `TestLocalGitCompletedRequiresObservation`,
  `TestM01LocalGitMarkerOperationInstanceIDMismatch` … `TestM08LocalGitResultEnvelopeRejectsStopMethod`
  (9 invariants, 13 focused scenarios, 8/8 mutations killed).
- Persisted decision `docs/decisions/P0-3-LOCAL-GIT-AND-RUN-RECOVERY-R1.yaml`
  accepting LOCAL_GIT family and Coordinator authority for `ApplyRunRecoveryCommand`
  (WP-P03-02B design phase, not yet implemented). `TestLocalGitUnknownFamilyRejected`.

## 2026-10-17 — Shadow Validation: crash recovery and admission report (Refs #70)

- Added restart-safe shadow recovery (`internal/shadowpilot/crash_recovery.go`):
  persisted `RUNNING` no longer proves liveness, ambiguous executions are
  observed before retry, stale generations and duplicate launches are rejected,
  and Review/Audit rationale survives restart. `TestRestartBeforeIntentDoesNotLaunch`, `TestPersistedRunningRequiresFreshObservation`, `TestM01PersistedRunningAcceptedAsLiveIsRejected`, `TestM02RestartDuplicateExecutionIsBlocked`, `TestM03StaleGenerationResultAcceptedIsRejected`, `TestM06OldReviewAppliedToNewSubjectIsRejected`, `TestM08PartialStateBecomesCurrentIsRejected` (документация — shadow pilot).
- Added deterministic Shadow Validation reporting and P0-3 admission
  (`internal/shadowpilot/report.go`): unsafe divergence, incomplete evidence,
  production writes, duplicate launches, stale results, or partial states
  block admission. `ADMIT_P0_3` is produced only when all WPs are integrated,
  runtime is clean, and Review and Audit are both accepted. `TestNewReportAllGatesPassGivesAdmitP03`, `TestNewReportUnsafeDivergenceGivesCorrectionRequired`, `TestNewReportNotEvaluatedDimensionGivesRepeat`, `TestDeriveRecommendationPrecedenceUnsafeBeatsNotEvaluated` (документация — shadow pilot).

## 2026-10-17 — Shadow Validation pilot: WP-SV-04 Review/Audit Graph (Refs #70)

- WP-SV-04: `internal/shadowpilot/review_audit.go` — `ShadowReviewResult` (reviewerID≠executorID enforced via `IndependenceViolation`), `ShadowAuditResult` (auditorID≠executorID, auditorID≠reviewerID), `ShadowFinding` (BLOCKING/NON_BLOCKING, OPEN/CLOSED; `Close()` требует evidenceRef — `StopRationaleOrEvidenceLost`), `ShadowDiscussionNode` (rationale preserved, нет поля verdict — структурный запрет M03), `ComputeReadiness()` (детерминированная readiness по exact subjectSHA; superseded results → NOT_EVALUATED; NOT_EVALUATED ≠ PASS; mixed-subject → NOT_EVALUATED). 8 mutation tests M01–M08 (все убиты), 12 тестов сценариев. `TestReviewerCannotEqualExecutor`, `TestM01ReviewerIndependenceDisabledIsRejected`, `TestAcceptedReviewAndAuditSameSubjectCanBecomeReady` (документация — shadow pilot, не production).

## 2026-10-17 — Shadow Validation pilot: WP-SV-03 Single-agent execution (Refs #70)

- WP-SV-03: `internal/shadowpilot/executor.go` — `ShadowAssignment` (durable assignment intent, immutable after creation), `Admit()` (subject SHA match + reviewer≠executor guard), `ShadowHeartbeat` (monotonic sequence, stale detection), `ShadowExecution` (state machine: ADMITTED→RUNNING→COMPLETED|FENCED). Ключевые stop conditions: `StopExactSubjectMismatch` при несоответствии subject SHA на admission, `StopReviewerEqualsExecutor` при конфликте ролей, `StopStaleGenerationAccepted` при несоответствии generation в результате (`StaleGenerationError`). Fenced execution cannot be completed (`TestFencedExecutionCannotBeCompleted`). Stale heartbeat triggers automatic fence (`TestCheckHeartbeatTimeoutFencesStaleExecution`). 17 новых тестов (`TestAdmitRejectsSubjectMismatch`, `TestCompleteRejectsStaleGeneration`, и т.д.) (документация — shadow pilot, не production).

## 2026-10-17 — Shadow Validation pilot: WP-SV-01 Integration Map + WP-SV-02 Shadow run model (Refs #70)

- WP-SV-01 (документация): read-only integration map — production entry point, adapter boundary, AgentSession bindings, Review/Audit command path, shadow store boundary, write denylist (17 методов), crash injection point, разрешённые пути.
- WP-SV-02: `internal/shadowpilot` — типы `ShadowRun`, `AgentResult`, `BaselineResult`, `ComparisonResult` (10 dimensions, derived outcome), `EvidenceBundle` (read-only refs, `ProductionAuthorityAsserted: false` enforced). Fixture `testdata/shadow-validation/input.yaml` с subjectSha `b4334cd`. Ключевой инвариант: `StopProductionStateMutation` — любое присвоение `ProductionAuthorityAsserted: true` является stop condition (`TestEvidenceBundleValidateRejectsProductionAuthority`). Unsafe divergence trumps NOT_EVALUATED (`TestNewComparisonResultUnsafeTrumpsNotEvaluated`).

## 2026-10-17 — R104 rationale-loss evidence classification (Refs #43)

- Проведён read-only анализ 12 случаев потери rationale по Issue #43. Три случая закрыты существующей реализацией (ReviewResult/AuditResult сохраняют Findings в canonical events; migration не затрагивает Task events; DP-003 охватывает document-layer расхождения). Семь случаев классифицированы как DP-003 (две поверхности расходятся: supersession reason хранится в `DomainEvent.Summary`, но не попадает в CLI-проекцию `superseded_ids`; `gate evaluate` выводит Status без reason codes). Два случая классифицированы как DP-004 (граница нормы R104 и типов не сверяется: `AuditedProperty.Note` опционален без `Evidence []Reference`; `Finding.Status = RESOLVED` не требует `ResolutionEvidenceRefs`). Новый класс DP не требуется; существующих DP-003/DP-004 достаточно. Результат зафиксирован в `docs/DEFECT_PATTERNS.md` (документация).

## 2026-10-17 — Completion candidate и credential-free GitHub observation wiring (Refs #67)

- Кандидат завершения проходит строгую проверку canonical Task/Run, exact subject, Action Map, Completion Policy и watchdog до записи canonical event; публикация выполняется после commit (`TestRunWithReadinessProcessesCandidateBeforeReceivingNextMessage`, `TestUnsupportedOwnerBoundaryIsNotPublished`). Добавлен только in-memory GitHub observation backend для fixture, без live API, credentials, writes или merge (`TestObserveExistingPR64WithoutCreate`).

## 2026-10-17 — Изоляция architecture-sync fixtures (Refs #59)

- `scripts/check-architecture-sync-test` исключает `.git` как каталог и как linked-worktree pointer file; fixture создаёт собственный repository. Проверки `NormalCheckoutFixtureExcludesGitDirectory`, `LinkedWorktreeFixtureExcludesGitFile` и `FixtureCreatesIndependentRepository` выполняются самим harness (документация).
- Project-local scratch задаётся явно, а `.scratch` исключается из копирования для предотвращения рекурсии. До и после fixture проверяются config, refs, HEAD, index и содержимое host worktree; системный tempfile default запрещён тестом `SystemTmpIsNotUsed` (документация).

## 2026-10-17 — Canonical ExternalAction schema 3 contract (Refs #63)

- Schema-3 actions require an explicit AGENT_SESSION or GITHUB family; session keys, request/result digests, claim state and event history retain their schema-2 semantics (`TestExistingSchema2SessionActionMigratesToAgentSession`, `TestMigrationPreservesIdempotencyKey`, `TestMigrationPreservesDigests`).
- Canonical startup migrates schema 2 under the existing persistence lock, with private verified backup, durable manifest, atomic publication and restart reconciliation; projection Load remains read-only (`TestMigrationReplayIsIdempotent`, `TestPartialMigrationDoesNotBecomeCurrent`, `TestExternalActionMigrationPublicationRecovery`).
- Typed GitHub payload validation binds markers and positive completion evidence; merge validation requires an accepted exact-head gate and rejects absent canonical gate capability. No live GitHub API, credentials, gate authoring or production GitHub dispatch is introduced (`TestCompletedRejectsMarkerMismatch`, `TestMergeGateMustMatchExpectedHead`, `TestUnsafeDowngradeRejectedWithSchema3Records`).

## 2026-10-15 — Durable orchestration implementation candidate (Refs #49)

- Existing coordd loop gains canonical external-action dispatch/recovery hooks; schema v2 retains action requests, claim epochs, ambiguous outcomes, observations and acknowledgement intents alongside Task events and Inbox receipts (`TestCompletionAndReceiptIntentCommitTogether`).
- Crash/restart observes unresolved operations before redispatch; accepted logical fence revokes the old AgentSession; fake stop proof gates the replacement intent. Revision bundles rebuild CURRENT/OPEN-WORK/Ledger from one snapshot (`TestCrashAfterDispatchBeforeResponseObservesBeforeRetry`, `TestProjectionBundleUsesOneRevisionAndRebuilds`).
- No live adapters or production replacement Run/session binding; Proposed ADR remains non-normative. Tests and compiling mutations cover the isolated deterministic slice (документация).

## 2026-10-15 — fix(test): R08 ancestor guard mutation kill in TestDecisionAncestorGit (Refs #44)

- `TestDecisionAncestorGit`: moved `gitCommitAll(t, outer)` to after fixture creation so the decision file is committed before `Check` runs; the root-mismatch guard (governance.go:247-249) is now the rejection cause, not uncommitted-file detection. Added Detail assertion (must contain "ancestor") and positive control with `RepoRoot=outer`. Mutation kill confirmed: deleting the guard leaves `resolvedDetected` unused (compile error) and, if disabled without deletion, allows `Check` to find the committed file producing no violation → test fails (документация).

## 2026-10-14 — WP-06: parity gate и CI трёх Go-контуров (Refs #46)

- `GO-TEST-HARNESS-RETIREMENT-R1` и `ISSUE-46-PARITY.yaml` фиксируют решение OWNER и все девять удалённых Python cases; Python зависимых recovery/architecture checks сохраняется (документация).
- `check-resolver-launcher` включает `cmd/coord-governance` с `TestLauncherDryRun` и `TestLauncherExecuteInvokesExecutorWithBoundProjectRoot`; CI `migrated-harnesses` запускает три targets с запрещёнными Python/pip executables.
- `TestSandboxFailsClosedWithoutWorkspaceRoots` проверяет отказ Linux backend при отсутствующих workspace roots даже с доступным executable; `TestNonLinuxSandboxFailsClosed` проверяет unsupported backend; `TestSeccompProfileParses` больше не пропускает отсутствующий config.
- `TestHTTPAdapterSupervisorFenceLiveProcess` сохраняет live HTTP и реальный child process из Python-сценария, проверяет ошибку, fencing и остановку процесса; `TestHTTPAdapterSupervisorFence` также проверяет termination.
- `TestHeartbeatMapsTerminalRegistryStatuses`, `TestHTTPAdapterSupervisorFenceRevoked` и `TestHTTPAdapterSupervisorFenceLeaseDigest` проверяют fail-closed mapping terminal wire statuses, отзыв на втором heartbeat и fencing при несовпадении lease digest; parity matrix отражает фактический Python-сценарий (`TestHTTPAdapterSupervisorFenceLiveProcess`).
- Отдельный GitLab `supervisor-seccomp-runtime` сохраняет `py_compile` и загрузку живого seccomp-фильтра; migrated supervisor Go job остаётся без Python harness (документация).

## 2026-10-14 — chore(test): retire Python harnesses after Go parity confirmed, WP-06 (Refs #46)

- Удалены три Python test-файла после полного Go parity, зелёного CI и AUDIT_ACCEPTED: `tests/test_resolver_launcher.py`, `tests/test_sandbox_session_registry.py`, `tests/test_supervisor_seccomp_registry.py` (документация).

## 2026-10-13 — feat(test): migrate Python check harnesses to Go, WP-02–WP-05 (Refs #46)

- `internal/admission/validator_test.go`: `TestDigestDriftDetected` — parity с Python `test_digest_drift_detected`; после изменения AGENTS.md проверяет production `Validate` отклоняет action context с `DIGEST_DRIFT`.
- `cmd/coord-governance/launch_test.go`: `TestLauncherDryRun` — parity с Python `test_launcher_dry_run`; проверяет dry-run через launcher и отсутствие вызова process executor. `TestLauncherExecuteInvokesExecutorWithBoundProjectRoot` различает ProjectRoot, RepositoryRoot и AssignedWorktree; мутации передачи RepositoryRoot/AssignedWorktree убиваются (A1).
- `internal/agentsession/admit_test.go`: добавлен `TestRegistryClientMockAdmission` — parity с Python `test_registry_client_mock`; mock HTTP возвращает ADMITTED, `sessionId=s1`.
- `internal/sandbox/profiles_test.go`: добавлены `TestSandboxFailsClosedWithoutBwrapOrEnv` (parity с Python `test_sandbox_fails_closed_without_bwrap_or_env`; явно отсутствующий путь Bubblewrap → error), `TestSeccompProfileParses` (парсит `config/SECCOMP-PROFILES.yaml`, проверяет `ptrace` в `denyExplicit`), `TestLibseccompLoaderFailsClosedOnNonLinux` (stub returns error on non-Linux).
- `internal/supervisor/http_fence_test.go`: добавлен `TestHTTPAdapterSupervisorFence` — parity с Python `test_http_adapter_and_supervisor_fence`; `httptest.Server` возвращает generation=2 при local=1 → `receipt.Fenced==true`, `FenceReason==FenceGeneration`.
- `Makefile`: три Python-цели (`check-resolver-launcher`, `check-sandbox-session-registry`, `check-supervisor-seccomp-registry`) переключены на Go: `go test -race -count=1 ./internal/...` (документация).

## 2026-10-13 — fix(recovery): require explicit RepoRoot to break git boundary tautology (review R08 round 2, Refs #44)

- `Check()` returns error when `CheckOptions.RepoRoot == ""`: without an explicit root, the cwd-derived and file-derived rev-parse calls both return the same ancestor, making the comparison tautological (`TestCheckRequiresRepoRoot`). `TestRealGovenance` updated to pass `RepoRoot` from `git rev-parse`.
- CLI `--repo-root` flag is now required: auto-derivation from cwd removed; binary errors if flag is omitted. Makefile `check-recovery` target updated to pass `--repo-root $(shell git rev-parse --show-toplevel)` (документация).

## 2026-10-13 — fix(recovery): fail-closed git boundary and RCS event guard (review R08/R09, Refs #44)

- `readDecisionCommitted` переписан: использует `git rev-parse --show-toplevel` вместо пешего `findGitRoot`; при отсутствии git-контекста возвращает ошибку (нет fallback на диск); при установленном `CheckOptions.RepoRoot` проверяет совпадение toplevel с ним — reject ancestral git (R08); флаг `--repo-root` добавлен в CLI, по умолчанию определяется через `git rev-parse`. Новые тесты: `TestDecisionNoGitContext`, `TestDecisionAncestorGit`. Обновлены `TestRegulationDecisionMissing`, `TestDecisionClassInvalid`, `TestDecisionStatusInvalid` — все используют git-fixture.
- `checkActionMap` отклоняет `RECOVER_COORDINATION_STATE` в секции `events` с кодом `RECOVER_COORDINATION_STATE_AS_EVENT` (R09). Новый тест: `TestRCSAsEvent`.

## 2026-10-13 — fix(recovery): cover optionalSha chain in coordinator YAML schema test (audit N04, Refs #44)

- `TestSchemaCoordinatorFullSHAPattern` дополнен: проверяет что `$defs/optionalSha.anyOf[0].$ref == "#/$defs/fullSha"` и что три subject SHA сайта (`reviewSubjectSha`, `auditSubjectSha`, `ciSubjectSha`) ссылаются на `#/$defs/optionalSha`. Мутация `$defs/optionalSha → {type: string}`: поймана. `TestSchemaCoordinatorFullSHAPattern`

## 2026-10-13 — fix(recovery): structural fullSha verification in coordinator YAML schema test (audit N03, Refs #44)

- `TestSchemaCoordinatorFullSHAPattern` переписан: парсинг YAML через `gopkg.in/yaml.v3`, проверка `$defs/fullSha.pattern` и навигация к каждому из 4 SHA-сайтов с разрешением `$ref`. Мутация C-1 (`$defs/fullSha` ослаблен + decoy): поймана. Репоинт сайта на слабую def: поймана. `TestSchemaCoordinatorFullSHAPattern`

## 2026-10-13 — fix(recovery): structural per-site fullSha verification (audit N02, Refs #44)

- `TestSchemaFullSHAPattern` переписан: вместо подсчёта строк — парсинг JSON-схемы и навигация по JSON Pointer к каждому из 7 обязательных SHA-полей. Decoy-паттерны в `description` больше не обходят проверку. Мутации E1 (все 7 сайтов + 8 decoy) и E2 (один сайт + один decoy): оба пойманы (exit 1). `TestSchemaFullSHAPattern`

## 2026-10-13 — fix(recovery): per-site fullSha coverage in schema integrity test (audit N01, Refs #44)

- `TestSchemaFullSHAPattern` заменяет `strings.Contains` на `strings.Count(...) >= 7`: JSON-схема содержит паттерн `[0-9a-f]{40}` ровно в 7 полях inline; ослабление любого одного поля снижает счётчик до 6 и немедленно роняет тест. Мутационная проверка (замена первого из 7 сайтов): exit 1, тест упал. `TestSchemaFullSHAPattern`

## 2026-10-13 — fix(recovery): restore report-level gate and schema integrity tests (audit F1/F3, Refs #44)

- Восстановлен `bash tests/test_recovery_checks.sh` в target `check-recovery` в Makefile: 18 report-level правил (wildcard ban, fullSha, cross-field consistency, path containment, credential leakage) снова входят в `make check`. Auditor доказал, что без этого мутация схем проходила CI без обнаружения (F1). `TestSchemaWildcardBanPattern` `TestSchemaFullSHAPattern` `TestSchemaCoordinatorFullSHAPattern`
- Добавлен `internal/recovery/schema_integrity_test.go`: три Go-теста фиксируют наличие wildcard-ban и full-SHA паттернов в обеих схемах. Удаление паттерна из любой схемы теперь немедленно роняет Go test-suite (F3). `TestSchemaWildcardBanPattern` `TestSchemaFullSHAPattern` `TestSchemaCoordinatorFullSHAPattern`

## 2026-10-13 — fix(ci): add jsonschema to Python deps (check-recovery needs it) (документация)

- `jsonschema` добавлен в `pip install` в CI: `scripts/check-recovery-report.py` требует его для валидации Recovery Report против JSON-схемы. Refs #44

## 2026-10-13 — fix(ci): restore Python setup for resolver/launcher tests (macOS fix)

- Возвращены `actions/setup-python` и `pip install PyYAML` в CI: они нужны для `check-resolver-launcher`, `check-sandbox-session-registry`, `check-supervisor-seccomp-registry`, которые используют Python. Прямые вызовы Python governance скриптов остались удалены. Refs #45 (документация)

## 2026-10-13 — fix(registry): mark uncommitted documents optional: true (CI fix for #45)

- 44 документа в DOCUMENT-REGISTRY.yaml, чьи файлы не закоммичены в Git (Python-скрипты, scenario-файлы, GitLab CI, RFC, шаблоны), помечены `optional: true`. Pre-existing defect: реестр регистрировал файлы, которые существовали только локально (untracked). `check-document-registry` в Go впервые обнаружил это на CI (Python-скрипт никогда не прогонялся в CI-checkout). Refs #45

## 2026-10-13 — Migrate governance checks to Go (Issue #45, closes CI blocker for #44)

- Добавлены 4 Go-пакета: `internal/documentregistry/` (strict-YAML парсер DOCUMENT-REGISTRY.yaml, 18 кодов нарушений, containment + symlink escape check, `TestRealRegistry`), `internal/actionmap/` (строгий парсер ACTION-MAP.yaml, 11 кодов нарушений, граф достижимости, reasonCode-аудит, `TestRealActionMap`), `internal/defectpatterns/` (парсер DEFECT-PATTERNS.yaml, 9 кодов нарушений, `TestRealDefectPatterns`), `internal/recovery/` (recovery governance checker, портирован из `scripts/check-recovery-governance.py`, git-show HEAD + git-archive fallback, 17 кодов нарушений, `TestRealGovenance`). Добавлена `internal/governanceio/yaml.go` (generic strict YAML decoder). Добавлены команды в `cmd/coord-governance/`: `check-document-registry`, `check-action-map`, `check-defect-patterns`, `recovery governance-check`. Добавлен `docs/operations/DEFECT-PATTERNS.yaml` — машиночитаемый источник из `docs/DEFECT_PATTERNS.md` (5 паттернов, все PARTIAL). Makefile: 4 Python-цели заменены Go-командами (`go run ./cmd/coord-governance ...`), `coord-governance` добавлен в `make build`. CI: убраны `actions/setup-python`, `pip install PyYAML` и 3 прямых запуска Python-скриптов; `make check` покрывает все проверки. `make check` PASS на Linux и macOS. Refs #45 (`TestRealRegistry`, `TestRealActionMap`, `TestRealDefectPatterns`, `TestRealGovenance`)

## 2026-10-13 — Recovery governance: coordinator session recovery integration (TD-R2), refs naghuale/coord#44

- Добавлены `docs/operations/AGENT-SESSION-RECOVERY-REGULATION.md`, `docs/templates/COORDINATOR-RECOVERY-PROMPT.md`, `docs/templates/RECOVERY-PROMPT-SHORT.md`, `docs/templates/RECOVERY-REPORT-TEMPLATE.yaml`, `docs/schemas/recovery-report.schema.json`, `docs/schemas/recovery-report.coordinator.schema.yaml`; валидаторы `scripts/check-recovery-report.py` и `scripts/check-recovery-governance.py`; фикстуры `tests/fixtures/valid/resume.yaml`, `tests/fixtures/valid/coordinator-recover-coordination-state.yaml`, `tests/fixtures/invalid/foreign-resume.yaml`; self-test `tests/test_recovery_checks.sh`; CI job `.gitlab/ci/recovery.yml`. Зарегистрированы 6 документов, 2 readEvents и 2 changeMatrix в `docs/DOCUMENT-REGISTRY.yaml`; 8 recovery events и секция `recoveryTransitions` в `docs/operations/ACTION-MAP.yaml`; секция «Восстановление сессии» в `AGENTS.md`; цель `check-recovery` в `Makefile`. Новый переход `RECOVER_COORDINATION_STATE`: coordinator сохраняет unknown changes, отделяет committed subject и готовит read-only classification assignments, не останавливая координацию; наличие `FOREIGN_OR_UNKNOWN` само по себе больше не является достаточным основанием для `PRESERVE_AND_STOP`. Commit `7d23d5b95cc840abf30cf4cf112bec9adc90384b`, base `51793b3da4597aa91f5a805cb02bbba1f099f44b`. Статус документов `proposed`. (`TestCurrentChangelogEntriesHaveEvidence`)

- docs: commit 7d23d5b registered 6 new documents in DOCUMENT-REGISTRY (not 7 as stated in commit message); 2 readEvents vocabulary entries and 2 changeMatrix keys also added — Refs #44 (`TestCurrentChangelogEntriesHaveEvidence`)

- Recovery governance corrections для #44: `RECOVER_COORDINATION_STATE` перенесён из раздела `events` в `recoveryTransitions` Action Map и добавлен в `expectedResults` событий `WORKTREE_STATE_UNKNOWN` и `SESSION_RESUMED`, чтобы переход стал достижимым; шаг 12 регламента и шаг 10 короткого промта приведены к двухуровневому правилу (read-only анализ безопасен → `RECOVER_COORDINATION_STATE`, риск повреждения evidence → `PRESERVE_AND_STOP`); `RECOVERY-REPORT-TEMPLATE.yaml` дополнен переходом в enum, обязательными `unknownFilesModified`/`unknownFilesCommitted` и секцией `coordinationRecovery`; `check-recovery-governance.py` получил графовую проверку достижимости переходов вместо substring-поиска. (`TestCurrentChangelogEntriesHaveEvidence`)

## 2026-10-10 — GO-01+GO-02+GO-03+GO-04+GO-06: Go ActionContext resolver, admission, Registry client, Supervisor loop

- Добавлены Go пакеты `internal/actioncontext/` (model, resolver, authority, digest — читает ACTION-MAP.yaml, DOCUMENT-REGISTRY.yaml, CONTEXT-BUDGETS.yaml; authority checks: historical/legacy/proposed/inactive; SHA-256 file digest binding; 4 тестa PASS), `internal/admission/` (bootstrap types, Validate() — containment, baseSHA, executable allowlist, side-effect gate, digest drift; 3 теста PASS), `internal/agentsession/` (Registry interface, IdempotencyKey, OpenAPIClient с retry/backoff/jitter; 1 тест PASS), `internal/supervisor/` (Loop.Run() — heartbeat ticker, generation/lease/status fencing, TerminateGroup, Registry Fence+Complete; 1 тест PASS), `internal/governanceio/` (DecodeStrict, Encode), `cmd/coord-governance/` (CLI: resolve). go.mod добавлен `gopkg.in/yaml.v3 v3.0.1`. make check PASS. (`TestCurrentChangelogEntriesHaveEvidence`)

## 2026-10-10 — Go migration plan: docs/plans/go-migration/GO-MIGRATION-PLAN.md

- Добавлен `docs/plans/go-migration/GO-MIGRATION-PLAN.md` — полный план миграции 13 задач (GO-01..GO-13) с dependencies, transition phases, CI и Definition of Done; зарегистрирован в DOCUMENT-REGISTRY.yaml (152 документа); Python prototypes помечены как reference_prototype, не production. (`TestCurrentChangelogEntriesHaveEvidence`)

## 2026-10-10 — Supervisor v2: idempotency, retry, OpenAPI mapping (AIAG-03 prototype)

- Обновлены `scripts/session_supervisor.py` (добавлен `heartbeatSequence`, интерфейс `--registry-mapping`/`--registry-retry-policy`) и `scripts/registry_http_adapter.py` (retry с backoff+jitter, Idempotency-Key по canonical JSON, statusMapping, `--mapping`/`--retry-policy`). Добавлены `scripts/check_openapi_mapping.py` (verifier: operationId, method, path, digests); `config/SESSION-REGISTRY-OPENAPI-MAPPING.yaml` (OpenAPI mapping candidate, status NOT_EVALUATED); `config/REGISTRY-RETRY.yaml` (max 4 attempts, retryable 4xx/5xx, idempotency keyInputs, heartbeat sequence contract); `tests/mock_registry_server.py` обновлён для idempotency (503 on heartbeat#1 first attempt, revoked on seq≥2). Тест обновлён под новый интерфейс: 2/2 PASS. `config/SESSION-REGISTRY-HTTP.yaml` помечен superseded. canonicalOpenApiMapping: NOT_EVALUATED. (`TestCurrentChangelogEntriesHaveEvidence`)

## 2026-10-10 — Supervisor loop, seccomp, Registry HTTP adapter (AIAG-03 prototype)

- Добавлены `scripts/session_supervisor.py` (полный цикл: admit → heartbeat → fence/complete → SupervisorReceipt), `scripts/registry_http_adapter.py` (configurable HTTP adapter: bearer auth, response mapping, endpoint config), `scripts/seccomp_exec.py` (Linux fail-closed syscall filtering через libseccomp); `config/SUPERVISOR.yaml`, `config/SESSION-REGISTRY-HTTP.yaml`, `config/SECCOMP-PROFILES.yaml` (python-agent-v1 allowlist); `docs/PRODUCTION-REGISTRY-ADAPTER.md`, `docs/SECCOMP.md`; `tests/mock_registry_server.py`, `tests/test_supervisor_seccomp_registry.py` (2 теста: supervisor fencing PASS, seccomp profile parses PASS); `.gitlab/ci/supervisor-seccomp-registry.yml`; зарегистрированы supervisor-policy, session-registry-http, seccomp-profiles, production-registry-adapter-doc, seccomp-doc в DOCUMENT-REGISTRY.yaml (149 документов); Makefile target `check-supervisor-seccomp-registry` добавлен в `check`. canonicalOpenApiMapping: NOT_EVALUATED; liveLocalSeccompLoad: NOT_EVALUATED. (`TestCurrentChangelogEntriesHaveEvidence`)

## 2026-10-10 — OS-level sandbox и AgentSession Registry admission (AIAG-03 prototype)

- Добавлены `scripts/sandbox_exec.py` (Bubblewrap fail-closed execution), `scripts/session_registry_client.py` (adapter interface), `scripts/launch_sandboxed_session.py` (combined launcher), `scripts/mock_session_registry.py` (test-only); `config/SANDBOX-PROFILES.yaml` (evaluation-linux, production-linux), `config/SESSION-REGISTRY-CONTRACT.yaml` (admit/heartbeat/complete/fence contract); `docs/SECURITY-BOUNDARY.md`; `tests/test_sandbox_session_registry.py` (2 тестa); `.gitlab/ci/sandbox-session-registry.yml`; зарегистрированы sandbox-profiles, session-registry-contract, security-boundary в DOCUMENT-REGISTRY.yaml (144 документа); Makefile target `check-sandbox-session-registry` добавлен в `check`. Статус proposed; production Registry adapter и supervisor loop не реализованы. (`TestCurrentChangelogEntriesHaveEvidence`)

## 2026-10-10 — ActionContext resolver и fail-closed launcher (AIAG-03 prototype)

- Добавлены `scripts/governance_lib.py`, `scripts/resolve-action-context`, `scripts/check-action-context`, `scripts/build-agent-bootstrap`, `scripts/launch-agent` — рабочий прототип resolver и fail-closed launcher из coord-resolver-launcher-kit; `config/CONTEXT-BUDGETS.yaml` (operational budgets); `docs/operations/ACTION-MAP.yaml` (машиночитаемая YAML-проекция ACTION-MAP.md, proposed); `tests/test_resolver_launcher.py` (5 тестов); `examples/project/` (test fixtures); `examples/*.yaml`; `.gitlab/ci/resolver-launcher.yml`; `action-map-yaml` зарегистрирован в DOCUMENT-REGISTRY.yaml (141 документ); Makefile target `check-resolver-launcher` добавлен в `check`; launcher enforcement не реализован до AIAG-04. (`TestCurrentChangelogEntriesHaveEvidence`)

## 2026-10-10 — AI Agent Governance work plan: docs/plans/ai-agent-governance и .github/ISSUE_TEMPLATE

- Добавлены 18 Issue-описаний AIAG-01..AIAG-18 (`docs/plans/ai-agent-governance/issues/`), work plan (`GITLAB-WORK-PLAN.md`), приоритеты и оценки (`PRIORITY-AND-ESTIMATION.md`), `README.md`, `labels.txt`, `issues.csv`; 6 GitHub Issue templates `.github/ISSUE_TEMPLATE/` (AI_Agent_Implementation, AI_Agent_Decision_R, AI_Agent_TD_R2, AI_Agent_External_Dependency, AI_Agent_Baseline_Promotion, AI_Agent_Incident); все 21 документ зарегистрированы в DOCUMENT-REGISTRY.yaml (140 документов); статус `proposed`; нормативные только после принятия RFC-0002. (`TestCurrentChangelogEntriesHaveEvidence`)

## 2026-10-10 — AI Agent Governance full kit: docs/ai-agents и docs/schemas

- Добавлены 10 файлов политики `docs/ai-agents/` (adapter-registry, ARTIFACT-STORE-DECISION, BASELINE-ACCEPTANCE, CONTEXT-BUDGETS, EVALUATION-COST-MODEL, EVALUATION-PROFILES, HIDDEN-CASE-POLICY, IMPLEMENTATION-STATUS, MODEL-ADAPTER-STANDARD, RETENTION-POLICY) и 2 JSON Schema файла `docs/schemas/` (action-context.schema.yaml, evidence-record.schema.yaml); все 12 зарегистрированы в DOCUMENT-REGISTRY.yaml (119 документов); статус `proposed`, кроме IMPLEMENTATION-STATUS — `architecture_status/active`; нормативные только после принятия RFC-0002. (`TestCurrentChangelogEntriesHaveEvidence`)

## 2026-10-10 — baseline calibration and reviewed Action Map store

- Добавлены immutable hard-zero threshold checks, сравнение с совместимым promoted baseline, append-only run storage, median candidate, fail-closed Reviewer activation и GitLab jobs. Текущий измеритель оставляет четыре evidence hard metrics как `NOT_EVALUATED`; поэтому baseline run/promotion пока блокируются, а sample baseline из архивов не используется. (`TestCurrentChangelogEntriesHaveEvidence`)

## 2026-10-10 — Action Map behavior evaluation kit

- Добавлены сценарные evaluation profiles, GitLab metrics job и сбор JSON/JUnit/OpenMetrics/dotenv artifacts. Agent verdict вычисляет отдельный comparator с self-tests; без model adapter результат остаётся `NOT_EVALUATED`. Разделены реализованные static/behavior checks и пока отсутствующие resolver/Evidence Gate; sample outputs из архива не добавлялись. (`TestCurrentChangelogEntriesHaveEvidence`)

## 2026-10-10 — GitLab CI integration for Action Map

- Added a reusable GitLab CI include fragment with blocking validation and self-test jobs, plus a root configuration example and integration guide.
- Registered the guide and example in the Document Registry. No active root `.gitlab-ci.yml` exists, so the example is not enabled and GitLab CI Lint remains pending integration with the project's final pipeline. (`TestCurrentChangelogEntriesHaveEvidence`)

## 2026-10-10 — обязательные Action Map admission и сценарные gates

- `AGENTS.md` и role prompts требуют admission по Document Registry и Action
  Map, а Coordinator записывает Action Card перед значимым переходом.
- Добавлены 15 зарегистрированных Action Map scenarios, result schema и
  checker со стабильными error codes для authority/transition правил и
  структурированных ответов агента. Добавлен path-filtered workflow для PR и
  push; adversarial self-tests используют project-local `.scratch/tmp`.
  Registry, Action Map scenarios и Architecture Sync включены в `make check`.
  (`TestCurrentChangelogEntriesHaveEvidence`)

## 2026-10-10 — добавлены карта и реестр документов (процесс разработки)

- Добавлены `docs/DOCUMENT-MAP.md`, `docs/DOCUMENT-REGISTRY.yaml` и
  `docs/operations/ACTION-MAP.md`: минимальные role startup paths, шесть классов
  документов, authority, владельцы, чтение/обновление и карты переходов событий.
- Реестр охватывает 82 документа и записи состояния; operational guides получили
  явные YAML headers с применимостью и authority. Добавлены
  `scripts/check-document-registry` и отрицательные self-tests, включённые в
  `make check` и CI.
  (`TestCurrentChangelogEntriesHaveEvidence`)

## 2026-10-10 — AGENTS.md: разделены решения OWNER и human actions (документация)

- (документация) Уточнены `OwnerBoundaryCheck`, `QUESTION_SUPPRESSED`,
  `HUMAN_ACTION_REQUIRED` и Review/Audit verdicts; Task artifacts размещены
  внутри `COORD_PROJECT_ROOT`. Добавлены Architecture Sync, wrapper-based
  Commit Readiness, admission fields для реальных сессий и branch/commit
  semantics Operations Ledger.
  (`TestCurrentChangelogEntriesHaveEvidence`)

## 2026-10-10 — VERIFICATION.md: разделены активные и исторические evidence (документация)

- (документация) Repository gate привязан к `Makefile` и запускается через
  project-local wrapper; legacy Run, canonical model, runtime и reference
  integrations разведены. Добавлены структурированные verification results,
  актуальные component references и ограничения исторических live-прогонов,
  включая неизвестные subject revisions и непереносимые local evidence.
  (`TestCurrentChangelogEntriesHaveEvidence`)

## 2026-10-10 — TRANSPORT.md: разделены legacy profiles и canonical boundary (документация)

- (документация) Уточнены transport-neutral diagram, legacy Run compatibility
  profiles, transport/executor capability axes, canonical `AuthenticatedFrame`
  requirements, AgentSession and WorkspaceBinding boundaries. Telepathy 0.8.0
  квалифицирована как baseline; `RESULT_ACK` ограничен legacy result path.
  Component registry дополнен `transport-abstraction`.
  (`TestCurrentChangelogEntriesHaveEvidence`)

## 2026-10-10 — TELEPATHY_HOST.md: уточнена граница legacy host/worker (документация)

- (документация) Standalone Telepathy host/worker явно обозначены как legacy
  Run reference profile; добавлены project-local paths, WorkspaceBinding и
  AgentSession границы, snapshot/CanonicalState distinction, wrapper command,
  baseline qualification, delivery profile и ограничения stop proof. Component
  registry синхронизирован; обнаружено, что test harness всё ещё пишет в
  checkout `state/` и его запуск отложен до переноса output path.
  (`TestCurrentChangelogEntriesHaveEvidence`)

## 2026-10-10 — TASK-MODEL.md: актуализация переходов и implementation status (документация)

- (документация) Цель документа отделена от maturity status; TM-06a и TM-26
  приведены к текущим canonical slices; Attempt/RunID, independence reasons,
  AgentSession readiness и replay semantics уточнены. Rework/SUPERSEDED
  отмечен как открытый нормативный конфликт с ADR-0001 и текущей FSM;
  меняющиеся test evidence вынесены в `COMPONENT-STATUS.yaml`.
  (`TestCurrentChangelogEntriesHaveEvidence`)

## 2026-10-10 — TARGET-ARCHITECTURE.md: status vocabulary и evidence registry (документация)

- (документация) `docs/TARGET-ARCHITECTURE.md` синхронизирован с компонентным
  словарём `implemented`/`partial`/`missing`/`proposed`/`legacy`; Run FSM/storage
  и Telepathy baseline помечены `legacy`; production command path отделён от
  store boundary. TaskID отражает test-only creation helper; Shadow Validation
  отмечена `implemented` после `REVIEW_ACCEPTED` и закрытия #42. Сокращено
  повторное Review/Audit evidence,
  уточнены Discussion Graph, Project Dependency Graph и Startup Card. В реестр
  добавлены legacy Run FSM/storage и `task-identity`, а для Shadow Validation
  зафиксированы точные symbols, tests и закрытый Issue #42.
  Mermaid диаграммы оставлены в отдельных Mermaid fences, ссылки исправлены.
  (`TestCurrentChangelogEntriesHaveEvidence`)

## 2026-10-10 — SESSION_CLI.md: legacy CLI и project-local paths (документация)

- (документация) `docs/SESSION_CLI.md` оформлен как руководство по legacy
  `internal/rooms`; примеры перенесены на `COORD_GLOBAL_ROOT`, назначенный
  `COORD_PROJECT_ROOT` и `coord-project-tmp`. Разделены legacy RoleSession,
  canonical AgentSession, WorkspaceBinding, Task assignment и process identity;
  Git history отделён от Operations Ledger, content export ограничен до
  принятия field policy, команды приведены к копируемым shell-блокам. В
  `COMPONENT-STATUS.yaml` добавлен `legacy-role-sessions-cli` с source/test
  evidence. (`TestCurrentChangelogEntriesHaveEvidence`)

## 2026-10-10 — ROLE_SESSIONS.md: legacy boundary и canonical distinctions (документация)

- (документация) `docs/ROLE_SESSIONS.md` и `docs/SESSION_CLI.md` явно
  квалифицированы как legacy `internal/rooms`; разделены `RoleSessionID`,
  canonical `AgentSession`, transport connection, `SessionBindingID` и process
  identity. Уточнены WorkspaceBinding/R143, reassignment, Operations Ledger,
  export allowlist и границы Git push policy. В `COMPONENT-STATUS.yaml`
  добавлен `legacy-role-sessions` с недостающими canonical интеграциями.
  (`TestCurrentChangelogEntriesHaveEvidence`)

## 2026-10-10 — ROLE_PROMPTS.md: State Machine, R142 и WorkspaceBinding (документация)

- (документация) `docs/ROLE_PROMPTS.md`: Coordinator получил явный автономный
  цикл и `OwnerBoundaryCheck`; роли привязаны к WorkspaceBinding, assignment,
  generation и project-local paths; Reviewer/Auditor используют структурированные
  verdicts; handoff и checkpoint привязаны к source revision. Уточнён статус
  legacy `coord prompt`. (`TestCurrentChangelogEntriesHaveEvidence`)

## 2026-10-10 — ROADMAP.md: implementation status и P0-3 entry readiness (документация)

- (документация) `docs/ROADMAP.md`: разведены canonical core, stage boundary и
  production integration; Review/Audit и Discussion Graph разделены по порядку;
  добавлен явный P0-3 readiness snapshot и следующий безопасный шаг. Исправлены
  устаревшие workspace/temp, isolation, replay и legacy process-stop утверждения;
  design prerequisites ADR-0002/ADR-0003 уточнены. (`TestCurrentChangelogEntriesHaveEvidence`)

## 2026-10-10 — QUICKSTART.md: global root и legacy/canonical границы (документация)

- (документация) `docs/QUICKSTART.md`: команды переведены на явный repository
  path и project-local wrapper; примеры legacy Run runtime отделены от
  production canonical workflow; добавлены проверка project handoff, project-local
  agent state и зрелость watchdog/reference host. (`TestCurrentChangelogEntriesHaveEvidence`)

## 2026-10-10 — OPERATIONS.md: workspace, runtime maturity и recovery boundaries (документация)

- (документация) `docs/OPERATIONS.md`: добавлены R143 workspace paths и handoff
  check; разделены canonical core, legacy Run runtime и `coordd` skeleton;
  уточнены not-ready write endpoints, Telepathy 0.8.0 baseline, token modes,
  state ownership и `/metrics`. Backup manifest/restore обозначены как целевой
  contract, не существующая автоматизация. (`TestCurrentChangelogEntriesHaveEvidence`)

## 2026-10-10 — EXPERIMENTS.md: Telepathy legacy baseline и evidence manifest (документация)

- (документация) `docs/EXPERIMENTS.md`: запуск и границы эксперимента уточнены
  для R143; Telepathy 0.8.0 отмечена как исторический baseline; сценарии
  отделены от P0 milestone, результаты привязаны к commit и environment manifest,
  legacy Run-контур отделён от canonical command path. В Architecture Component
  Registry добавлен legacy harness с явно перечисленными ограничениями.
  (`TestCurrentChangelogEntriesHaveEvidence`)

## 2026-10-10 — DEVELOPMENT.md: рабочий процесс и workspace policy (документация)

- (документация) `docs/DEVELOPMENT.md`: руководство синхронизировано с R127,
  R143, Operations Ledger и Architecture Sync; описаны Issue/assignment,
  назначенный worktree, независимые Review/Audit, readiness, commit/push/CI,
  Completion Report и закрытие Issue. Уточнено, что push принятого commit не
  требует отдельного решения OWNER. (`TestCurrentChangelogEntriesHaveEvidence`)

## 2026-10-10 — Defect Patterns Ledger: validation ordering, historical status, and review gate (#43)

- (документация) `docs/DEFECT_PATTERNS.md`: уточнена нормативность и роль
  реестра; примеры помечены статусами; DP-002 разделён на aggregate state,
  runtime resources и ownership scope; DP-003/DP-004 отражают Architecture Sync
  gate как `PARTIAL`; DP-005 требует state-independent validation до чтения
  mutable state, state-dependent checks под lock и `Validation Failure = No
  Observable Mutation`. R104 получил активный evidence backlog в #43.
- (проверки) `scripts/check-defect-patterns` и self-tests добавлены в `make
  check`; тестируются уникальность pattern/check IDs, статус автоматизации,
  наличие тестов и ADR, требования для `FULL`/`NONE` (`TestCurrentChangelogEntriesHaveEvidence`).
- (документация) `docs/operations/ARCHITECTURE-SYNC-PROCESS.md`: добавлен
  DP-001…DP-005 review checklist и defectPatternCheck пример с обязательной
  причиной для N/A.
- Evidence: `TestCurrentChangelogEntriesHaveEvidence`; `python3
  scripts/check-defect-patterns`; `python3 scripts/check-defect-patterns-test`
  (9 self-tests PASS).

## 2026-10-10 — CURRENT_VERSION.md CHANGES_REQUIRED FD cleanup (документация)

- (документация) docs/CURRENT_VERSION.md: FD — исторический документ отделён от
  активного контекста, устаревшие формулировки и потенциальные operational misuse
  устранены.
  - Blocking: добавлен MUST NOT blockquote (нельзя использовать как текущую инструкцию
    запуска, источник Telepathy версии, session config или readiness decision).
  - Blocking: "после завершения P0-2 2026-10-08" заменено — добавлена секция
    "Позднейшая пометка" с коммитами; явно указано "это не означает закрытие всей
    границы P0-2"; из тела статуса убрано утверждение о полном завершении.
  - Blocking: local runtime evidence path помечен как не переносимое доказательство.
  - Significant: "Что запускать" → "Что запускалось"; добавлен preffix "На историческом
    срезе выполнялись:" перед code block.
  - Significant: cmd/coord описан как "legacy daemon/CLI ролевого и rooms-контура",
    явно указано "не является текущим canonical coordd".
  - Significant: Telepathy 0.8.0 помечена как только историческая baseline; добавлено
    предупреждение о том, что текущая версия определяется проектной конфигурацией.
  - Significant: "Real model: false" заменено на естественный язык.
  - Significant: коммиты be7820a и др. вынесены в отдельную секцию "Позднейшая пометка".

## 2026-10-10 — ARCHITECTURE.md CHANGES_REQUIRED FD cleanup (документация)

- (документация) docs/ARCHITECTURE.md: FD — Mermaid code fences и экранирование
  уже были корректны; применены остальные блокирующие и редакционные замечания.
  - Ссылка на TARGET-ARCHITECTURE.md: вынесена на одну строку.
  - Atomic mutation statement: уточнён — "когда эти элементы предусмотрены
    конкретной canonical операцией; store-level boundary реализована; production
    caller path указывается отдельно".
  - Shadow Validation: добавлена ссылка на `docs/ROADMAP.md` как источник порядка.
  - Discussion Graph: добавлена Actor provenance — выводится из AgentSession и
    назначения Run, не принимается из тела команды.
  - Owner Console и Decision Resolver: помечены как "proposed".
  - Секция результата: переписана (blocking #2) — legacy path больше не называется
    "canonical transaction"; раздел разделён на "Canonical Review/Audit", "Canonical
    Executor result path" (#6 реализован) и "Legacy Executor result path".
  - Recovery "остановка writer": заменено на "запрос на остановку writer";
    добавлено "фактическая остановка требует supervisor evidence".
  - Telepathy host: ссылка оформлена как Markdown link.
  - Раздел "Canonical Review и Audit results" → "Canonical ReviewResult and
    AuditResult".
  - Добавлено разграничение Coordinator (legacy Run coordinator) vs coordd (daemon).
  - Backtick identifiers: `next_update_by`, `progress_pct`, `RecoveryNeeded`,
    `ErrFenceExhausted` приведены к стандарту.

## 2026-10-10 — Issue #6 (P0-3): canonical Executor RESULT path (R117/R124)

- (код) task/result.go: добавлены `ExecutorOutcome` (SUCCESS/FAILURE/PARTIAL),
  `ExecutorResult` struct с `Validate()`. Refs #6.
- (код) task/event.go: добавлен `ExecutorResultRecorded` EventType; поле
  `ExecutorResult` в `DomainEvent`; обработка в `Validate()`.
- (код) store/canonical.go: добавлен метод `TaskRevision(id) (uint64, bool)`.
- (код) store/canonical_result.go: добавлен `RecordExecutorResult` —
  атомарная запись результата, DomainEvent и ResultAck через `ApplyInboxCommand`.
  `validateTaskResultEvents` обновлён для Executor (EventSeq, стадия Execution,
  метаданные). `requiresDomainResult` скорректирован: `run.Result == nil ||
  Reviewer || Auditor` (без Executor — canonical path уже охвачен через `Result == nil`).
- (код) store/canonical_discussion.go: удалён placeholder-error; `ResultRef`
  теперь валидируется через `ExecutorResultRecorded` event.
- (код) coordinator.go: добавлен fail-closed gate (R117/R124) — task-scoped Run
  с RESULT envelope маршрутизируется только через `RecordExecutorResult`; при
  отсутствии `CanonicalStore` возвращает ошибку без fallback.
- (тесты) store/canonical_result_test.go: `TestExecutorResultIsStructuredAndTaskScoped`,
  `TestExecutorResultRejectsNonExecutorRun`.
- (тесты) coordinator_test.go: `TestCanonicalExecutorResultGateFailsClosedWithoutCanonicalStore`.

## 2026-10-10 — AGENT_INSTRUCTIONS cleanup: COORD_GLOBAL_ROOT bootstrap, legacy/canonical separation, identity (документация)

- (документация) docs/AGENT_INSTRUCTIONS.md: редакционная пометка добавлена; FD.
  - Добавлен статус-раздел: examples/agent и legacy Run protocol явно отделены от
    canonical ApplyAgentCommand command path.
  - Добавлен раздел «Запуск из COORD_GLOBAL_ROOT»: все сессии из COORD_GLOBAL_ROOT,
    WorkspaceBinding отдельно; обязательные env vars; pre-assignment checklist.
  - Agent bootstrap text: агент MUST использовать transport из assignment, не выбирать
    самостоятельно; убрана формулировка "выбери доступный канал".
  - Registry/supervisor: "настраивает владелец" → "уполномоченный оператор или
    automated deployment; HUMAN_ACTION_REQUIRED, а не OWNER_REQUIRED".
  - CLI section: переименован в "Legacy CLI"; state path исправлен на
    COORD_PROJECT_ROOT/runtime (не внутри checkout); .coord-writer.lock помечен
    как legacy limitation; exit-0/SUCCESS clarification поднята в начало.
  - Telepathy section: помечен как "Legacy 0.8.0 baseline"; note об update-all.mjs.
  - Bridge identity: убрана ambiguous "либо"; transport formirует AuthenticatedFrame;
    coordinator.Handle не является production admission.
  - Добавлен раздел «Process evidence и остановка»: NO_RESPONSE ≠ process exited;
    ADR-0009 process states не текущая capability.

## 2026-10-10 — ADR-0008 normative cleanup: AC-09, CommitIntent gaps marked, Executor/Supervisor, Commit Readiness (документация)

- (документация) ADR-0008-AUTONOMOUS-COMMIT.md: редакционная пометка добавлена;
  FD — документ остаётся Proposed, принятые инварианты AC-01–AC-13 не изменены.
  - AC-02: "push/merge/deploy выполняет OWNER" → "находятся вне области ADR,
    регулируются отдельной policy".
  - AC-06: разделены Git-операция executor и process lifecycle supervisor.
  - AC-09: заменён blanket OWNER_REQUIRED на точный критерий (норма R / trust
    boundary / fail-closed / критерии приёмки); независимый Reviewer обязателен.
  - AC-12: расширен до primary reason + ordered blocking reasons list.
  - AC-14: добавлен как placeholder — CommitIntent idempotency (TD-R2, не определено).
  - Commit Readiness: отделён от execution state; добавлены execution states
    (NOT_STARTED → CONFIRMED/FAILED).
  - Prerequisites: capability-based list; ссылка на RFC-0001 для workspace layout.
  - "Этот ADR вводит" → "После принятия и реализации этот ADR вводит..."
  - CommitIntent protocol, reconciliation, Fence-to-ref binding, replay и
    Git identity/signing отмечены как TD-R2 — не определены, требуют отдельного
    Reviewer + Auditor.

## 2026-10-10 — ADR-0007 normative cleanup: BR-02/BR-07 contradiction, Archived Brief, BR-09 access (документация)

- (документация) ADR-0007-PROJECT-BRIEF.md: редакционная пометка добавлена;
  FD — документ остаётся Proposed, принятые инварианты BR-01–BR-11 не изменены.
  - BR-02: разделены необязательные поля (не включаются) и обязательные
    недостоверные поля (UNKNOWN с причиной согласно BR-07).
  - BR-07: переименован в «обязательное недостоверное значение»; добавлен reason
    code и ссылка на источник проблемы.
  - BR-04: ограничен до Task/Run для MVP; Discussion nodes/edges — будущее расширение.
  - BR-08: разделены write readiness и Brief endpoint readiness.
  - BR-09: явно обозначен как целевое правило; конкретный механизм — ADR-0006 и
    ADR-0009; до реализации: Task Brief MUST NOT выдаваться агентским сессиям.
  - BR-10: уточнено — «только OWNER через external read interface»; внутренние
    функции Coord не являются потребителями Brief.
  - Archived Brief: перемещён из «Виды Brief» в явный non-MVP без определённых
    contracts; убрано обязательное требование к нему из Критериев соответствия.
  - MVP: добавлена норма детерминизма (canonical revision + schema + generator +
    access projection); заменена хрупкая дата «по состоянию на 2026-10-08» ссылкой
    на COMPONENT-STATUS.yaml.
  - Legacy rooms раздел: заменены line references символами и component registry.
  - OWNER/Owner → OWNER унифицированы.

## 2026-10-10 — ADR-0005 normative cleanup: CanonicalState composition, ST-02/ST-07 status, migration manifest (документация)

- (документация) ADR-0005-STORAGE-MODEL.md: редакционная пометка добавлена;
  все правки FD — принятые нормы ST-01–ST-07 не затронуты.
  - Заменён стale вводный блок (canonical model «пока не реализована»); теперь:
    «canonical snapshot и validation реализованы частично, остаются открытыми
    production integration, migrations, backup/restore, TaskID collision».
  - Уточнён состав CanonicalState: добавлены AgentSession/AgentSessionEvents/
    Inbox receipts; явно отделено canonical state от ephemeral session presence.
  - `store.CanonicalPersistence`: уточнено, что это отдельный контракт рядом с
    legacy Run-only, а не надстройка над ним.
  - ST-01: добавлено «durable storage commit, не Git commit»; уточнено, что state
    и Events — части одного document, несогласованность — логическая, не файловая.
  - ST-02: исправлен статус — «один активный writer Run на Task» убран из
    «не реализовано» (уже реализовано); не реализовано: единый production write path.
  - ST-03: уточнена классификация неопределённой ошибки commit.
  - ST-04: укорочен список тестов → 2 ключевых + ссылка на component registry.
  - ST-05: добавлен migration manifest с обязательными полями.
  - ST-06: явно разделены daemon command serialization и store mutex.
  - ST-07: исправлен статус — «не реализовано» → «частично»; генератор реализован,
    transaction-boundary uniqueness отсутствует.
  - Ограничения: исправлено противоречие «монотонно растут / очищаются при recovery»;
    Run.Seen и canonical Inbox receipts разделены по lifecycle.

## 2026-10-10 — ADR-0004 normative cleanup: stale implementation status, readiness clarification, ADR-0009 boundary (документация)

- (документация) ADR-0004-RUNTIME-AND-DAEMON-MODEL.md: редакционная пометка
  добавлена; все правки FD — принятые нормы RT-01–RT-06 не затронуты.
  - Заменён устаревший inline-блок реализации ссылкой на COMPONENT-STATUS.yaml.
  - RT-02: исправлен статус — «не реализовано» → «частично» с test evidence;
    daemon ownership и canonical persistence реализованы частично.
  - RT-03: обновлён статус с оставшимися пробелами (production recovery, stop proof);
    уточнена readiness — явно разделены ordinary mutations, supervisor recovery
    endpoints и diagnostics.
  - RT-04: обновлён статус — canonical command boundary реализована на store model,
    production caller path не подключён; диаграмма оставлена в code block.
  - RT-05: добавлен маркер «целевая read boundary, не product capability до ADR-0006».
  - RT-06: укорочен список тестов — два ключевых + ссылка на component registry;
    добавлено явное упоминание `ApplyAgentCommand`.
  - Process Ownership: добавлен маркер ADR-0009 для Session Registry и process identity.
  - Task.Fence / Run.Fence: унифицированы термины везде в документе.

## 2026-10-10 — ADR-0003 normative cleanup: PriorStatus, EXT-11, provider evidence, TID-01a fix (документация)

- (документация) ADR-0003-EXTERNAL-REFERENCE-MODEL.md: редакционная пометка
  добавлена; все правки FD — принятые инварианты не затронуты.
  - Добавлен `PriorStatus *ExternalReferenceStatus` в структуру `ExternalReference`
    с инвариантом атомарной установки при переходе в `DELETED_EXTERNAL`.
  - EXT-11: добавлен инвариант canonical command boundary с явным порядком шагов.
  - Добавлен подраздел «Идентичность доказательства провайдера»: `ProviderEvidenceID`
    обязателен в событиях deletion/restoration; команды без него отклоняются.
  - Добавлен подраздел «Порядок обработки команд и replay» с явным порядком шагов.
  - TID-01a: исправлена ошибка «128-битный счётчик» → «80-битный случайный
    компонент ULID»; добавлено объяснение 48+80-битной структуры ULID.
  - `EXTERNAL_REFERENCE_UPDATED`: явно указана неизменяемость `Provider`, `Instance`,
    `ExternalID` после `EXTERNAL_REFERENCE_ADDED`.
  - `Metadata`: добавлены ограничения глубины и типов; конкретная схема per-provider.
  - API раздел: добавлен маркер «целевой контракт, не product capability до ADR-0006».
  - Owner → OWNER; owner-команда → OWNER-команда — везде в документе.
  - EXT-10: owner → OWNER.

## 2026-10-10 — ADR-0002 normative cleanup: type matrix, EdgeID identity, DG-07 split, terminology (документация)

- (документация) ADR-0002-PROJECT-DEPENDENCY-GRAPH.md: редакционная пометка
  добавлена; все правки FD — принятые инварианты не затронуты.
  - Добавлена MVP-матрица допустимых пар типов узлов/рёбер (TASK-only active,
    остальные fail-closed до trusted resolver).
  - Добавлены нормы EdgeID: устойчивый уникальный идентификатор, генерируется
    Coord, повтор команды возвращает существующий EdgeID.
  - Уточнена формулировка DG-04: семантика удаления отсутствующего ребра → EDGE_NOT_FOUND.
  - DG-06a: добавлен явный порядок обработки команды (replay lookup → GraphRevision).
  - DG-07 разбит на DG-07a–07e: eligibility первого Run, rework, позднее добавление,
    FAILED prerequisite, atomic replacement.
  - DG-07e: зависимости ADR/MILESTONE в canonical state → readiness blocked fail-closed.
  - DG-08: добавлен маркер «открытый вопрос перед реализацией» для scope capability
    при cross-Task edge — требует OWNER decision до реализации command boundary.
  - API раздел: добавлен маркер «целевой API, не текущая capability до ADR-0006».
  - «Conversation Graph» → «Discussion Graph» везде; UI-проекция явно обозначена.
  - OWNER/Owner → OWNER, OWNER_DECISION унифицированы.

## 2026-10-10 — ADR-0001 normative cleanup: stale status, section split, OWNER escalation fix (документация)

- (документация) ADR-0001-CANONICAL-COORDINATION-MODEL.md: редакционная пометка
  обновлена; исправления строго FD — принятые инварианты не затронуты.
  - CM-10a и CM-11: «не реализовано» → «частично» с конкретными ограничениями.
  - Discussion Graph section: добавлены закрывающие параграфы о FINDING-цепочках
    и transport-обмене; секции CM-12 и CM-13–15 вынесены в отдельные подразделы
    «Owner identity and command authorization» и «AgentSession and agent identity».
  - CM-13: убрана автоматическая эскалация «требует отдельного проекта и решения»;
    заменено на «реализуется отдельной Issue; новое решение OWNER — только при
    изменении принятой semantics, trust boundary или прав».
  - CM-15: уточнён порядок — явно разделены шаг transport (подпись кадра) и шаг
    canonical command boundary (AgentSession, generation, Run, Fence, роль).
  - CM-01: добавлено разграничение `Session` и `AgentSession`.
  - Порядок реализации: обновлён на фактическое состояние P0-2 COMPLETE + P0-3 plan.
  - Последствия: исправлены ложные утверждения о «будущей работе» для уже
    реализованных canonical daemon skeleton, Task model, Domain Events, storage.

## 2026-10-10 — Shadow Validation: read-only canonical vs legacy comparison, ADR-0001 step 10 ([#42](https://github.com/naghuale/coord/issues/42))

- (документация) TARGET-ARCHITECTURE.md: строка Shadow Validation обновлена.
- Новый пакет `shadowval`: ограниченная Shadow Validation по ADR-0001 §686–691
  и ROADMAP. Два допустимых факта: `TaskIDReference` (room.Task совпадает с
  canonical TaskID) и `AgentIDPresence` (AgentID участника присутствует в
  RoleSelection/RoleHistory canonical Run той же Task). Ни один store не
  модифицируется; двойная запись исключена. 12 тестов с отрицательным
  покрытием и мутациями: `TestShadowValidationMatchesTaskIDReference`,
  `TestShadowValidationRejectsInvalidTaskID`,
  `TestShadowValidationMismatchOnUnknownTaskID`,
  `TestShadowValidationMatchesAgentIDPresence`,
  `TestShadowValidationMismatchOnAbsentAgentID`,
  `TestShadowValidationDoesNotCreateRunFromMember`,
  `TestShadowValidationAgentIDMatchIsNotRunEquality`,
  `TestShadowValidationDoesNotMutateDomainEvents`,
  `TestShadowValidationDoesNotMutateTask`,
  `TestShadowValidationIsReadOnly`,
  `TestShadowValidationMatchesAgentIDFromRoleHistory`,
  `TestShadowValidationSkipsRoomsWithoutTask`.

Статус: «реализовано и проверено на canonical + legacy read-only paths;
production write path и import не подключены; приёмка границы этапа #42 OPEN».

## 2026-10-10 — Architecture Sync: COMPONENT-STATUS registry expanded to 9 components ([#41](https://github.com/naghuale/coord/issues/41))

- (документация) `docs/architecture/COMPONENT-STATUS.yaml`: реестр расширен с
  3 до 9 компонентов; добавлены `canonical-persistence` (partial),
  `agent-session` (partial), `review-audit-results` (partial),
  `daemon-runtime` (partial), `project-dependency-graph` (missing),
  `startup-card-read-api` (proposed). Каждый новый компонент: normativeSources,
  implemented/missing, evidence (symbols + tests), anchors в обоих документах.
  TestDiscussionEdgeRequiresRole отсутствует в HEAD — вместо него использован
  `TestDiscussionEdgeRejectsUnknownType`.
- (документация) `docs/ARCHITECTURE.md`, `docs/TARGET-ARCHITECTURE.md`:
  HTML anchors для 6 новых компонентов (canonical-persistence, agent-session,
  canonical-review-audit-results/review-audit-results, daemon-runtime,
  project-dependency-graph, startup-card-read-api).
- (документация) `scripts/check-architecture-sync-test`: фиксируемые docs и
  Go-символы обновлены — покрывают все 9 компонентов; индекс мутации
  `proposed_normative` исправлен с 2 на 7 (startup-card-read-api).

## 2026-10-10 — Architecture Sync: COMPONENT-STATUS registry, validator and CI ([#41](https://github.com/naghuale/coord/issues/41))

- (документация) `docs/architecture/COMPONENT-STATUS.yaml`: единый реестр
  статусов компонентов — `canonical-command-boundary` (partial),
  `discussion-graph` (partial), `session-registry-supervisor` (proposed).
  Каждый компонент: normativeSources, implemented/missing, evidence (symbols +
  tests + issues), anchors в обоих архитектурных документах.
- (документация) `docs/operations/ARCHITECTURE-SYNC-PROCESS.md`: регламент
  синхронизации — 10-шаговый процесс от architecture impact до Completion
  Report; словарь статусов (implemented/partial/missing/proposed/legacy);
  блокирующие ошибки (ARCHITECTURE_MATURITY_INFLATED и др.).
- (документация) `scripts/check-architecture-sync`: валидатор реестра —
  уникальность ID, допустимые статусы, partial требует обоих списков,
  proposed требует normativeForCurrentProduct: false, тесты и символы
  существуют в коде, anchors присутствуют в документах, arch-significant paths
  требуют docs-update или impact manifest.
- (документация) `scripts/check-architecture-sync-test`: 4 самотеста
  валидатора; все 4 PASS при чистом реестре.
- (документация) `.github/workflows/architecture-sync.yml`: CI workflow —
  PR и push в main/p0-2-task; Python 3.12 + PyYAML + check + self-tests.
- (документация) `Makefile`: target `check-architecture-sync` добавлен в
  `make check`.
- (документация) `docs/ARCHITECTURE.md`, `docs/TARGET-ARCHITECTURE.md`:
  HTML anchors для всех трёх компонентов реестра.
- (документация) `.coord/architecture-impact.example.yaml`: шаблон manifest
  для явного N/A при arch-significant изменениях без архитектурного влияния.

## 2026-10-10 — Architecture maps sync: canonical core vs legacy vs production paths ([#41](https://github.com/naghuale/coord/issues/41))

- (документация) `docs/TARGET-ARCHITECTURE.md`: Discussion Graph статус нет →
  частично (canonical core реализован в #30); ReviewResult/AuditResult «Не
  реализовано» обновлено; AgentSession Part 4 разделена на «реализована на
  canonical model» и «production path не подключён»; status definitions
  уточнены (частично = canonical core реализован, production integration не
  закрыта); Section 9 P0 расширена списком реализованных canonical срезов;
  раздел 5 Discussion Graph получил caveat «целевая и частично реализованная
  модель»; раздел 7 Startup Card помечен как зарезервированное направление
  ADR-0006.
- (документация) `docs/ARCHITECTURE.md`: добавлен header с явным разделением
  legacy runtime / canonical core / не подключённые production paths; расширен
  состав `CanonicalState` (AgentSessions, ReviewResult, AuditResult, Discussion
  Graph); «P0-2 завершён» заменено точной формулировкой; раздел «Рабочий цикл»
  переименован в «Legacy Run execution lifecycle» с пояснением; добавлены новые
  разделы: «Canonical agent command boundary», «Canonical Discussion Graph»,
  «Canonical Review и Audit results»; persistence разделён на legacy/canonical;
  recovery разделён на legacy/Task-wide; «Подтверждение результата» уточняет,
  что Executor RESULT production path (#6) не подключён.

## 2026-10-10 — Coordinator State Machine: verdicts, transitions and OWNER boundaries ([#41](https://github.com/naghuale/coord/issues/41))

- (документация) `docs/operations/COORDINATOR-STATE-MACHINE.md`: полная
  спецификация машины состояний координатора — вердикты Executor/Reviewer/Auditor,
  readiness-переходы NOT_READY→COMPLETE, семантика check/mutation/CI результатов,
  контекстные состояния, таблица автономии 13 записей.
- (документация) `docs/operations/schemas/coordinator-state-machine.yaml`:
  машиночитаемая схема; `developmentProcess.status: active`, session/process
  lifecycle отделены как `proposed`, `pending-ADR-0009`,
  `normativeForCurrentProduct: false`.

## 2026-10-10 — AGENTS.md sync: RFC-0001, R142, R145 and Completion Report ([#40](https://github.com/naghuale/coord/issues/40))

- (документация) `AGENTS.md`: убран абсолютный путь `COORD_GLOBAL_ROOT` из
  переносимой нормы R143; переменная пути приведена к
  `${COORD_GLOBAL_ROOT}/${COORD_PROJECT_NAMESPACE}`; AgentID derivation уточнён
  через AgentSession, generation и verified key (ADR-0004).
- (документация) `AGENTS.md`: добавлен guard в раздел Telepathy — раздел не
  разрешает `update-all.mjs` без отдельной задачи.
- (документация) `AGENTS.md`: добавлена полная классификация R142 (FD / TD /
  TD-R2 / R) и условия OWNER_REQUIRED в раздел «Автономия координатора».
- (документация) `AGENTS.md`: добавлен раздел `Project Operations Ledger (R145)` —
  обязательные ops-ветки, append-only записи, запрет secrets в Git.
- (документация) `AGENTS.md`: добавлен раздел «Активный контекст координатора» —
  три слоя (permanent core, current project, task-specific), проверка 6/6 перед
  началом работы.
- (документация) `AGENTS.md`: условие закрытия Issue обновлено — требуется
  независимая приёмка, CI, интеграция и Completion Report; добавлено описание
  формата Completion Report.
- (документация) `AGENTS.md`: правило gate-файлов уточнено — OWNER требуется
  только при изменении нормы класса R, trust boundary или fail-closed; обычные
  правки проходят ревью без OWNER (R127 + R142).

## 2026-10-10 — Wrapper v2: symlink containment fix + test harness ([#37](https://github.com/naghuale/coord/issues/37))

- (документация) `scripts/coord-project-tmp`: v2 — `_safe_mkdir` проверяет
  каждый компонент до записи: symlink check → `mkdir` → `pwd -P` containment
  → `chmod`; устраняет окно, в котором симлинк-атака могла записать файлы за
  пределы `PROJECT_REAL` до срабатывания проверки.
- (документация) `scripts/coord-project-tmp-test`: воспроизводимый test harness;
  23/23 сценария: missing args, invalid project/binding/generation, path blocking
  (`/tmp`, `--flag=/tmp/x`, `-o/tmp/x`), симлинк-escape для всех семи уровней
  (`.scratch`, `tmp`, `tmp/s`, `sessions`, binding, generation, runs).
- (документация) `AGENTS.md`: добавлены подразделы «Реакция на результаты
  Reviewer и Auditor» и «Сценарии автономного продолжения» — фиксируют, когда
  координатор продолжает цикл исправления автономно, а когда требуется OWNER.

## 2026-10-10 — Workspace Root (R143): wrapper v1, policy и AGENTS.md ([#37](https://github.com/naghuale/coord/issues/37))

- (документация) `AGENTS.md`: добавлен раздел `Workspace Root (R143)` — норма запуска
  агентов из `COORD_GLOBAL_ROOT`, containment проекта в `COORD_PROJECT_ROOT`,
  запрет системного temp, identity через AgentID/AgentSession/generation/key.
- (документация) `scripts/coord-project-tmp` v1: `--binding` (обязательный, SessionBindingID),
  `--generation` (обязательный); новый layout temp
  `.scratch/tmp/sessions/<binding>/<generation>/runs/<random>`; socket root
  `.scratch/tmp/s`; `GOCACHE` в `.scratch/go-build`; блокировка `--flag=/tmp/x`
  и `-X/tmp/x`; 17/17 негативных тестов пройдено.
- (документация) `docs/workspace/coord-workspace-policy.yaml` v1: исправлен
  `worktreePath` (`{projectRoot}/worktrees/` вместо `{globalRoot}/worktrees/{project}/`);
  `allowedScopes` сужены до назначенного worktree, task, session temp и runtime
  endpoint; добавлены `operationsWorktree`, `sessionLaunch`, `goBuildCache`,
  `socketRoot`; invocation обновлён на `--binding`/`--generation`.

## 2026-10-10 — RFC-0001: Local Project Workspace, Operations Ledger and Context Management ([#39](https://github.com/naghuale/coord/issues/39))

- (документация) `docs/rfc/RFC-0001-LOCAL-PROJECT-OPERATIONS.md`: зарегистрирован
  Proposed RFC с нормами размещения проектов, ролей агентов, Operations Ledger,
  временных файлов, управления контекстом, Completion Report и восстановления;
  добавлено требование перезапускать активные сессии после обновления integration.
- (документация) `.coord/project.yaml` и `.coord/CONTEXT-INDEX.yaml` ссылаются на RFC.
- (документация) `AGENTS.md`: обновлённый Telepathy требует перезапуска затронутых
  активных сессий; зафиксирован ожидаемый `Hook failed` в Codex до перезапуска.

## 2026-10-10 — Completion Report standard in Operations Ledger

- (документация) `docs/operations/OPERATIONS-LEDGER.md`: добавлена секция
  `Completion Report` с обязательным форматом таблицы и правилами (одно
  сообщение OWNER после завершения задачи, без дублирования деталей из Ledger,
  без лишних вопросов).

## 2026-10-10 — Remove hardcoded /tmp in newSessionCLIHarness ([#38](https://github.com/naghuale/coord/issues/38))

- (тест) `cmd/coord/sessions_test.go`: `os.MkdirTemp("/tmp", "coord-")` заменён на
  `os.MkdirTemp("", "coord-")` — убирает явную ссылку на системный `/tmp`,
  использует OS-дефолтный temp dir через `$TMPDIR`. `t.TempDir()` не подходит:
  добавляет имя теста в путь и превышает лимит Unix socket (104 символа на macOS)
  (`TestSessionBlockAndUnblockUseOwnerCapability`,
  `TestSessionBlockRejectsInvalidArgsBeforeRequest`,
  `TestSessionUnblockReferencesRemainExecutable`).

## 2026-10-10 — Project context portability: CONTEXT-INDEX and four-layer model

- (документация) Добавлена `.coord/CONTEXT-INDEX.yaml` — карта авторитетных источников
  по темам: project state, open work, task model, ADR, workspace binding и др.
- (документация) `.coord/project.yaml`: `sessionCwdVariable: COORD_GLOBAL_ROOT` вместо
  примеров конкретных путей; добавлена секция `contextModel` с четырьмя слоями
  (permanentCore, currentProjectContext, currentTaskContext, archive) и правилом compaction.
- (документация) `docs/operations/schemas/context-check.schema.yaml` — минимальная
  схема проверки полноты контекста координатора (6 probes, requiredFacts, PASS/FAIL).

## 2026-10-10 — Reject whitespace AgentID in ApplyAgentCommand ([#36](https://github.com/naghuale/coord/issues/36))

- (код) Два fail-closed `strings.TrimSpace` check для `session.AgentID` и
  `run.RoleSelection.AgentID`; отклонение с `ErrAgentCommandMissingAgentID` до
  replay, handler и durable write
  (`TestAgentCommandRejectsWhitespaceAgentIDWithoutWriting`, subtests:
  `session_AgentID`, `run_role_selection_AgentID`).
- (документация) TD решение R142/R116 для #36 записано в
  `docs/plans/p0-2-task/AGENT-COMMAND-BOUNDARY.md`; `TrimSpace` используется
  только для проверки пустоты, не для нормализации AgentID.

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
- (документация) UX_AUDIT дополнен запретом использовать исторические команды
  как текущую инструкцию, evidence manifest с ограничениями provenance,
  квалификацией legacy Role Sessions и lifecycle-статусами F1–F4. Персональные
  participant labels и полный отзыв вынесены из основного текста; отзыв
  сохранён локально как private artifact с явно ограниченной provenance.
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
  (`TestCreateTaskGeneratesCanonicalTaskID`). Public create API не добавлен (R52).
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
## 2026-10-10 — GitLab CI integration for Action Map

- Added a reusable GitLab CI include fragment with blocking validation and self-test jobs, plus a root configuration example and integration guide.
- Registered the guide and example in the Document Registry. No active root `.gitlab-ci.yml` exists, so the example is not enabled and GitLab CI Lint remains pending integration with the project's final pipeline. (`TestCurrentChangelogEntriesHaveEvidence`)
## 2026-10-10 — Proposed AI-agent engineering governance RFC

- Added RFC-0002 and its proposed Agent Bootstrap Template to the document registry and navigation map.
- Both remain non-normative until OWNER acceptance; no launcher, session-admission, or authority behavior is enabled by this documentation change. Clarified that Registry/Action Map selection is currently manual, Action Cards are recommended process control, and static validation does not prove model behavior. (`TestCurrentChangelogEntriesHaveEvidence`)
