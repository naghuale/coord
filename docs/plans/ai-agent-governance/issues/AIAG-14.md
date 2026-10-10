# AIAG-14: Включить token и cost telemetry

## Goal

Observed token usage, prices and profile cost reports available.
## External inputs

Provider prices, runner price, currency and adapter token telemetry. Missing inputs produce NOT_EVALUATED.

## Acceptance criteria

- [ ] Input and output tokens observed
- [ ] Pricing configuration versioned
- [ ] Profile cost emitted
- [ ] Cost cannot relax safety gate


## Planning estimate

```yaml
priority: P2
gitlabWeight: 5
complexityRange: 5-8
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
decisionClass: TD
priority: P2
milestone: M6-hardening
blockedBy: [AIAG-07, AIAG-09]
ownerRequired: false
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
