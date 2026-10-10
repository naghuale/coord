# Production Registry HTTP adapter

The adapter is production-oriented but endpoint mapping remains configuration.
It performs HTTPS/HTTP JSON calls, obtains the bearer token only from an
environment variable, enforces request timeouts and maps response fields into
the stable launcher contract.

This package does not claim that the default `/v1/agent-sessions/...` paths are
the canonical project API. Replace them with the accepted AgentSession Registry
OpenAPI mapping and record the adapter/config digest before approval.

Required production acceptance:

- HTTPS with certificate verification;
- protected/masked credential source;
- immutable adapter and config digests;
- server-side identity and generation checks;
- idempotent heartbeat/complete semantics;
- independent Review and TD-R2 Audit;
- integration tests against the actual Registry implementation.
