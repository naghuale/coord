#!/usr/bin/env python3
import argparse, sys
from pathlib import Path
import yaml

REQUIRED_EVENTS = {
    "SESSION_INTERRUPTED", "SESSION_RESUMED", "SESSION_REPLACED", "CONTEXT_LOST",
    "WORKTREE_STATE_UNKNOWN", "REQUIRED_SOURCE_MISSING", "SUBJECT_DRIFT_DETECTED"
}
REQUIRED_TRANSITIONS = {
    "RESUME_ASSIGNMENT", "FREEZE_FOR_REVIEW", "RECOVER_COORDINATION_STATE",
    "PREPARE_CORRECTION", "BLOCKED_EXTERNAL", "HUMAN_ACTION_REQUIRED",
    "OWNER_REQUIRED", "PRESERVE_AND_STOP"
}
# Registry markers that explicitly deny current normative authority.
NON_NORMATIVE_AUTHORITY = {
    "none", "proposed_direction", "design_only", "example_only", "legacy_only",
}

def load(path):
    return yaml.safe_load(Path(path).read_text(encoding="utf-8"))

def main():
    p=argparse.ArgumentParser()
    p.add_argument("--registry", required=True)
    p.add_argument("--action-map", required=True)
    p.add_argument("--agents", required=True)
    p.add_argument("--regulation-path", required=True)
    args=p.parse_args()
    problems=[]

    registry=load(args.registry)
    paths={d.get("path") for d in registry.get("documents", [])}
    if args.regulation_path not in paths:
        problems.append("recovery regulation is not registered")

    reg_doc=next(
        (d for d in registry.get("documents", []) if d.get("path") == args.regulation_path),
        None,
    )
    if reg_doc is None:
        problems.append("recovery regulation not found in registry")
    else:
        if reg_doc.get("status") != "accepted":
            problems.append(f"recovery regulation status must be 'accepted', got {reg_doc.get('status')!r}")
        normative=reg_doc.get("normativeAuthority")
        if not isinstance(normative, str) or normative in NON_NORMATIVE_AUTHORITY:
            problems.append(f"recovery regulation must have normativeAuthority, got {normative!r}")
        if reg_doc.get("currentNormativeAuthority") is not True:
            problems.append("recovery regulation must have currentNormativeAuthority=true")
        if not reg_doc.get("decisionRef"):
            problems.append("recovery regulation must record the OWNER decisionRef")

    amap=load(args.action_map)
    events=set(amap.get("events", {}).keys())
    missing=sorted(REQUIRED_EVENTS-events)
    if missing:
        problems.append("missing recovery events: " + ", ".join(missing))
    serialized=Path(args.action_map).read_text(encoding="utf-8")
    missing_transitions=sorted(t for t in REQUIRED_TRANSITIONS if t not in serialized)
    if missing_transitions:
        problems.append("missing recovery transitions: " + ", ".join(missing_transitions))

    all_expected=set()
    for ev_body in amap.get("events", {}).values():
        if isinstance(ev_body, dict):
            all_expected.update(ev_body.get("expectedResults", []))
    recovery_transition_keys=set(amap.get("recoveryTransitions", {}).keys())
    unreachable=sorted(
        t for t in REQUIRED_TRANSITIONS
        if t not in all_expected and t in recovery_transition_keys
    )
    if unreachable:
        problems.append("unreachable recovery transitions (not in any expectedResults): " + ", ".join(unreachable))

    undefined_but_required=sorted(
        t for t in REQUIRED_TRANSITIONS
        if t not in recovery_transition_keys
    )
    if undefined_but_required:
        problems.append("recovery transitions required but missing from recoveryTransitions: " + ", ".join(undefined_but_required))

    wtu_expected=set(
        amap.get("events", {}).get("WORKTREE_STATE_UNKNOWN", {}).get("expectedResults", [])
    )
    if "RECOVER_COORDINATION_STATE" not in wtu_expected:
        problems.append("RECOVER_COORDINATION_STATE not reachable from WORKTREE_STATE_UNKNOWN")

    sr_expected=set(
        amap.get("events", {}).get("SESSION_RESUMED", {}).get("expectedResults", [])
    )
    if "RECOVER_COORDINATION_STATE" not in sr_expected:
        problems.append("RECOVER_COORDINATION_STATE not reachable from SESSION_RESUMED")

    agents=Path(args.agents).read_text(encoding="utf-8")
    for phrase in ["RECOVER_SESSION", "PRESERVE_AND_STOP", "RESUME_ASSIGNMENT", "RECOVER_COORDINATION_STATE"]:
        if phrase not in agents:
            problems.append(f"AGENTS missing {phrase}")

    if problems:
        for x in problems: print("- "+x, file=sys.stderr)
        print("RECOVERY_GOVERNANCE_INVALID", file=sys.stderr)
        return 1
    print("RECOVERY_GOVERNANCE_VALID")
    return 0
if __name__=="__main__": raise SystemExit(main())
