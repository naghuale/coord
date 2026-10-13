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

# gap-3: SESSION_RESUMED edge removed, WORKTREE_STATE_UNKNOWN edge kept -> must FAIL
mutate_action_map sr-edge-removed 'am["events"]["SESSION_RESUMED"]["expectedResults"] = [r for r in am["events"]["SESSION_RESUMED"]["expectedResults"] if r != "RECOVER_COORDINATION_STATE"]'
expect_invalid sr-edge-removed "$SCRATCH/sr-edge-removed.yaml"

echo "REGRESSION_MUTATIONS_PASS"

# F7 authority regression: the regulation must stay Accepted and normative.
mutate_registry() {
  python3 - "$ROOT/docs/DOCUMENT-REGISTRY.yaml" "$SCRATCH/$1.yaml" "$2" <<'PY'
import copy, sys, yaml
reg = yaml.safe_load(open(sys.argv[1], encoding="utf-8"))
reg = copy.deepcopy(reg)
for doc in reg["documents"]:
    if doc.get("path") == "docs/operations/AGENT-SESSION-RECOVERY-REGULATION.md":
        exec(sys.argv[3])
        break
yaml.safe_dump(reg, open(sys.argv[2], "w", encoding="utf-8"), sort_keys=False, allow_unicode=True)
PY
}

expect_invalid_registry() {
  if python3 "$GOV" --registry "$2" --action-map "$ACTION_MAP" --agents "$AGENTS_FILE" --regulation-path "$REGULATION" 2>/dev/null; then
    echo "REGRESSION: $1 mutation should fail but passed" >&2
    exit 1
  fi
}

mutate_registry authority-proposed 'doc["status"] = "proposed"'
expect_invalid_registry authority-proposed "$SCRATCH/authority-proposed.yaml"

mutate_registry authority-no-normative 'doc["normativeAuthority"] = ""'
expect_invalid_registry authority-no-normative "$SCRATCH/authority-no-normative.yaml"

mutate_registry authority-not-current 'doc["currentNormativeAuthority"] = False'
expect_invalid_registry authority-not-current "$SCRATCH/authority-not-current.yaml"

mutate_registry authority-no-decision 'doc.pop("decisionRef", None)'
expect_invalid_registry authority-no-decision "$SCRATCH/authority-no-decision.yaml"

echo "AUTHORITY_REGRESSION_PASS"

# Regression mutations for the Recovery Report validator itself.
expect_invalid_report() {
  # $1: mutation name, $2: fixture, $3: python mutation expression
  local mf
  mf=$(mktemp "$SCRATCH/report-mutation-XXXXXX")
  if ! python3 - "$2" "$mf" "$3" <<'PY'
import copy, sys, yaml
doc = yaml.safe_load(open(sys.argv[1], encoding="utf-8"))
doc = copy.deepcopy(doc)
exec(sys.argv[3])
yaml.safe_dump(doc, open(sys.argv[2], "w", encoding="utf-8"), sort_keys=False, allow_unicode=True)
PY
  then
    echo "REGRESSION: $1 mutation setup failed" >&2
    exit 1
  fi
  if python3 "$ROOT/scripts/check-recovery-report.py" "$mf" --schema "$COORD_SCHEMA" 2>/dev/null; then
    echo "REGRESSION: $1 should fail but passed" >&2
    exit 1
  fi
}

COORD_FIXTURE="$ROOT/tests/fixtures/valid/coordinator-recover-coordination-state.yaml"
RESUME_FIXTURE="$ROOT/tests/fixtures/valid/resume.yaml"

expect_invalid_report staged-unknown-rcs "$COORD_FIXTURE" \
  'doc["workingTree"]["stagedFiles"] = doc["classification"]["foreignOrUnknown"][:1]'
expect_invalid_report bucket-overlap "$COORD_FIXTURE" \
  'doc["classification"]["generated"] = doc["classification"]["foreignOrUnknown"][:1]'
expect_invalid_report head-mismatch-resume "$RESUME_FIXTURE" \
  'doc["recovery"]["headMatchesExpected"] = False'
expect_invalid_report high-risk-resume "$RESUME_FIXTURE" \
  'doc["workingTree"]["preservationRisk"] = "HIGH"'
expect_invalid_report repo-dotdot "$RESUME_FIXTURE" \
  'doc["recovery"]["repositoryRoot"] = doc["recovery"]["projectRoot"] + "/../../outside"'
expect_invalid_report worktree-dotdot "$RESUME_FIXTURE" \
  'doc["recovery"]["assignedWorktree"] = doc["recovery"]["projectRoot"] + "/../workspace-02"'
expect_invalid_report repo-outside "$RESUME_FIXTURE" \
  'doc["recovery"]["repositoryRoot"] = "/etc/passwd"'
expect_invalid_report launch-cwd-wrong "$RESUME_FIXTURE" \
  'doc["recovery"]["requiredLaunchCwd"] = doc["recovery"]["projectRoot"]'
expect_invalid_report unclassified-changed "$COORD_FIXTURE" \
  'doc["workingTree"]["modifiedFiles"] = doc["workingTree"].get("modifiedFiles", []) + ["docs/NEW-UNCLASSIFIED.md"]; doc["workingTree"]["totalChangedFiles"] = doc["workingTree"].get("totalChangedFiles", 0) + 1'

echo "REPORT_REGRESSION_MUTATIONS_PASS"
