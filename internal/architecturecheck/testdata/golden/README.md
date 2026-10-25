# Architecture checker golden fixtures (Refs #97)

Captured from Python `scripts/check-architecture-sync` / `scripts/check-architecture-sync-test`
evidence logs under `.scratch/tmp/issue97-impl-grok-a38/logs/` (PYTHON_LIVE_EXIT:0,
PYTHON_SELFTEST_EXIT:0). Go tests compare `internal/architecturecheck` findings against
these committed expected codes. Go tests must not invoke Python.
