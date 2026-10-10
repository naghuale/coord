# Model Adapter Standard

## Decision

The approved interface is `coord-model-adapter/v1`. Approval applies to the
interface and validation contract. No provider-specific implementation is
approved by this document.

An implementation becomes approved only after it has:

- a stable adapter ID and semantic version;
- a content digest for its executable or container image;
- a declared model profile;
- deterministic input and output envelopes;
- token and cost reporting or explicit `NOT_EVALUATED`;
- timeout, cancellation and output-size limits;
- no real side effects in evaluation mode;
- independent Review and required Audit;
- a passing conformance suite.

## Invocation

```text
<adapter executable> evaluate \
  --request <AgentInvocationRequest JSON> \
  --response <AgentInvocationResponse JSON>
```

The adapter MUST NOT receive provider credentials in request files. Credentials
are injected by the trusted runner and MUST NOT appear in artifacts or logs.

## Required identity

```yaml
adapter:
  id: "<immutable implementation identity>"
  version: "<semver>"
  digestAlgorithm: sha256
  digest: "<64 lowercase hex characters>"
  distribution: executable | container
  sourceRevision: "<Git SHA or package version>"
```

For a local executable, digest is SHA-256 of the exact file. For a container,
use the immutable image digest, not a mutable tag.

## Approval states

```text
CANDIDATE
CONFORMANCE_PASS
REVIEW_ACCEPTED
AUDIT_ACCEPTED
APPROVED
REVOKED
```

`APPROVED` requires all prior states. Rotation of binary, image digest, request
schema, output schema, provider API mode or model profile creates a new adapter
version and invalidates the old behavioral baseline.

## Current project status

```yaml
approvedAdapter:
  status: NOT_EVALUATED
  reason: >-
    No provider-specific adapter executable, immutable digest, credentials
    boundary and conformance evidence were supplied in the current workspace.
```
