# Coordinator State Machine

**Status:** Active for repository development process  
**Authority:** R127, R142, R145, RFC-0001  
**Dependencies:** ADR-0001, ADR-0004, ADR-0005  
**Future dependency:** ADR-0009 for proposed session and process lifecycle  
**Issue:** [#41](https://github.com/naghuale/coord/issues/41)  
**Schema:** `docs/operations/schemas/coordinator-state-machine.yaml`

---

This document specifies the active coordinator state machine for repository
development and proposed extensions for future session and process supervision.
The coordinator follows these transitions without asking OWNER when the next
step follows from active norms.

The machine-readable schema is authoritative for identifiers, allowed values,
and transitions. This document is authoritative for rationale and human-readable
requirements. A mismatch between the schema and this document is a defect. Until
reconciled, the more restrictive fail-closed interpretation applies.

---

## Active process specification

All elements in this section are **active** — derived from R142, RFC-0001, and
the Operations Ledger. They apply to the current repository development process.

### Executor verdicts

| Verdict | Coordinator action | OWNER |
|---|---|---|
| `WORK_IN_PROGRESS` | Monitor; do not reassign without cause | No |
| `READY_FOR_REVIEW` | Verify files and hashes; assign Reviewer; for TD-R2 also assign Auditor | No |
| `BLOCKED` | Classify subtype (see below) | Depends |
| `STOPPED` | Executor stopped and does not claim result readiness. Coordinator must: (1) verify actual worktree state; (2) determine if useful work was saved; (3) classify reason; (4) choose resume / handoff / cancellation; (5) not treat process as EXITED; (6) not treat Task as complete. Causes: `TASK_COMPLETED_ELSEWHERE`, `AGENT_UNAVAILABLE`, `TECHNICAL_FAILURE`, `SCOPE_CONFLICT`, `CANCELLED_ASSIGNMENT`, `UNKNOWN_STOP_REASON`. `UNKNOWN_STOP_REASON` leads to fact-gathering, not automatic OWNER. | No |
| `HANDOFF_REQUIRED` | Verify disk state; save work; cancel assignment; assign new executor | No |

**`BLOCKED` subtypes:**

| Subtype | Action | OWNER |
|---|---|---|
| `TECHNICAL_BLOCKER` | Resolve blocker | No |
| `DEPENDENCY_BLOCKER` | Track dependency | No |
| `MISSING_EVIDENCE` | Obtain evidence | No |
| `OWNER_REQUIRED` | Present to OWNER | Yes |
| `EXTERNAL_ACTION_REQUIRED` | Prepare exact command or action for the authorized person (operator, sysadmin, credential owner). OWNER only if action is OWNER-reserved. `EXTERNAL_ACTION_REQUIRED` is not automatically `OWNER_REQUIRED`. | Only if OWNER-reserved |

### Reviewer verdicts

**`ACCEPTED`**

Coordinator must:
- Verify verdict applies to current tree or patch hash.
- Verify Reviewer independence.
- Verify no changes after verdict.
- Continue readiness.

OWNER not required.

**`CHANGES_REQUIRED`**

Coordinator must:
1. Classify each issue as defect / FD / TD / TD-R2.
2. Return to Executor or run fix cycle.
3. Require missing negative tests and mutations.
4. Freeze new version.
5. Repeat review.
6. For TD-R2: repeat independent audit.
7. Continue until `ACCEPTED` or genuine OWNER boundary.

OWNER not required if issues are within Issue scope and follow active norms.

**`NOT_EVALUATED`**

`NOT_EVALUATED` ≠ `PASS` ≠ `FAIL` ≠ `N/A`.

Coordinator must:
- Record exactly what was not checked.
- If check is mandatory: obtain evidence or assign another reviewer.
- If check is optional: record `N/A` with reason.
- If evidence insufficient: leave `NOT_EVALUATED`.
- Never auto-convert `NOT_EVALUATED` to `N/A` or `PASS`.

**`INVALID_SUBMISSION`**

Causes: unknown base SHA; tree changed after submission; patch hash mismatch;
required files missing; issues from different tasks mixed; reviewer received
wrong version.

Coordinator returns to preparation — not OWNER.

### Auditor verdicts

**`AUDIT_ACCEPTED`** — continue readiness. OWNER not required.

**`CHANGES_REQUIRED`** — same autonomous fix cycle as reviewer `CHANGES_REQUIRED`.
Issues touching identity, signing, authorization, AgentSession, fencing, replay,
canonical storage, recovery, containment, process control, credentials, or trust
boundary are classified TD-R2.

**`NOT_EVALUATED`** — not the same as `N/A`. Coordinator obtains missing
evidence or tools.

**`N/A`** — mandatory written reason required:
```yaml
audit:
  status: N/A
  reason: "<why this check does not apply to this change>"
```

**`AUDIT_BLOCKED`** — coordinator resolves technical blocker first. OWNER
required only if blocker crosses R142 boundary.

### Readiness state machine

```
NOT_READY
  → WORK_IN_PROGRESS
  → PRE_REVIEW_BLOCKED
  → READY_FOR_REVIEW
  → REVIEW_IN_PROGRESS
  → READY_FOR_AUDIT          (for TD-R2)
  → AUDIT_IN_PROGRESS
  → READY_FOR_COMMIT
  → COMMITTED
  → PUBLISHED
  → CI_PENDING
  → CI_PASS / CI_FAILED
  → READY_FOR_INTEGRATION
  → INTEGRATED
  → COMPLETE
```

This diagram lists allowed states; not every task must visit every state. A TD
(non-TD-R2) task skips `READY_FOR_AUDIT` and `AUDIT_IN_PROGRESS`. A fix cycle
re-enters `WORK_IN_PROGRESS` from any state after `CHANGES_REQUIRED`.

**Automatic transitions (no OWNER required):**

| Trigger | Action | Next |
|---|---|---|
| Readiness checks pass | Assign Reviewer | `REVIEW_IN_PROGRESS` |
| Reviewer `ACCEPTED`, audit required | Prepare frozen audit submission | `READY_FOR_AUDIT` |
| Reviewer `ACCEPTED`, no audit | Readiness checks | → `READY_FOR_COMMIT` |
| Auditor assigned, frozen submission delivered | Begin audit | `AUDIT_IN_PROGRESS` |
| Auditor `AUDIT_ACCEPTED` | Verify version unchanged; continue readiness | `READY_FOR_COMMIT` or next mandatory gate |
| `CHANGES_REQUIRED` (from Reviewer or Auditor) | Fix → new freeze → repeat review | `WORK_IN_PROGRESS` → `READY_FOR_REVIEW` |
| All checks pass | Create local commit | `COMMITTED` |
| `COMMITTED` | Push | `PUBLISHED` → `CI_PENDING` |
| `CI_PASS` on correct SHA | Integration if permitted | `INTEGRATED` |
| `INTEGRATED` + Completion Report published + Ledger updated + CURRENT/OPEN-WORK updated | Close task | `COMPLETE` |
| `CI_FAILED`: PRODUCT / TEST / DOCUMENTATION | Fix code or test | `WORK_IN_PROGRESS` |
| `CI_FAILED`: INFRASTRUCTURE | Retry per accepted retry policy | `CI_PENDING` |
| `CI_FAILED`: WRONG_SUBJECT_SHA | Run CI for correct SHA | `CI_PENDING` |

### Check results

| Result | Meaning | Rule |
|---|---|---|
| `PASS` | Check confirmed specific property | — |
| `FAIL` | Property violated | Start fix cycle |
| `NOT_EVALUATED` | Check not performed | Must not convert to `PASS` or `N/A` |
| `N/A` | Not applicable | Reason required |
| `INCONCLUSIVE` | Evidence insufficient | Must not convert to `PASS`; identify missing evidence |

### Mutation results

| Result | Rule |
|---|---|
| `KILLED` | Tests detected meaningful break — correct |
| `SURVIVED` | Must determine: real test gap (add test) **or** equivalent mutation (record with evidence) |
| `EQUIVALENT` | Evidence required: `rationale` + `observableBehaviorChanged: false` + `verifiedBy` |
| `NOT_RUN` | Not a successful check |
| `INVALID_MUTATION` | Does not compile, breaks unrelated contract, or doesn't test claimed property |

`EQUIVALENT` bare value is insufficient. Required schema:
```yaml
mutation:
  result: EQUIVALENT
  observableBehaviorChanged: false
  rationale: "<why behavior does not change>"
  verifiedBy:
    - reviewer
```

### CI failure classification

| Class | Action |
|---|---|
| `PRODUCT_FAILURE` | Fix code |
| `TEST_FAILURE` | Fix test or code |
| `DOCUMENTATION_FAILURE` | Fix documentation |
| `INFRASTRUCTURE_FAILURE` | Retry per accepted retry policy only |
| `FLAKY_TEST_SUSPECTED` | Prove flakiness — do not retry blindly until green |
| `WRONG_SUBJECT_SHA` | Run CI for correct commit SHA |

Infrastructure failure must not mask product failure.

### Context states

| State | Meaning | Action |
|---|---|---|
| `CONTEXT_READY` | All 6 probes passed | Proceed |
| `CONTEXT_INCOMPLETE` | Required fact missing | Load only needed source via CONTEXT-INDEX |
| `CONTEXT_STALE` | Source changed after summary | Re-read source |
| `CONTEXT_CONFLICT` | Two sources contradict each other | Apply source hierarchy |
| `RECOVERY_CONFLICT` | Ledger / Issue / refs / CI diverge | Block automatic continuation; reconcile facts; OWNER only if conflict cannot be resolved under applicable authority |

### Decision states

| State | Meaning | Requires |
|---|---|---|
| `FD` | Direct deduction from active norm | Coordinator records |
| `TD` | Reversible technical decision | Independent review |
| `TD-R2` | Technical in identity/signing/authorization/AgentSession/fencing/replay/canonical storage/recovery/containment/process control/credentials/trust boundary | Reviewer + Auditor + negative tests + mutations |
| `R` | New or changed norm | OWNER only |
| `OWNER_REQUIRED` | Proven R142 boundary | — |
| `QUESTION_SUPPRESSED` | OwnerBoundaryCheck found no confirmed R142 boundary | Coordinator must not send question; execute next autonomous transition |
| `HUMAN_ACTION_REQUIRED` | Requires a human operator action (physical, MFA, credential, visual confirmation) but not necessarily OWNER product decision | Prepare exact action for the authorized person; `HUMAN_ACTION_REQUIRED ≠ OWNER_REQUIRED` |
| `FORBIDDEN` | Violates active norm | Cannot authorize by ordinary TD |
| `INSUFFICIENT_EVIDENCE` | Evidence missing | Gather facts first; not ask OWNER |
| `NORM_CONFLICT` | Norms give incompatible answers | Show conflict and options to OWNER |

### OwnerBoundaryCheck gate

Before sending any question to OWNER the coordinator MUST perform an
`OwnerBoundaryCheck`: state the question, list the verified facts, name the
applicable rules, and check each R142 boundary explicitly.

| Boundary | Confirmed | Result |
|---|---|---|
| New or changed norm (`R` class) | Yes | `OWNER_REQUIRED` |
| Norm conflict (active norms give incompatible answers) | Yes | `OWNER_REQUIRED` |
| Trust boundary change | Yes | `OWNER_REQUIRED` |
| Agent rights change | Yes | `OWNER_REQUIRED` |
| Fail-closed weakening | Yes | `OWNER_REQUIRED` |
| Irreversible risk acceptance | Yes | `OWNER_REQUIRED` |
| Different user / product consequences | Yes | `OWNER_REQUIRED` |
| OWNER-reserved action | Yes | `OWNER_REQUIRED` |
| Personal decision (MFA, password, physical confirmation) | Yes | `HUMAN_ACTION_REQUIRED` |
| None of the above confirmed | — | `QUESTION_SUPPRESSED` |

`QUESTION_SUPPRESSED` result: coordinator MUST NOT send the question and MUST
execute the next unambiguous State Machine transition autonomously.

Conditions that do NOT confirm an R142 boundary and therefore produce
`QUESTION_SUPPRESSED`: missing Reviewer, unfinished technical task,
`CHANGES_REQUIRED`, `CI_FAILED`, `CONTEXT_STALE`, handoff needed,
installation of an accepted local tool, choosing the next State Machine
transition, insufficient evidence.

### Coordinator autonomy table

| Result | Action | OWNER |
|---|---|---|
| `READY_FOR_REVIEW` | Assign Reviewer | No |
| `REVIEW_ACCEPTED` | Continue readiness (assign Auditor if TD-R2, else → READY_FOR_COMMIT) | No |
| `AUDIT_ACCEPTED` | Continue readiness → READY_FOR_COMMIT | No |
| `CI_PASS` | Integration if permitted → INTEGRATED | No |
| `CHANGES_REQUIRED` | Fix and repeat review | No |
| `NOT_EVALUATED` | Get evidence or another reviewer | No |
| `CI_FAILED` | Classify then fix | No |
| `SURVIVED` | Check gap or equivalence | No |
| `CONTEXT_STALE` | Re-read source | No |
| `HANDOFF_REQUIRED` | Save and transfer work | No |
| `INSUFFICIENT_EVIDENCE` | Gather facts | No |
| `NORM_CONFLICT` | Prepare options for OWNER | **Yes** |
| `R` | Transmit exact question to OWNER | **Yes** |
| Trust boundary change | Present change and options to OWNER | **Yes** |
| Agent rights change | Present change and options to OWNER | **Yes** |
| Irreversible risk | Prepare options for OWNER | **Yes** |
| Different user consequences | Prepare options for OWNER | **Yes** |
| OWNER-reserved action | Transmit to OWNER | **Yes** |
| MFA / password required | Prepare exact human action | **Yes** |
| Fail-closed weakening | Do not execute without decision | **Yes** |

### Commit blocked reasons

`REVIEW_MISSING` · `AUDIT_MISSING` · `MANDATORY_CHECK_FAILED` ·
`STAGED_DIFF_MISMATCH` · `FILES_CHANGED_AFTER_REVIEW` ·
`GATE_REVIEW_MISSING` · `ISSUE_SCOPE_VIOLATION`

---

## Proposed: session lifecycle

**Status:** Proposed — pending ADR-0009 (Session Supervisor).  
**`normativeForCurrentProduct: false`**

These states are provided for design examples, threat modeling, and future
planning. They must not be used as proof of current Coord state, basis for
process control, or declared as current product capabilities.

**States:** `DISCOVERED` · `VERIFIED` · `ADMITTED` · `ACTIVE` · `IDLE` ·
`SUSPECT` · `QUARANTINED` · `REVOKED` · `STALE`

**Rules:**
- `NO_RESPONSE` → `SUSPECT` (not `EXITED`).
- `OWNER_ATTESTED_SESSION` allows continued admission; does not revalidate old
  Task assignment.

---

## Proposed: process lifecycle

**Status:** Proposed — pending ADR-0009.  
**`normativeForCurrentProduct: false`**

Process state must not mix with session state. Transport visibility is not
readiness. PID is not identity.

**States:** `UNKNOWN` · `RUNNING` · `TERMINATION_PENDING` · `EXITED`

---

## References

| Source | Governs |
|---|---|
| RFC-0001 (ACCEPTED) | Operations Ledger, roles, context, Completion Report |
| R142 (AGENTS.md) | FD/TD/TD-R2/R classification, OWNER boundaries |
| R143 (AGENTS.md) | Workspace containment |
| R145 (AGENTS.md) | Operations Ledger branches |
| ADR-0001 | Canonical model, task model |
| ADR-0004 | Runtime, fencing, write path |
| ADR-0005 | State storage, commit |
| ADR-0009 (Proposed) | Session Registry, Process Supervisor |
