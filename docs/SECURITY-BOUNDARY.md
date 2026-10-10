# OS sandbox and AgentSession Registry boundary

## Linux backend

The implementation uses Bubblewrap as a low-level unprivileged sandbox wrapper.
The profile requests separate user, PID, IPC, UTS, cgroup and network namespaces,
uses read-only system mounts, exposes only configured project paths, clears the
environment and applies rlimits. Network is denied by default.

Bubblewrap provides primitives, not a complete security policy. The launcher
therefore fails closed when `bwrap` is missing and does not claim malware-proof
isolation.

## macOS and Windows

No production backend is enabled in this kit. `sandbox-exec` is deprecated on
macOS, and a supported arbitrary CLI-process replacement was not verified.
Production execution on macOS should use a separately approved VM/container
backend. Unsupported platforms fail closed.

## AgentSession Registry integration

The launcher never invents a session ID. It requests admission through a
versioned adapter, verifies project, assignment and lease-token digest bindings,
and exports only accepted session ID and generation into the sandbox.

The included mock adapter is for tests only. The actual AgentSession Registry
OpenAPI mapping is `NOT_EVALUATED` because the canonical specification was not
available to this execution environment. A production adapter must map the
project's real API without changing this launch contract.

## Remaining limitations

- no cgroup resource controller is configured by this user-space wrapper;
- no seccomp profile is bundled;
- no AppArmor/SELinux policy is bundled;
- process-tree fencing after registry revocation requires a supervisor;
- heartbeat, completion and fence commands have a client contract but are not
  yet wired into a long-running supervisor loop;
- the mock registry does not prove production identity or durability.
