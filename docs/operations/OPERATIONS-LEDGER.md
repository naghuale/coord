# Operations Ledger

**Authority:** R145
**Status:** accepted
**Date:** 2026-10-10

## Purpose

The Operations Ledger provides Git-backed, machine-readable continuity for the coord project. On any machine, a new session can clone the repository, read `ops/coord/project`, and resume work at the last verified checkpoint — without relying on model memory or local session state.

## Project Identity

Project identity is defined by `.coord/project.yaml`:

- `projectId` — stable identifier (not a directory name)
- `canonicalUrl` — canonical repository URL
- Git object identity (SHA, refs)

Local workspace names (`workspace-NN`) are NOT identity. A project may live in `workspace-04` on one machine and `workspace-01` on another. The `projectId` and `canonicalUrl` remain stable.

## Branch Layout

```
ops/<projectId>/project        — verified checkpoint, open work, milestones, recovery
ops/<projectId>/coordinator    — assignments, decisions, commands, readiness, incidents
ops/<projectId>/executor       — task contracts, plans, commands, checkpoints, results
ops/<projectId>/reviewer       — review inputs, checklists, verdicts, ReviewResult
ops/<projectId>/auditor        — security boundaries, mutation matrix, AuditResult
```

Branches are created from the current integration branch. They are never automatically merged back into product branches.

## Directory Structure per Branch

### `ops/coord/project`

```
operations/
├── project/
│   ├── CURRENT.yaml        — latest verified checkpoint
│   ├── OPEN-WORK.yaml      — active issues and their states
│   ├── MILESTONES.yaml     — completed milestones
│   └── RECOVERY.yaml      — recovery procedure and known conflicts
├── assignments/
│   └── issue-<N>.yaml
├── checks/
├── decisions/
├── sessions/
├── publications/
└── incidents/
```

### `ops/coord/coordinator`

```
operations/
├── assignments/
│   └── <taskId>/
│       ├── assignment.yaml
│       └── commands.jsonl
├── decisions/
│   └── <id>.yaml
├── readiness/
│   └── <taskId>.yaml
├── incidents/
│   └── <id>.yaml
└── corrections/
    └── <id>.yaml
```

### `ops/coord/executor`

```
operations/
└── tasks/
    └── <taskId>/
        ├── contract.yaml
        ├── plan.yaml
        ├── commands.jsonl
        ├── checkpoint.yaml
        └── result.yaml
```

### `ops/coord/reviewer`

```
operations/
└── reviews/
    └── <taskId>/
        ├── input.yaml
        ├── checklist.yaml
        ├── findings.yaml
        └── result.yaml
```

### `ops/coord/auditor`

```
operations/
└── audits/
    └── <taskId>/
        ├── scope.yaml
        ├── mutation-matrix.yaml
        ├── not-evaluated.yaml
        └── result.yaml
```

## Append-Only Policy

Published operations history is never rewritten. To correct a mistake:

```yaml
kind: Correction
correctsEvent: ops_<ULID>
reason: <why the original record was wrong>
correctedConclusion: <what is true instead>
evidence:
  - <verifiable reference>
```

Records of historical work are flagged:

```yaml
recorded_after_fact: true
recordedAt: <ISO8601>
```

## What Git Stores vs. What It Does Not

**Stored in Git (in ops/* branches):**
- ProjectID, canonical URL
- Checkpoint state (CURRENT.yaml)
- Assignments, commands, checks
- Review and audit results
- Incidents and corrections
- Tree hashes, commit SHAs, CI status

**Never stored in Git:**
- Private keys, session tokens, credentials
- Socket paths, PIDs as live identity
- Runtime leases, `.scratch` contents
- Evidence that is confidential by classification

## Recovery on a New Machine

```
clone repository
→ read .coord/project.yaml
→ choose a free workspace-NN
→ register project in projects.yaml (local registry)
→ fetch ops/coord/*
→ read operations/project/CURRENT.yaml
→ reconcile with GitHub issues, current HEAD, CI
→ on conflict: create RECOVERY_CONFLICT, do not resume automatically
→ create worktrees for active tasks
→ conduct admission for new sessions
→ continue from last verified checkpoint
```

If CURRENT.yaml diverges from GitHub, HEAD, or CI, a `RECOVERY_CONFLICT` record is created and work does not continue until the conflict is resolved.

## Coordinator Autonomy Under R145

The coordinator maintains the ledger without OWNER involvement for:
- Creating and publishing ops/* branches
- Writing assignments, commands, and checkpoints
- Launching reviewer and auditor sessions
- Recording corrections

The coordinator calls OWNER only when R142 boundaries apply:
- New product norm required
- Trust boundary change
- Weakening of fail-closed behavior
- Other R142 conditions
