<!-- Use when work is blocked by provider, credentials, protected storage, GitLab configuration, or pricing data. -->
/label ~"ai-agent-governance" ~"status::blocked"

# External dependency

## Required input

```yaml
type: provider | credentials | gitlab_configuration | protected_store | pricing | other
ownerDecisionRequired: false
humanActionRequired: false
```

## Why project work cannot substitute for it

<!-- State the technical boundary. -->

## Prepared project-side work

- [ ] Contract/schema ready
- [ ] Fail-closed behavior ready
- [ ] Exact command or configuration prepared
- [ ] Expected result and post-check documented

## Unblock condition

```yaml
evidenceRequired:
nextSafeAction:
```

## Forbidden shortcuts

- [ ] Do not embed credentials in repository or artifacts
- [ ] Do not count missing input as PASS
- [ ] Do not invent provider/GitLab behavior
