# Coordinator workflows

Generated from COORDINATOR-WORKFLOWS.yaml. Proposed; no production activation.

| Workflow | Canonical event | Canonical transition | Observation | Priority | Workflow outcome |
|---|---|---|---|---:|---|
| start-ready-work | CURRENT_ISSUE_CLOSED | CONTINUE_AUTONOMOUSLY | READY_ISSUE_OBSERVED | 20 | READY_WORK_PLAN_CREATED |
| replace-non-admitted-session | AGENT_ASSIGNMENT_DISPATCHED | ADVANCE_DISPATCHED_WORK | ASSIGNMENT_DELIVERY_OBSERVED | 50 | NON_ADMITTED_REPLACEMENT_PLAN_CREATED |
| reject-stale-assignment-response | AGENT_ASSIGNMENT_DISPATCHED | ADVANCE_DISPATCHED_WORK | AGENT_RESPONSE_OBSERVED | 65 | STALE_RESPONSE_REJECTED |
| reconcile-stale-heartbeat | AGENT_ASSIGNMENT_DISPATCHED | ADVANCE_DISPATCHED_WORK | HEARTBEAT_STALE_OBSERVED | 60 | SESSION_RECONCILE_PLAN_CREATED |
| replace-after-failed-reconcile | AGENT_ASSIGNMENT_DISPATCHED | ADVANCE_DISPATCHED_WORK | RECONCILE_FAILURE_OBSERVED | 55 | FAILED_RECONCILE_REPLACEMENT_PLAN_CREATED |
| replace-blocked-session | AGENT_ASSIGNMENT_DISPATCHED | ADVANCE_DISPATCHED_WORK | SESSION_EXECUTION_BLOCK_OBSERVED | 45 | BLOCKED_SESSION_REPLACEMENT_PLAN_CREATED |
| prepare-physical-frontend-action | HUMAN_ACTION_NEEDED | HUMAN_ACTION_REQUIRED | PHYSICAL_FRONTEND_BLOCK_OBSERVED | 80 | HUMAN_FRONTEND_CARD_PLANNED |
| freeze-unreviewed-change | CODE_OR_DOCUMENT_CHANGED | FREEZE_FOR_REVIEW | UNREVIEWED_CHANGE_DENIAL_OBSERVED | 40 | UNREVIEWED_SUBJECT_FREEZE_PLANNED |
| prepare-accepted-write-human-action | HUMAN_ACTION_NEEDED | HUMAN_ACTION_REQUIRED | ACCEPTED_WRITE_BLOCK_OBSERVED | 79 | EXTERNAL_WRITE_HUMAN_CARD_PLANNED |
| prepare-proven-owner-boundary | OWNER_QUESTION_CANDIDATE | OWNER_REQUIRED | NORMATIVE_BOUNDARY_OBSERVED | 90 | OWNER_TRACE_PLANNED |
| record-proven-external-blocker | BLOCKER_DETECTED | BLOCKED_EXTERNAL | EXTERNAL_BLOCKER_OBSERVED | 70 | EXTERNAL_BLOCKER_RECORD_PLANNED |
| prepare-review-correction | REVIEW_CHANGES_REQUIRED | PREPARE_CORRECTION | REVIEW_FINDING_OBSERVED | 30 | REVIEW_CORRECTION_PLAN_CREATED |
| correct-technical-ci-failure | CI_FAILED | CONTINUE_AUTONOMOUSLY | CI_FAILURE_OBSERVED | 30 | CI_CORRECTION_PLAN_CREATED |
| recover-unknown-work-readonly | WORKTREE_STATE_UNKNOWN | RECOVER_COORDINATION_STATE | UNKNOWN_WORK_OBSERVED | 10 | READONLY_RECOVERY_PLAN_CREATED |
| preserve-unsafe-unknown-work | WORKTREE_STATE_UNKNOWN | PRESERVE_AND_STOP | UNKNOWN_WORK_OBSERVED | 100 | PRESERVATION_STOP_PLAN_CREATED |

Observations and outcomes are local namespaces. Plans do not grant mutation authority. Only stale-response rejection has a pure local executor; external actions and daemon wiring are not implemented by this catalog.

## Completion policy boundary

COORDINATOR-COMPLETION-POLICY.yaml is a separate typed policy. ValidateCompletionDecision checks evidence binding, freshness, existing OWNER/HUMAN reason allowlists and all core turn guards. A Human Action requires observed absence of automation, replacement and internal remedies plus a complete command/post-check/failure card whose digest matches the accepted action. A boundary flag never skips completion checks.

Observation records and Facts must be supplied by a trusted canonical/approved-adapter projection, never parsed from model text. This library checks structured provenance; it does not authenticate an Inbox sender, establish the truth of a normative argument, or implement that production projection. Existing watchdog completion and coordd callers are unchanged.
