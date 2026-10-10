# AIAG-03: Реализовать ActionContext resolver

## Goal

Deterministic resolver, schema validation and negative tests pass.
## Scope

Implement deterministic event-to-document resolution and emit schema-valid ActionContext.

## Acceptance criteria

- [ ] Unknown event fails closed
- [ ] Required documents are selected by `readWhen` and authority
- [ ] Historical, legacy and proposed misuse is rejected
- [ ] Document digests are bound
- [ ] Context budget is enforced without silent truncation


## Planning estimate

```yaml
priority: P0
gitlabWeight: 8
complexityRange: 8-13
estimateType: relative_points
confidence: medium
reestimateWhen:
  - scope changes
  - external dependency changes
  - accepted norm changes
  - provider or GitLab capability differs from assumption
```

> `gitlabWeight` is a relative planning value, not elapsed time. The range records uncertainty; GitLab stores the single lower-bound weight.

## Required metadata

```yaml
projectId: coord
decisionClass: TD-R2
priority: P0
milestone: M2-admission
blockedBy: [AIAG-02]
ownerRequired: false
review: required
audit: required
architectureImpact: required_to_evaluate
```

## Definition of Ready

- [ ] Owning sources identified
- [ ] Exact base SHA recorded
- [ ] WorkspaceBinding and allowed paths recorded
- [ ] Expected outputs recorded
- [ ] Required tests and negative controls recorded
- [ ] Independent Reviewer assigned
- [ ] Auditor assigned when required
- [ ] Stop conditions recorded

## Verification

- [ ] Unit/static checks
- [ ] Negative tests
- [ ] Mutation or fault-injection evidence where applicable
- [ ] `git diff --check`
- [ ] Full gate through project-local wrapper
- [ ] Exact-version Review
- [ ] Exact-version Audit or justified N/A
- [ ] Architecture Sync

## Completion

- [ ] Integrated SHA recorded
- [ ] GitLab pipeline recorded
- [ ] Operations Ledger updated
- [ ] CURRENT and OPEN-WORK updated
- [ ] Completion Report published
- [ ] Issue closed only after all mandatory gates
