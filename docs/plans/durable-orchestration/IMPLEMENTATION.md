# Durable orchestration — issue #49

Status: isolated implementation candidate; ADR Proposed, not normative.

The existing coordd owns all state. Canonical schema v2 adds global commit revision and typed orchestration records. Action changes and Task domain events commit together; acceptance commits canonical Inbox receipt and acknowledgement intent. Existing transport Outbox remains unchanged. Under #96 DISABLE_PENDING_ACCEPTANCE, production coordd does not invoke orchestration recovery, action Tick, adapters or projections. Existing snapshot data and read-only OwnsRun fencing remain; the ordinary Task/Run/Assignment paths are unchanged. Only the explicit _test.go harness invokes recovery before Tick and processes at most one action per Tick. Production enablement has no environment/config/flag path and requires a separate change after BOTH ADR acceptance and readiness of the authenticated single write path. Orchestrated Runs reject raw legacy transport mutation and are excluded from the legacy timeout writer.

No live action adapter is installed in coordd. Missing adapters create bounded BLOCKED_EXTERNAL obligations, never launch agents. Fake adapters exercise crash/restart and next-generation replacement request; production replacement Run/session admission is not implemented. There is no user-facing launch endpoint, LLM CandidateDecision layer, full CI/Issue scheduler, or generic accepted-norm reason evaluator.

Claims use epochs; recovery converts in-flight operations to AMBIGUOUS. Retries observe the same operation key. Completed RUNNING observations receive a fresh observation instance after restart. Fence acceptance revokes the original canonical AgentSession; only confirmed fake stop proof allows replacement intent. The replacement action binding identifies the fenced source generation; its typed request identifies generation+1. It grants no new canonical writer authority.

In the explicit enabled test harness, projections are JSON-as-YAML files under state_dir/operations/revisions/<revision>/, published via current.json manifest. Production coordd no longer publishes or repairs this bundle under #96; existing bundle files are left untouched. The canonical store remains authoritative. Publication retries repair missing files; no projection acknowledgement commits back into canonical state.

Schema v1 upgrades in memory to v2, then persists on the next successful canonical mutation. Old strict binaries reject v2 fields; rollback requires quiescence and a reviewed schema strategy, never loading v2 into an old writer. Unknown schema versions remain fail-closed. Existing Task revisions and role independence checks remain active.

Tests run through scripts/coord-project-tmp. Full checks additionally require PyYAML/jsonschema; the executor uses a project-local venv. Development-only scripts/test-orchestration-mutations.py copies source into project-local scratch, compiles each mutant and requires failure at its designated assertion. It does not edit the checkout or test a copied reimplementation of the algorithm.

Review must assess the API boundary: store orchestration methods accept trusted in-process commands; no authentication endpoint is introduced. A live adapter must enforce idempotency/claim fencing at its external-effect boundary before production use. Storage CAS/lease expiration alone does not stop a worker.
