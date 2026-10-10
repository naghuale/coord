# Behavioral Baseline Acceptance

## Candidate creation

A candidate is generated only from at least five compatible runs with:

- matching suite digest, profile, adapter ID and schemas;
- zero hard violations;
- zero `NOT_EVALUATED`;
- source-run references;
- median aggregation.

## Independent acceptance

1. Generator creates `current.candidate.json` with `review.status: REQUIRED`.
2. Reviewer verifies source files, compatibility key, aggregation and hard metrics.
3. Auditor is required if adapter, evidence, authority or baseline-store security changed.
4. Coordinator records Review/Audit references.
5. Candidate is copied to `current.json` with `review.status: ACCEPTED`.
6. Baseline-store CI validates references and exact digest.

The baseline author cannot accept the candidate. OWNER is not required unless
promotion changes norms, hard gates or accepted semantics.

## Rejection and revocation

Reject when source runs are missing, incompatible, redaction failed, metrics
semantics changed or any hard violation is non-zero. Revoke after adapter
revocation, suite/schema incompatibility, discovered evidence corruption or
hidden-case leakage.
