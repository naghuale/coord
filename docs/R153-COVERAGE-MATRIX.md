# R153 coverage after Assignment Ledger MVP (#90)

Implementation evidence table, not a new norm or a claim of full R153.
Status applies to this slice; Review/Audit on the frozen SHA still required.

| WP / subrequirement | Status | Code / tests | Remaining work |
|---|---|---|---|
| WP-R153-04 exact assignment identity, state/TTL/generation preflight | IMPLEMENTED | assignment.Record/Ready; store.CheckAssignmentWork; TestAssignmentNoWorkBeforeActiveAndAckInsufficient, TestAssignmentExactIdentityGenerationAndSubject, TestAssignmentExpiredValidUntilDeniedBeforeTimer, TestAssignmentPreflightAndWorkStartedRequireIssuer | Actual runtime must call preflight; no universal pre-tool hook |
| WP-R153-04 no transport admission / terminal refusal | IMPLEMENTED | Ledger.Append/ValidateTransition; ApplyAgentCommand guard; TestAssignmentTerminalNeverRevives, TestAssignmentTransportCommandCannotChangeAuthority, TestAssignmentOe4StaleLateDeliveryAfterRestartDoesNoWork (stale preflight after restart only; no late frame injection) | External listener injection prevention remains WP-AUDIT-RUNTIME-01 |
| WP-R153-04 admission basis / operator visibility evidence | PARTIALLY_IMPLEMENTED | Record.Validate exact target/role/SHA/reference, canonical AgentSession checks | Full eligibility/pool/R151/OWNER waiver verification, verified model family, participation/history and visibility observation adapters are not implemented |
| WP-R153-04 all R151/R153 replacement/fallback eligibility | NOT_IMPLEMENTED | immutable no-reuse IDs only | Full accepted policy evaluator, scoped waivers, external evidence/mapping |
| WP-R153-06 durable assignment records and append-only history | IMPLEMENTED | CanonicalState.assignment_ledger; assignment.Validate/ValidateTransition; TestAssignmentOldSnapshotCompatibleAndHistoryAppendOnly | No journal import (explicit cutover rule); general projections/analytics deferred |
| WP-R153-06 loss/handoff preserving issuer/clock | PARTIALLY_IMPLEMENTED | ObserveAssignmentCoordinatorLoss, HandoffAssignment; TestAssignmentCoordinatorLossAndVerifiedHandoff; TestAssignmentHandoffRequiresCurrentPredecessor (rotated/revoked predecessor and normal handoff); TestAssignmentLossRequiresCurrentObserver (rotated/revoked observer); TestAssignmentLossRecoveryWithRotatedCoordinator (active observer/successor, stale subject, restart); TestAssignmentSessionRotationBlocksWorkAndResult covers work/result only; TestAssignmentLossRejectsCoordinatorAndTarget; TestAssignmentLossBatchRejectsAnyAffectedTarget; TestAssignmentRecoveryRejectsTargetAndOpenLossObserver; TestAssignmentRecoveryUsesOnlyOpenLossObserver; TestAssignmentReplayRejectsSelfInterestedRecovery | Physical host/coordinator loss detector and automatic reconciliation not implemented; trusted explicit observation/recovery required. #93 must reject an unprivileged third-party loss observer before commit (TestWriteAuthorityRejectsUnprivilegedLossObserver, future reference only) |
| WP-R153-06 atomic result and terminal completion | IMPLEMENTED | AcceptAssignmentResult and applyAssignmentResultCommand; TestAssignmentDuplicateResultAcceptedOnlyOnceAtomically, TestAssignmentFailedCommitPublishesNeitherResultNorCompletion, TestAssignmentTypedResultGateAtomicWithTask | Wider provenance dispositions/every production role ingress remain WP-R153-05; receipt is not blanket verdict acceptance |
| WP-R153-06 R154 incident/fallback/escalation journal | NOT_IMPLEMENTED | immutable validUntil/issuer; handoff cannot reset them | Incident clock computation and full R148/R154 incident journal belong to later work; no changed intervals or automatic clock inference |
| WP-R153-06 same-store read/compatibility/frozen rollback | IMPLEMENTED | coord assignment status; TestAssignmentStatusReadOnlyAndFailClosed, TestAssignmentStatusUnknownUnavailableCorruptAreDistinct, TestAssignmentStatusLaunchRollbackPreservesLedger, TestAssignmentLedgerRollbackFrozenMode, TestAssignmentFrozenStateTable | Old binary writable downgrade not supported; new binary frozen mode preserves service/data |

Operational cutover: BLOCKED_ON_WRITE_AUTHORITY, deliberately deferred by OWNER
option (a) to follow-up #93 (COMMAND-BOUNDARY §3.2).
No direct canonical write CLI or legacy credential reuse is authorized.

Overall WP-R153-04: PARTIALLY_IMPLEMENTED. Overall WP-R153-06:
PARTIALLY_IMPLEMENTED. WP-R153-01/02/03 full registry/source adapters/model mapping,
WP-R153-05 full evidence dispositions and WP-R153-07 full offline recovery remain
outside this implementation. No full R153, #89 five-dimension or production-readiness
claim follows from ledger test coverage. Review and the OWNER-authorized
ROLE_CONFLICT_DISCLOSED audit remain separate gates.
