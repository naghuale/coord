# RA-1: Review/Audit result core

RA-1 adds canonical typed Review and Audit results to DomainEvent schema v1.
Result acceptance commits the typed result, Task transition to `BLOCKED` at the
same stage, terminal role Run, DomainEvent, Inbox receipt and `RESULT_ACK`
Outbox entry through one Store transaction. Startup validation checks the event
payload, Task/Run/role/Attempt association and references. The wait reason is
the projection `WAITING_FOR_SUPERVISOR_STOP`, derived from the missing committed
stop proof; RA-1 does not add a BlockReason field or implement stop proof.

Finding IDs and timestamps are assigned by Coord. Verdict sets are closed, and
Finding severity does not affect the Task transition. Finding evidence may
reference `TASK`, same-Task `RUN`, and earlier same-Task `DOMAIN_EVENT` objects;
these are validated. `ARTIFACT` refs fail closed with `artifact metadata not
defined`. Result input/output artifact-ref fields are present but must remain
empty because canonical Artifact metadata and its ownership rules remain
undefined. This follows ADR-0001:240/244 and requires a separate OWNER decision
before Artifact refs can be accepted.

Discussion Graph, conflict/escalation, role independence, production command
authentication, and supervisor stop proof remain outside RA-1.

The internal `RecordReviewResult` and `RecordAuditResult` methods do not accept
an inbound Task Fence, Attempt, or sender session. They therefore cannot
independently reject a delayed result from a prior owner after recovery transfers
the same RunID to a new OwnerSession/Fence. Enforcing current Task ownership on
incoming writes remains the daemon command-boundary requirement in
[TASK-MODEL.md, “Task-wide Fence and ownership”](../../TASK-MODEL.md#task-wide-fence-и-ownership)
(pp. 216–218); it is outside RA-1 and must be enforced before this internal
recording path is exposed to production commands.
