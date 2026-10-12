#!/usr/bin/env bash
set -eu
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
SCHEMA="$ROOT/docs/schemas/recovery-report.schema.json"
COORD_SCHEMA="$ROOT/docs/schemas/recovery-report.coordinator.schema.yaml"

python3 "$ROOT/scripts/check-recovery-report.py" "$ROOT/tests/fixtures/valid/resume.yaml" --schema "$SCHEMA"

if python3 "$ROOT/scripts/check-recovery-report.py" "$ROOT/tests/fixtures/invalid/foreign-resume.yaml" --schema "$SCHEMA"; then
  echo "invalid fixture unexpectedly passed" >&2
  exit 1
fi

python3 "$ROOT/scripts/check-recovery-report.py" "$ROOT/tests/fixtures/valid/coordinator-recover-coordination-state.yaml" --schema "$COORD_SCHEMA"

echo "RECOVERY_CHECK_SELF_TEST_PASS"
