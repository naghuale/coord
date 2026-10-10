# Priority and estimation standard

## Priorities

| Priority | Meaning | Admission rule |
|---|---|---|
| `P0` | Blocks RFC adoption, fail-closed enforcement, approved adapter, or MR behavioral gate | Schedule before dependent work |
| `P1` | Required for baseline, nightly evaluation, retention, hidden-case hardening | Start after P0 predecessor gates |
| `P2` | Economics or storage evolution with a safe initial fallback | Start after measured behavioral operation |

Priority is based on dependency and risk, not on the person assigned to the issue.

## Relative estimates

Use GitLab issue weight as a relative complexity value:

```text
1  = isolated documentation or configuration adjustment
2  = small bounded change with direct tests
3  = contained feature or decision package
5  = multi-file implementation with CI and negative tests
8  = cross-component change, migration, or TD-R2 verification
13 = large slice with external integration or several independent gates
21 = split required before Ready unless the issue is an explicit tracking item
```

Weights are not durations. No calendar estimate is implied. Each issue also records a complexity range to expose uncertainty. Re-estimate after scope, external dependencies, norms, provider capabilities, or GitLab instance behavior change.

## Ready rule

An implementation issue with weight `21` must be decomposed before `Ready`. Class `R` issues may retain a broad range because the decision can change downstream scope, but the implementation issues created by the decision must be independently estimated.
