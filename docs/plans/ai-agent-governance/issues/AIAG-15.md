# AIAG-15: Принять решение по внешнему artifact store

## Goal

Decision records retention/custody trigger and selected backend or deferral.
## Decision

Decide whether Git and pipeline artifacts remain sufficient or whether stable cross-project retention/write-once custody requires a package registry or external store.

## Acceptance criteria

- [ ] Retention and deletion requirements recorded
- [ ] Duplicate and permission behavior verified on actual GitLab instance
- [ ] Provenance/digest requirements recorded
- [ ] Decision is implement, defer with trigger, or reject


## Planning estimate

```yaml
priority: P2
gitlabWeight: 3
complexityRange: 3-8
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
priority: P2
milestone: M6-hardening
blockedBy: [AIAG-11]
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
