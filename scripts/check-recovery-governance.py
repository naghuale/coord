#!/usr/bin/env python3
import argparse, subprocess, sys
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
# Only these values grant a document current normative authority.
NORMATIVE_AUTHORITY_ALLOWLIST = {"process", "product", "canonical"}

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
        if not isinstance(normative, str) or normative not in NORMATIVE_AUTHORITY_ALLOWLIST:
            problems.append(f"recovery regulation must have normativeAuthority in {sorted(NORMATIVE_AUTHORITY_ALLOWLIST)}, got {normative!r}")
        if reg_doc.get("currentNormativeAuthority") is not True:
            problems.append("recovery regulation must have currentNormativeAuthority=true")
        if not reg_doc.get("decisionRef"):
            problems.append("recovery regulation must record the OWNER decisionRef")
        else:
            ref = str(reg_doc["decisionRef"])
            # Only bare IDs are accepted. A path-like reference would let Path
            # joining escape docs/decisions/ and point the check at any file.
            if "/" in ref or "\\" in ref or ref.startswith("."):
                problems.append(
                    f"recovery regulation decisionRef must be a bare ID "
                    f"(no path separators), got {ref!r}"
                )
            else:
                decisions_dir = (Path(args.regulation_path).parent.parent / "decisions").resolve()
                decision_path = (decisions_dir / (ref + ".yaml")).resolve()
                if not str(decision_path).startswith(str(decisions_dir) + "/"):
                    problems.append("decision artifact path escapes docs/decisions/")
                elif not decision_path.is_file():
                    problems.append(
                        f"recovery regulation decisionRef={ref!r} "
                        f"has no committed artifact at {decision_path.as_posix()}"
                    )
                else:
                    repo_root = Path(args.regulation_path).parent.parent.parent.resolve()
                    tracked = subprocess.run(
                        ["git", "ls-files", "--error-unmatch",
                         decision_path.relative_to(repo_root).as_posix()],
                        capture_output=True, cwd=repo_root,
                    )
                    if tracked.returncode != 0:
                        problems.append(
                            f"decision artifact {ref!r} exists on disk "
                            f"but is not committed to git"
                        )
                    else:
                        try:
                            dec = load(str(decision_path))
                        except Exception as exc:
                            problems.append(f"cannot load decision artifact {ref!r}: {exc}")
                        else:
                            if dec.get("status") != "accepted":
                                problems.append(
                                    f"decision artifact {ref!r} "
                                    f"status must be 'accepted', got {dec.get('status')!r}"
                                )
                            if dec.get("class") != "R":
                                problems.append(
                                    f"decision artifact {ref!r} "
                                    f"must be class R, got {dec.get('class')!r}"
                                )

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
