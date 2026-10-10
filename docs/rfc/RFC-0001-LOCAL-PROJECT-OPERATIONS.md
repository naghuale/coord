# RFC-0001: Local Project Workspace, Operations Ledger and Context Management

- **Status:** Accepted
- **Scope:** `repository-development-process`
- **Product feature:** no
- **Issue:** [#39](https://github.com/naghuale/coord/issues/39)
- **Validated against:** `coord` integration branch `p0-2-task` at `f192b69`
- **Created:** 2026-10-10
- **Accepted:** 2026-10-10 by OWNER (naghuale)

## Abstract

This RFC defines a portable operating standard for working on multiple software projects with multiple AI-agent roles from one local workspace. It defines project identity and local placement, the session launch directory, project-local files and temporary storage, role separation, Git-backed operations records, context loading and recovery, and the Completion Report lifecycle.

This RFC describes the repository development process. It does not define a Coord product feature or replace the product architecture in accepted ADRs. Its requirements apply to projects that adopt this RFC; the conformance record in §17 distinguishes current `coord` behavior from work that is still in progress.

## Status and change control

**Acceptance record:** This revision (`p0-2-task@f192b69`) was accepted on 2026-10-10 by OWNER (naghuale). Independent review: opencode:workspace-o73 (ACCEPTED). Verification: `make check` + `git diff --check` + reference/path checks passed on `p0-2-task@908af6e` (pre-registration commit). Closing issue: [#39](https://github.com/naghuale/coord/issues/39).

---

`Proposed` means the text is registered for review and is not yet an accepted process norm. Move this RFC to `Accepted` only after:

1. independent review of the exact document revision;
2. verification against the current `coord` integration tree, including `make check`, `git diff --check`, and reference/path checks;
3. explicit OWNER acceptance of that revision.

An accepted revision is not edited retroactively to conceal its history. Change this process through a new RFC or a recorded superseding revision, preserving the prior text and the reason for the change. Individual architectural choices remain in short ADRs; do not turn an ADR into the history of this whole process.

## 1. Purpose and goals

The standard provides:

- one explicit filesystem permission boundary for all agent sessions on a machine;
- portable project placement on macOS, Linux, and Windows;
- stable project identity independent of a local directory name;
- containment of project repositories, worktrees, task artifacts, runtime data, evidence, and scratch files within one assigned `workspace-NN`;
- distinct Coordinator, Executor, Reviewer, and Auditor responsibilities;
- durable, recoverable work history in Git without relying on model memory;
- a small active context with stable references to additional information;
- a factual Completion Report attached to its GitHub Issue;
- fail-closed handling when required roots, evidence, or checks are unavailable;
- no use of operating-system temporary directories for project work.

This standard covers repository development and local agent orchestration. It does not authorize access to another project, a production system, or a human account beyond the assignment and permissions already granted.

## 2. Normative language

The key words **MUST**, **MUST NOT**, **REQUIRED**, **SHALL**, **SHALL NOT**, **SHOULD**, **SHOULD NOT**, **RECOMMENDED**, **NOT RECOMMENDED**, **MAY**, and **OPTIONAL** are to be interpreted as described by [BCP 14](https://www.rfc-editor.org/info/bcp14/), [RFC 2119](https://www.rfc-editor.org/rfc/rfc2119), and [RFC 8174](https://www.rfc-editor.org/rfc/rfc8174) when, and only when, they appear in all capitals. Lowercase occurrences have their ordinary English meanings.

Use normative keywords only when a requirement is necessary for interoperability, security, or prevention of harmful behavior.

## 3. Terms

**`COORD_GLOBAL_ROOT`** — the explicitly configured local directory that contains all project namespaces and is the working directory from which all agent sessions on that machine are launched.

**`ProjectID`** — a stable, portable identifier for a project, such as `coord` or `telecli`. It is not a local directory name.

**`localNamespace`** — a local directory assigned to a project, such as `workspace-01`. It is a placement binding, not project identity.

**`COORD_PROJECT_ROOT`** — the local root of one project, formed from `COORD_GLOBAL_ROOT` and its assigned `localNamespace`.

**Product worktree** — a Git worktree containing product code or documentation for a task.

**Operations worktree** — a Git worktree checked out from an `ops/<ProjectID>/<Role>` branch.

**Operations Ledger** — the project’s Git-backed record of checkpoints, assignments, commands, checks, role results, incidents, corrections, publications, and recovery evidence.

**Active Context** — the minimum information needed to choose and carry out the next safe action.

**Archived Context** — information retained at a stable reference and loaded only when recovery or a cross-reference requires it.

**AgentID** — the stable identity used to check role independence across sessions and tasks. A session address alone does not establish independence.

## 4. Global root and session launch

### 4.1. Root selection

Each machine MUST configure `COORD_GLOBAL_ROOT` explicitly before launching an agent. If it is unset, empty, inaccessible, or invalid, the launcher MUST stop with a clear error. It MUST NOT select a system temporary directory, the current directory, or another implicit fallback.

All agent roles and all projects on the same machine MUST launch with their working directory set to `COORD_GLOBAL_ROOT`. An agent MUST NOT be launched from a project root, task directory, or worktree as its session working directory.

The session launch directory and the assigned worktree are separate values. The launcher may grant the session access to an assigned worktree beneath its existing permission boundary; changing the launch directory to that worktree is not the substitute for an assignment.

### 4.2. Platform configuration examples

These examples describe how an operator supplies a local root. They do not prescribe or publish one person’s absolute path.

macOS or Linux, after choosing a root:

```sh
export COORD_GLOBAL_ROOT="<absolute-user-selected-directory>"
cd "$COORD_GLOBAL_ROOT"
```

A Linux user may choose `$HOME/workspace`; a managed installation may choose a dedicated volume such as `/srv/coord-workspace`.

Windows PowerShell:

```powershell
$env:COORD_GLOBAL_ROOT = Join-Path $env:USERPROFILE "workspace"
Set-Location $env:COORD_GLOBAL_ROOT
```

On Windows, applications SHOULD use Known Folder APIs when they need a standard user directory. The workspace root is still selected explicitly by local policy; `%TEMP%` MUST NOT be used as the workspace root.

### 4.3. Updating local agent integrations

After an updater changes an installed agent integration such as Telepathy, the operator MUST restart every still-running session whose installation was updated before continuing work through that session. A running process may retain the old integration code while its on-disk hooks have already been replaced. For Telepathy 0.9.0, a running Codex session may report `Hook failed` after the update; restart that session and verify tool operation before resuming. Successful updater output confirms installation files were updated, not that already-running sessions loaded the new version.

### 4.4. Derived paths

For a project whose local namespace is `workspace-01`, the local configuration MAY define:

```text
COORD_PROJECT_ID=coord
COORD_PROJECT_NAMESPACE=workspace-01
COORD_PROJECT_ROOT=<COORD_GLOBAL_ROOT>/<COORD_PROJECT_NAMESPACE>
COORD_REPOSITORY_ROOT=<COORD_PROJECT_ROOT>/repositories/coord
COORD_WORKTREE_ROOT=<COORD_PROJECT_ROOT>/worktrees
COORD_TASK_ROOT=<COORD_PROJECT_ROOT>/tasks
COORD_RUNTIME_ROOT=<COORD_PROJECT_ROOT>/runtime
COORD_EVIDENCE_ROOT=<COORD_PROJECT_ROOT>/evidence
COORD_SCRATCH_ROOT=<COORD_PROJECT_ROOT>/.scratch
COORD_TMP_ROOT=<COORD_SCRATCH_ROOT>/tmp
COORD_SESSION_CWD=<COORD_GLOBAL_ROOT>
COORD_OPERATIONS_PREFIX=ops/coord
```

Values in the example are symbolic. Implementations MUST resolve them using platform-aware path APIs. They MUST NOT construct operating-system paths by assuming `/` is the separator.

## 5. Project registry, namespaces, and filesystem layout

### 5.1. Local registry

`${COORD_GLOBAL_ROOT}/projects.yaml` is a machine-local registry. It binds stable `ProjectID` values to local namespaces, repositories, integration branches, and path-variable names. It MUST NOT contain credentials, access tokens, private keys, or other secrets. It SHOULD refer to local paths through environment-variable names rather than copying those paths into version-controlled project identity.

Example:

```yaml
apiVersion: coord.dev/v1alpha1
kind: LocalProjectRegistry

globalRoot:
  env: COORD_GLOBAL_ROOT

sessionLaunch:
  cwd:
    env: COORD_GLOBAL_ROOT
  allAgentsUseGlobalRoot: true

projects:
  - projectId: coord
    localNamespace: workspace-01
    repository:
      canonicalUrl: https://github.com/naghuale/coord.git
      integrationBranch: p0-2-task
    paths:
      projectRoot: { env: COORD_PROJECT_ROOT }
      repositoryRoot: { env: COORD_REPOSITORY_ROOT }
      worktreeRoot: { env: COORD_WORKTREE_ROOT }
      taskRoot: { env: COORD_TASK_ROOT }
      runtimeRoot: { env: COORD_RUNTIME_ROOT }
      evidenceRoot: { env: COORD_EVIDENCE_ROOT }
      scratchRoot: { env: COORD_SCRATCH_ROOT }
      tempRoot: { env: COORD_TMP_ROOT }
    operationsPrefix: ops/coord
    status: ACTIVE
```

A registry may contain several projects, each with a distinct namespace and stable `ProjectID`. Suspending a project does not change its identity or move its files implicitly.

### 5.2. Namespace policy

Each project MUST have exactly one assigned `workspace-NN` namespace on a machine. All of its repositories, worktrees, tasks, runtime data, evidence, caches, and scratch files MUST remain beneath that namespace. The local registry itself is the only workspace-level coordination file in this standard.

The current local policy reserves `workspace-00` through `workspace-10`. Assigning a namespace outside that range or changing its interpretation REQUIRES an explicit update to local workspace policy and the registry. New projects MUST use a free namespace and MUST keep their stable `ProjectID` when moved to another machine.

Agents MUST NOT create parallel project roots such as `workspace-01`, `workspace-2`, or `workspace-codex` outside the configured global root. Existing legacy directories MUST be treated as migration inputs, not as a pattern for new work.

### 5.3. Target layout

For `ProjectID=coord`, the target layout is:

```text
${COORD_GLOBAL_ROOT}/
├── projects.yaml
├── workspace-00/
│   └── <separately-registered-project-or-legacy-data>/
├── workspace-01/
│   ├── repositories/
│   │   └── coord/
│   ├── worktrees/
│   │   ├── coordinator/
│   │   ├── issue-<N>/
│   │   ├── review-<N>/
│   │   └── operations-<role>/
│   ├── tasks/
│   │   └── task_<ULID>/
│   ├── runtime/
│   │   ├── context/
│   │   ├── sessions/
│   │   ├── supervisor/
│   │   └── sockets/
│   ├── evidence/
│   └── .scratch/
│       ├── tmp/
│       │   ├── s/
│       │   ├── recovery/
│       │   ├── sessions/<SessionBindingID>/<generation>/runs/
│       │   └── tasks/
│       ├── go-build/
│       ├── go-tmp/
│       ├── cache/
│       ├── runtime/
│       ├── npm-cache/
│       ├── pip-cache/
│       └── yarn-cache/
└── workspace-02/
    └── <another-registered-project>/
```

This is a target layout, not a claim that every project has completed migration. `repositories/<ProjectID>` contains the canonical clone. Product and Operations worktrees are created beneath `worktrees/`; temporary and task data remain beneath the same project root. Runtime sockets and ephemeral state MUST NOT be committed to Git.

## 6. Project identity and Workspace Binding

### 6.1. Version-controlled identity

Each project MUST have a version-controlled identity file at `.coord/project.yaml` or its declared equivalent. It records stable identity and policy, including:

- `ProjectID`, display name, canonical repository URL, main and integration branches;
- the `ops/<ProjectID>/<Role>` branch prefix and role branch names;
- environment-variable names for local root and project paths;
- the global-root launch invariant and project containment policy;
- links to governing project documents.

It MUST NOT contain absolute local filesystem paths, local usernames, credentials, session tokens, or runtime socket paths. A workspace namespace MAY be recorded as local registry data but MUST NOT be treated as project identity.

### 6.2. Per-session binding

Every session MUST receive a Workspace Binding that names:

- `ProjectID`, local namespace, and project root;
- session address, generation, stable AgentID, and launch working directory;
- Issue, TaskID, role, base SHA, assigned product worktree, and Operations worktree as applicable;
- allowed, read-only, and prohibited paths;
- project-local temporary root.

Admission MUST verify `launchCwd == COORD_GLOBAL_ROOT`, that assigned worktrees are under `COORD_PROJECT_ROOT/worktrees/`, and that temporary storage is under `COORD_PROJECT_ROOT/.scratch/`. A binding MUST NOT grant access to a sibling project or an unrelated worktree. Missing or contradictory binding facts MUST fail closed before work starts.

## 7. Project-local temporary storage

### 7.1. Location and environment

All temporary files created by project tools, builds, tests, and agent sessions MUST be placed under `${COORD_PROJECT_ROOT}/.scratch/`. Session-specific temporary state SHOULD include both a stable session binding identifier and its generation:

```text
${COORD_TMP_ROOT}/sessions/<SessionBindingID>/<generation>/
```

A project launcher SHOULD set `TMPDIR`, `TMP`, `TEMP`, and `TEMPDIR` to the session-local directory, `GOTMPDIR` and `GOCACHE` to project-local Go directories, `XDG_CACHE_HOME` and `XDG_RUNTIME_DIR` to project-local directories, and language-specific cache variables to project-local caches.

### 7.2. System temporary directories

The system temporary directories (`/tmp`, `/private/tmp`, `/var/tmp`, their platform equivalents, and `%TEMP%`) MUST NOT be used for project work. A wrapper MUST reject a configured temporary root outside the project. If an executable ignores the environment and writes to a hard-coded system path, it MUST be run with operating-system isolation that denies that path; the wrapper alone is not enforcement for a hard-coded path.

If the local scratch root is missing or unwritable, the operation MUST stop with an actionable error. It MUST NOT fall back to a system temporary directory.

### 7.3. Caches and cleanup

Caches, compiler temporary files, runtime sockets, recovery artifacts, and task-scoped scratch files MUST be placed under project-local `.scratch/`. Cleanup MUST be limited to the assigned task or session generation. A cleanup routine MUST NOT delete another live generation’s files or published evidence.

## 8. Role separation and acceptance

### 8.1. Roles

**Coordinator** maintains project and task state, prepares bounded assignments, selects worktrees, evaluates readiness, coordinates review and audit, records verified progress, handles OWNER boundaries, and publishes a factual Completion Report. It MUST derive decisions from current files, code, tests, Git state, and explicit OWNER decisions rather than model memory.

**Executor** implements one bounded assignment in its assigned worktree. The Executor MUST NOT accept its own work or change its scope without a recorded assignment update.

**Reviewer** independently reviews the frozen result against the assignment, base SHA, and review checklist. The Reviewer MUST NOT be the Executor for that task, measured by stable AgentID.

**Auditor** independently checks security boundaries, fail-closed behavior, negative cases, and required mutations. For a `TD-R2` task, an Auditor MUST be assigned and MUST be distinct by stable AgentID from both Executor and Reviewer. `N/A` MUST NOT be used to waive a required `TD-R2` audit. For a task that is not `TD-R2`, Auditor MAY be `N/A` with the reason recorded, unless an assignment, accepted norm, or OWNER decision requires an audit.

### 8.2. Acceptance and evidence

Task completion requires an accepted independent review, all required audits accepted, required checks passing, and the commit and integration conditions in the project’s Commit Readiness policy. Transport delivery alone MUST NOT be treated as acceptance. A failed, missing, stale, or non-independent verdict MUST leave the task unaccepted and the Issue open.

New project norms, trust-boundary changes, changes to fail-closed behavior, and changes reserved to OWNER MUST be presented to OWNER rather than silently decided by an agent. A request to implement an already-approved norm does not authorize an agent to invent adjacent policy.

## 9. Operations Ledger

### 9.1. Branches and records

Each project MUST maintain published Git branches:

```text
ops/<ProjectID>/project
ops/<ProjectID>/coordinator
ops/<ProjectID>/executor
ops/<ProjectID>/reviewer
ops/<ProjectID>/auditor
```

The branches are created from the current integration branch and are not automatically merged into product branches. Their records are scoped by role:

| Branch | Typical records |
|---|---|
| `project` | verified checkpoint, open work, milestones, recovery, issue assignments, sessions, publications |
| `coordinator` | task assignments, commands, decisions, readiness, incidents, corrections |
| `executor` | task contract, plan, commands, checkpoints, results |
| `reviewer` | review input, checklist, findings, result |
| `auditor` | audit scope, mutation matrix, exclusions, result |

The exact directory structure and schemas are defined by [`docs/operations/OPERATIONS-LEDGER.md`](../operations/OPERATIONS-LEDGER.md) and the schemas indexed from `.coord/CONTEXT-INDEX.yaml`.

### 9.2. Append-only history and current snapshots

Published evidence and accepted historical records MUST NOT be rewritten or removed to hide prior state. A correction MUST be a new record that identifies the record being corrected, gives a reason, states the corrected conclusion, and links evidence.

`CURRENT.yaml` and `OPEN-WORK.yaml` are current snapshots and MAY be updated by a new commit. Updating a snapshot MUST NOT rewrite the published Git history that explains earlier state. Historical entries added after the fact MUST be labelled `recorded_after_fact: true` and include a recording time.

### 9.3. Stored and excluded data

The Ledger SHOULD store project identity, checkpoints, assignments, commands, checks, review and audit results, incident/correction records, publication facts, tree hashes, commit SHAs, and CI status.

It MUST NOT store private keys, credentials, session tokens, confidential evidence, live PIDs, leases, runtime socket paths, or scratch contents. Issue numbers and URLs are references to work, not authoritative evidence that a check passed.

GitHub Issues track work and provide external references. Normative decisions come from accepted ADRs and recorded OWNER decisions. State claims must cite current Git/test/GitHub CI evidence in accordance with the reporting rules.

## 10. Context management

### 10.1. Four layers

The Coordinator MUST maintain a Context Index that resolves topics to stable sources. Context is loaded in four layers:

| Layer | Name | Loading rule | Content |
|---|---|---|---|
| 1 | `permanentCore` | Always | `.coord/project.yaml`, `AGENTS.md`, `.coord/CONTEXT-INDEX.yaml` |
| 2 | `currentProjectContext` | Always | current checkpoint, open work, current ContextCheck, known deviations and OWNER decisions |
| 3 | `currentTaskContext` | On demand for the assigned task | assignment, base SHA, applicable norms, allowed files, tests, stop conditions, review checklist |
| 4 | `archive` | Only for recovery or explicit cross-reference | completed tasks, old sessions, tool output, mutation logs, incidents, old checkpoints, legacy work |

The Context Index MUST point to the current authoritative source and MUST distinguish an existing source from one planned for creation. A missing required source MUST be recorded as missing or a known deviation; it MUST NOT be represented as verified.

### 10.2. ContextCheck

The Coordinator SHOULD perform a qualitative ContextCheck at session start, after compaction, at stage transition, during recovery, and after a context error. It MUST NOT use token count or token reduction as a pass gate.

The check records at least these facts: project ID, source commit, current stage, active task, blockers, next safe action, open OWNER decisions, and known deviations. It records source verification, relevant behavior counters, and six probes:

1. project identity and source HEAD;
2. active task;
3. blockers;
4. next safe action;
5. whether an OWNER decision is required;
6. whether evidence for the last completed task can be recovered by stable references.

A ContextCheck passes only when all six probes pass, all required facts are present, no OWNER boundary was missed, no cross-project context was introduced, no authoritative source is stale, and no decision was reversed due to stale context. An unresolved check is `FAIL`; only the missing authoritative source should be loaded through the index before rechecking.

### 10.3. Compaction

If information is not needed for the next safe action and is available through a stable reference, it SHOULD be removed from active context. After a task completes, the active context SHOULD drop full diffs, detailed logs, old session addresses, interim review messages, individual mutation output, transient file hashes, and rejected alternatives.

The compact record MUST retain enough stable evidence to recover the completed task: Issue/Task identity, commit, review, required audit (or justified N/A), CI/check status, and evidence references. Compaction MUST preserve current authority, known deviations, blockers, OWNER boundaries, and the next safe action. Compaction is not permission to discard the underlying Git evidence.

## 11. Recovery on a new machine

A recovery session MUST:

1. clone the canonical repository;
2. read `.coord/project.yaml` and choose a free, policy-approved namespace;
3. configure `COORD_GLOBAL_ROOT` and register the project in the local `projects.yaml`;
4. fetch the project’s `ops/<ProjectID>/*` branches;
5. read the current checkpoint, open work, Context Index, and required assignment;
6. reconcile Git HEAD and refs with GitHub Issues, published commits, and CI;
7. create only project-local repositories/worktrees and temporary roots;
8. launch agents from `COORD_GLOBAL_ROOT` and run workspace admission;
9. resume only from the last verified checkpoint.

If the Ledger, product Git, Issue, or CI disagree, the Coordinator MUST record a `RECOVERY_CONFLICT` with evidence and MUST NOT resume affected work automatically. Recovery proceeds only after the conflict is resolved under the applicable authority.

## 12. Completion Report and Issue lifecycle

The Coordinator MUST update current project and open-work snapshots, post one Completion Report to the corresponding GitHub Issue, and close that Issue only after all defined close conditions are satisfied. The current report format and close conditions are maintained in [`docs/operations/OPERATIONS-LEDGER.md`](../operations/OPERATIONS-LEDGER.md); that file is the operational template and must remain consistent with this RFC.

Each Issue receives its own report; reports for separate Issues MUST NOT be consolidated. A report MUST include verifiable integration SHA, executor, independent reviewer, required auditor status, checks, CI, integration result, normative outcome, what changed, what now works, open work, OWNER requirement, and next safe action. It MUST NOT include full diffs, shell logs, private paths, credentials, or chat transcripts.

A `TD-R2` report MUST show an accepted independent Auditor result. `N/A` is permitted only where the task is not `TD-R2`, with a reason, and where no assignment or accepted norm requires an audit. An Issue MUST remain open until implementation, independent review, required audit, published commit, green CI, integration, and report publication are all verified. A completed but not yet integrated task uses `READY_FOR_INTEGRATION` and MUST NOT be closed.

## 13. ADRs and decisions

This RFC defines the whole repository-development process. ADRs remain short records of individual architectural decisions: context, decision, status, and consequences. A decision that is accepted is not silently rewritten. If it changes, record a new ADR or explicit superseding decision that links to the earlier record.

This RFC MUST NOT silently change an accepted product ADR or a recorded OWNER decision. Where it conflicts with an accepted ADR or a current OWNER decision, the conflict is `UNKNOWN` until the OWNER resolves it; implementation MUST stop at the affected boundary.

## 14. Security and failure behavior

- Missing or invalid workspace roots, bindings, assignments, or required evidence MUST fail closed.
- A tool MUST NOT escape its assigned project root or worktree boundary.
- No fallback to a system temporary directory is allowed.
- Secrets and confidential material MUST remain outside Git and Completion Reports.
- Role independence is checked by stable AgentID and task history, not by model name or session name.
- Review or audit acceptance MUST be tied to the frozen tree being integrated. A changed tree invalidates the old verdict until it is reviewed again.
- Published Git history MUST NOT be force-pushed or rewritten.
- No model’s memory is an authority source. The current files and verifiable records are.

## 15. Rejected alternatives

### Separate launch directory per agent

Rejected as the shared standard. Directories such as `workspace-01/coordinator` or `workspace-01/opencode` create additional operating-system permission boundaries and cause inconsistent access when new worktrees are created. Agent role is represented by assignment and allowed paths, while the launch cwd remains the common global root.

### Launch from the project root or task worktree

Rejected as the shared standard. It makes each project or worktree a different session boundary and prevents a stable cross-project workspace model.

### Global worktrees outside the project namespace

Rejected. Worktrees and task files outside their assigned `workspace-NN` make a project non-self-contained and complicate permission review and recovery.

### System `/tmp` for project work

Rejected. A system temp directory is shared outside the project boundary, may be cleaned independently, and can violate the declared workspace permission model.

### Full project history in active context

Rejected. It increases stale-context risk and duplication. Stable references and selective recovery are the default.

### One operations branch per session

Rejected. The Ledger uses one branch per project and role; session identity and generations are recorded as data, not as branch proliferation.

### Token count as a context pass gate

Rejected. Context quality is evaluated by required facts, probes, authority boundaries, and source freshness, not by size reduction.

## 16. Adoption sequence

Adopt the standard in independently verifiable slices:

1. register this RFC and complete independent review and OWNER acceptance;
2. keep project identity and local registry environment-based, remove machine-specific paths from Git;
3. complete repository, worktree, task, runtime, evidence, and scratch migration under the assigned project root;
4. enforce project-local temp and cache variables, fail closed on system-temp use, and verify OS-level isolation for binaries with hard-coded paths;
5. maintain and validate all five role Operations branches, including initial checkpoint and open-work records;
6. maintain the four context layers, Context Index, six ContextCheck probes, and compaction evidence;
7. publish one Completion Report per Issue and close only after every close condition passes;
8. test recovery under a fresh approved namespace, including detection of a deliberate Git/Issue/CI conflict.

Each slice MUST have its own Issue, bounded assignment, review evidence, checks, and checkpoint. Do not combine unrelated slices into one task or commit.

## 17. `coord` validation and known deviations

This proposed revision was checked against `coord` at `p0-2-task@908af6e` on 2026-10-10. The check confirms that the following artifacts and policies are represented in the current project:

| Area | Evidence in current `coord` | Result |
|---|---|---|
| Stable project identity and environment-named global root | `.coord/project.yaml`, local `projects.yaml` | Present |
| Four context layers and indexed sources | `.coord/project.yaml`, `.coord/CONTEXT-INDEX.yaml` | Present |
| Qualitative six-probe ContextCheck | `docs/operations/schemas/context-check.schema.yaml`, Operations context record | Present |
| Operations branch model and recovery procedure | `docs/operations/OPERATIONS-LEDGER.md` | Present |
| Completion Report and Issue close conditions | `docs/operations/OPERATIONS-LEDGER.md` | Present |
| System temporary-directory restriction | `.coord/project.yaml`; #38 removed an explicit `/tmp` argument from the session test harness | Policy is recorded and the hard-coded test path is removed; project-local `TMPDIR` setup and OS-level enforcement are not verified by this review |
| Canonical clone at `repositories/coord` | issue #37 in `operations/project/OPEN-WORK.yaml` | **Not complete**; current repository checkout remains transitional |

The check is not a claim that every target-layout migration is complete. Issue #37 remains an explicit transition item; no other agent may assume the repository move has happened until the checkpoint records it. Likewise, the `systemTmp: deny` declaration and removal of the hard-coded `/tmp` test argument do not by themselves prove that all tools use project-local temporary storage or that OS-level isolation blocks hard-coded paths; this remains an adoption and verification item. The RFC remains `Proposed` until independent review and OWNER acceptance. Acceptance of the process MUST NOT be reported as completion of the migration or as evidence that an unimplemented runtime feature exists.

The current migration also has two protected legacy worktrees, `workspace-2-codex` and `workspace-2-opencode`. They MUST NOT be reset, removed, or repurposed as part of this RFC. Preserve them until their contents and ownership have been verified and a separately authorized migration or cleanup plan names the exact paths. This exception does not make those locations valid for new project work.

Issue #34 remains blocked with `WAITING_FOR_S0` in the current `OPEN-WORK.yaml`. This RFC does not resolve that blocker or authorize work across its boundary.

## 18. References

- [BCP 14](https://www.rfc-editor.org/info/bcp14/) — normative requirement keywords.
- [RFC 2119](https://www.rfc-editor.org/rfc/rfc2119) and [RFC 8174](https://www.rfc-editor.org/rfc/rfc8174) — requirement-word interpretation.
- [`AGENTS.md`](../../AGENTS.md) — repository development, autonomy, commit readiness, and OWNER boundaries.
- [`.coord/project.yaml`](../../.coord/project.yaml) — project identity, workspace policy, and context layers.
- [`.coord/CONTEXT-INDEX.yaml`](../../.coord/CONTEXT-INDEX.yaml) — authoritative source index.
- [`docs/operations/OPERATIONS-LEDGER.md`](../operations/OPERATIONS-LEDGER.md) — operational record layout, report format, and close conditions.
- [`docs/operations/schemas/workspace-binding.schema.yaml`](../operations/schemas/workspace-binding.schema.yaml) — binding and admission checks.
- [`docs/operations/schemas/project-state.schema.yaml`](../operations/schemas/project-state.schema.yaml) — checkpoint and work status values.
- [`docs/operations/schemas/context-check.schema.yaml`](../operations/schemas/context-check.schema.yaml) — ContextCheck fields and pass conditions.
- [`docs/TASK-MODEL.md`](../TASK-MODEL.md) and accepted ADRs — product Task model and architecture, which this process RFC does not override.
