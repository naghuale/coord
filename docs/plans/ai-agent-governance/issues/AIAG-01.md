# AIAG-01: Принять RFC-0002 и зафиксировать нормативный scope

## Goal

OWNER decision, Review and Audit references recorded.
## Scope

Accept, revise or reject RFC-0002. Record accepted and excluded scope, effective commit, Review and Audit references.

## Acceptance criteria

- [ ] OWNER decision ID recorded
- [ ] RFC status changed atomically
- [ ] AGENTS.md synchronized
- [ ] Document Registry synchronized
- [ ] No Proposed mechanism described as already enforced


## Planning estimate

```yaml
priority: P0
gitlabWeight: 3
complexityRange: 3-5
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
decisionClass: R
priority: P0
milestone: M0-governance
blockedBy: []
ownerRequired: true
review: required
audit: conditional_or_NA
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
