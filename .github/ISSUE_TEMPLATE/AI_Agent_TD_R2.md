<!-- Security, identity, authorization, fencing, replay, storage, recovery or irreversible side-effect change. -->
/label ~"ai-agent-governance" ~"type::security" ~"decision::td-r2" ~"gate::review" ~"gate::audit"

# TD-R2 change

## Security property

```yaml
property:
threatOrFailureMode:
failClosedRequirement:
subjectBinding:
```

## Scope and boundaries

```yaml
baseSha:
allowedPaths: []
prohibitedPaths: []
realSideEffects: false
```

## Required negative cases

- [ ] Invalid identity
- [ ] Stale generation or fence
- [ ] Wrong subject SHA
- [ ] Replay or duplicate
- [ ] Partial write / recovery
- [ ] No side effect on rejection

## Evidence

```yaml
observed: []
derived: []
reported: []
unknown: []
```

## Acceptance

- [ ] Independent Reviewer accepted exact subject
- [ ] Independent Auditor accepted exact subject
- [ ] Mutation/fault injection demonstrates test sensitivity
- [ ] No `REPORTED` or `UNKNOWN` claim passes a gate
