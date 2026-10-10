<!-- AI-agent implementation issue. Fill every required field before status::ready. -->
/label ~"ai-agent-governance" ~"type::feature" ~"status::open"

# Summary

## Goal

<!-- One verifiable outcome. -->

## Event and owning sources

```yaml
actionMapEvent:
componentIds: []
owningNorms: []
currentStatus:
targetStatus:
```

## Classification

```yaml
decisionClass: FD | TD | TD-R2
priority: P0 | P1 | P2
gitlabWeight: 1 | 2 | 3 | 5 | 8 | 13
complexityRange:
ownerRequired: false
architectureImpact: REQUIRED | N/A
```

## Dependencies

- Blocked by:
- Blocks:
- External dependencies:

## Assignment boundary

```yaml
baseSha:
workspaceBinding:
allowedPaths: []
readOnlyPaths: []
prohibitedPaths: []
toolsAllowed: []
realSideEffects: false
expectedOutputs: []
stopConditions: []
```

## Acceptance criteria

- [ ] Functional behavior implemented
- [ ] Fail-closed behavior implemented
- [ ] Required documents updated
- [ ] No maturity inflation

## Verification

- [ ] Positive tests
- [ ] Negative tests
- [ ] Mutation/fault evidence where applicable
- [ ] Full gate through project-local wrapper
- [ ] `git diff --check`
- [ ] Architecture Sync

## Review and Audit

```yaml
review: required
audit: required_for_TD-R2_or_NA_with_reason
frozenSubjectHash:
```

## Completion

- [ ] Integrated SHA recorded
- [ ] GitLab pipeline recorded
- [ ] CURRENT / OPEN-WORK / Ledger updated
- [ ] Completion Report published
