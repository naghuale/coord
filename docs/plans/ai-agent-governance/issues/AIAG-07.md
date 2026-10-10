# AIAG-07: Выполнить adapter conformance, Review и Audit

## Goal

Immutable digest recorded and adapter status APPROVED.
## Scope

Run adapter conformance and independently approve one immutable implementation digest.

## Acceptance criteria

- [ ] Executable or image digest recorded
- [ ] Credentials absent from requests, logs and artifacts
- [ ] Timeout and cancellation bounded
- [ ] Invalid output rejected
- [ ] Usage telemetry or explicit NOT_EVALUATED
- [ ] Review accepted
- [ ] Audit accepted


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
milestone: M3-adapter
blockedBy: [AIAG-06]
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
