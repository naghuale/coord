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

    amap=load(args.action_map)
    events=set(amap.get("events", {}).keys())
    missing=sorted(REQUIRED_EVENTS-events)
    if missing:
        problems.append("missing recovery events: " + ", ".join(missing))
    serialized=Path(args.action_map).read_text(encoding="utf-8")
    missing_transitions=sorted(t for t in REQUIRED_TRANSITIONS if t not in serialized)
    if missing_transitions:
        problems.append("missing recovery transitions: " + ", ".join(missing_transitions))

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
