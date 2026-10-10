# AIAG-13: Настроить защищённое hidden-case хранилище

## Goal

Protected source, redacted failures and leakage rotation tested.
## External dependency

Requires a protected GitLab project/file variable, Vault or equivalent restricted source. Public repository storage is prohibited.

## Acceptance criteria

- [ ] Hidden body inaccessible to evaluated agent
- [ ] Expected result frozen before invocation
- [ ] Failure output redacted
- [ ] Leakage causes rotation
- [ ] Hidden case cannot introduce a hidden norm


## Planning estimate

```yaml
priority: P1
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
priority: P1
milestone: M6-hardening
blockedBy: [AIAG-07]
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
