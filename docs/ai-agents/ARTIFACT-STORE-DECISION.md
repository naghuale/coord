# Artifact Store Decision

## Decision

Use a two-tier design:

1. GitLab job artifacts for short-lived per-pipeline raw and normalized results.
2. A versioned Git baseline store for redacted run summaries and promoted
   baselines.

An external immutable store is **not required for initial adoption**, because
raw evaluation results are not release artifacts and the existing design binds
baselines to source runs and independent Review.

An external immutable store becomes required when any of these is true:

- retention must outlive GitLab artifact expiry independently of Git history;
- audit policy requires write-once retention;
- baseline evidence must survive project deletion or forceful administration;
- multiple projects consume the same signed baseline;
- regulatory or incident requirements demand independent custody.

## Recommended future option

Use the GitLab Generic Package Registry for versioned baseline bundles when a
stable cross-pipeline URL and lifecycle independent of one pipeline are needed.
Job artifacts remain appropriate for pipeline-bound, expiring outputs. GitLab
explicitly distinguishes these uses.

## Important limitation

The Generic Package Registry provides versioning and CI integration, but this
document does not claim write-once immutability. Duplicate-package policy,
permissions, retention and deletion controls must be verified on the actual
GitLab instance before calling the store immutable.

For stronger integrity, publish a digest manifest and provenance describing
where, when and how the bundle was produced. SLSA defines provenance as
verifiable information tracing an artifact to its production process.
