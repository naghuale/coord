<!-- Behavioral baseline candidate and promotion issue. -->
/label ~"ai-agent-governance" ~"area::baseline" ~"gate::review"

# Behavioral baseline promotion

## Compatibility identity

```yaml
evaluationProfile:
modelAdapterId:
modelAdapterVersion:
modelAdapterDigest:
scenarioSuiteDigest:
resultSchemaVersion:
metricSemanticsVersion:
```

## Source runs

- [ ] At least five compatible runs
- [ ] Hard violations equal zero
- [ ] `NOT_EVALUATED` equals zero
- [ ] Source-run digests verified
- [ ] Redaction passed

## Candidate

```yaml
path: ci/baselines/action-map/current.candidate.json
digest:
aggregation: median
reviewStatus: REQUIRED
```

## Acceptance

- [ ] Median recomputed independently
- [ ] Reviewer accepted candidate digest
- [ ] Auditor accepted or N/A reason recorded
- [ ] `current.json` written only after acceptance
- [ ] Baseline store checker passes
