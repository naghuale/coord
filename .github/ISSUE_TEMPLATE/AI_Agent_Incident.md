<!-- Process or agent-system incident. Do not use for employee evaluation. -->
/label ~"ai-agent-governance" ~"type::security" ~"priority::p0"

# Incident

## Observed event

```yaml
observedAt:
subjectSha:
affectedGate:
sideEffectsKnown:
```

## Immediate containment

- [ ] Mutations stopped fail-closed
- [ ] Evidence preserved in project-local restricted storage
- [ ] Ownership not cleared as a substitute for fencing
- [ ] Credentials rotated if exposure is confirmed

## Evidence classification

```yaml
observed: []
derived: []
reported: []
unknown: []
```

## Correction

- [ ] Owning norm identified
- [ ] Defect issue created
- [ ] Regression scenario added
- [ ] TD-R2 Audit scheduled where required
