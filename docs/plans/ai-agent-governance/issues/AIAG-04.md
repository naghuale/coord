# AIAG-04: Реализовать launcher admission

## Goal

Invalid admission blocks agent launch fail-closed.
## Scope

Prevent real agent launch without valid bootstrap, assignment, WorkspaceBinding and ADMITTED ActionContext.

## Acceptance criteria

- [ ] Missing bootstrap blocks start
- [ ] Invalid admission blocks start
- [ ] Base SHA mismatch blocks start
- [ ] Worktree outside project root blocks start
- [ ] Side-effect policy must be explicit


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
blockedBy: [AIAG-03]
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
