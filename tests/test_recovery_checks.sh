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

# Regression mutations for the recovery governance graph checks.
# Temporary data stays in project-local scratch (R143); system /tmp is forbidden.
# The directory is unique per run so parallel runs do not share or delete
# each other's evidence. The template carries the absolute path explicitly:
# BSD mktemp ignores TMPDIR when the template has no path component.
SCRATCH_BASE_PROJECT="${COORD_PROJECT_ROOT:-$(cd "$ROOT/../.." 2>/dev/null && pwd || echo "$ROOT")}"
SCRATCH_BASE="${COORD_SCRATCH_ROOT:-$SCRATCH_BASE_PROJECT/.scratch/tmp}"
mkdir -p "$SCRATCH_BASE"
SCRATCH=$(mktemp -d "$SCRATCH_BASE/recovery-regression-XXXXXX")
trap 'rm -rf "$SCRATCH"' EXIT

ACTION_MAP="$ROOT/docs/operations/ACTION-MAP.yaml"
GOV="$ROOT/scripts/check-recovery-governance.py"
REGISTRY="docs/DOCUMENT-REGISTRY.yaml"
AGENTS_FILE="AGENTS.md"
REGULATION="docs/operations/AGENT-SESSION-RECOVERY-REGULATION.md"
# Registry paths are repository-relative, so run the governance checks from ROOT.
cd "$ROOT"

mutate_action_map() {
  # $1: mutation name, $2: python expression mutating `am`
  python3 - "$ACTION_MAP" "$SCRATCH/$1.yaml" "$2" <<'PY'
import copy, sys, yaml
am = yaml.safe_load(open(sys.argv[1], encoding="utf-8"))
am = copy.deepcopy(am)
exec(sys.argv[3])
yaml.safe_dump(am, open(sys.argv[2], "w", encoding="utf-8"), sort_keys=False, allow_unicode=True)
PY
}

expect_invalid() {
  # $1: mutation name, $2: mutated action map path
  if python3 "$GOV" --registry "$REGISTRY" --action-map "$2" --agents "$AGENTS_FILE" --regulation-path "$REGULATION" 2>/dev/null; then
    echo "REGRESSION: $1 mutation should fail but passed" >&2
    exit 1
  fi
}

python3 "$GOV" --registry "$REGISTRY" --action-map "$ACTION_MAP" --agents "$AGENTS_FILE" --regulation-path "$REGULATION" > /dev/null

# gap-1: transition definition removed while its edges remain -> must FAIL
mutate_action_map definition-removed 'del am["recoveryTransitions"]["RECOVER_COORDINATION_STATE"]'
expect_invalid definition-removed "$SCRATCH/definition-removed.yaml"

# gap-2: WORKTREE_STATE_UNKNOWN edge removed, SESSION_RESUMED edge kept -> must FAIL
mutate_action_map wtu-edge-removed 'am["events"]["WORKTREE_STATE_UNKNOWN"]["expectedResults"] = [r for r in am["events"]["WORKTREE_STATE_UNKNOWN"]["expectedResults"] if r != "RECOVER_COORDINATION_STATE"]'
expect_invalid wtu-edge-removed "$SCRATCH/wtu-edge-removed.yaml"

echo "REGRESSION_MUTATIONS_PASS"
