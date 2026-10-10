# AIAG-11: Сформировать и принять behavioral baseline

## Goal

current.json accepted independently and store validation passes.
## Scope

Generate `current.candidate.json`, review aggregation and promote accepted `current.json`.

## Acceptance criteria

- [ ] Source run digests verified
- [ ] Median recomputed independently
- [ ] Review status ACCEPTED
- [ ] Audit accepted or justified N/A
- [ ] Baseline store checker passes


## Planning estimate

```yaml
priority: P1
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
decisionClass: TD-R2
priority: P1
milestone: M5-baseline
blockedBy: [AIAG-10]
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
