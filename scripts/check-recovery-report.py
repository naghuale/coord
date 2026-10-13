#!/usr/bin/env python3
import argparse, json, os, re, sys
from pathlib import Path
import yaml
from jsonschema import Draft202012Validator, FormatChecker

SECRET_PATTERNS = [
    re.compile(r"(?i)(password|passwd|secret|token|private[_-]?key)\s*[:=]\s*[^\s]+"),
    re.compile(r"-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----"),
]

def fail(msg):
    print(f"RECOVERY_REPORT_INVALID: {msg}", file=sys.stderr)
    return 1

def main():
    p = argparse.ArgumentParser()
    p.add_argument("report")
    p.add_argument("--schema", required=True)
    p.add_argument("--project-root")
    p.add_argument("--strict-source-files", action="store_true")
    args = p.parse_args()

    raw = Path(args.report).read_text(encoding="utf-8")
    for pattern in SECRET_PATTERNS:
        if pattern.search(raw):
            return fail("possible credential material detected")

    data = yaml.safe_load(raw)
    schema_text = Path(args.schema).read_text(encoding="utf-8")
    if args.schema.endswith((".yaml", ".yml")):
        schema = yaml.safe_load(schema_text)
    else:
        schema = json.loads(schema_text)
    errors = sorted(Draft202012Validator(schema, format_checker=FormatChecker()).iter_errors(data), key=lambda e: list(e.path))
    if errors:
        for e in errors:
            print(f"- {'/'.join(map(str,e.path)) or '<root>'}: {e.message}", file=sys.stderr)
        return fail(f"schema errors={len(errors)}")

    wt = data["workingTree"]
    actual = len(set(wt["stagedFiles"] + wt["modifiedFiles"] + wt["untrackedFiles"]))
    if wt["totalChangedFiles"] != actual:
        return fail(f"totalChangedFiles={wt['totalChangedFiles']} actualUnique={actual}")
    if wt["clean"] != (actual == 0):
        return fail("workingTree.clean inconsistent with changed files")

    transition = data["recoveryCard"]["selectedTransition"]
    foreign = data["classification"]["foreignOrUnknown"]
    preserved = data["recoveryCard"]["preservedUnknownChanges"]
    if foreign and not set(foreign).issubset(set(preserved)):
        return fail("foreignOrUnknown files must be listed in preservedUnknownChanges")
    if transition == "RESUME_ASSIGNMENT" and foreign:
        return fail("RESUME_ASSIGNMENT forbidden with foreignOrUnknown changes")

    if data["subjectBinding"]["subjectDriftDetected"] and transition == "RESUME_ASSIGNMENT":
        return fail("RESUME_ASSIGNMENT forbidden after subject drift")

    scratch = data["recovery"]["scratchRoot"].rstrip("/")
    project = data["recovery"]["projectRoot"].rstrip("/")
    if scratch != project + "/.scratch/tmp":
        return fail("scratchRoot must equal projectRoot/.scratch/tmp")
    global_root = data["recovery"].get("globalRoot", "").rstrip("/")
    if data["recovery"]["requiredLaunchCwd"].rstrip("/") != global_root:
        return fail("requiredLaunchCwd must equal globalRoot per R143")

    # Unknown staged changes are a hard stop; they cannot be classified read-only.
    staged = wt.get("stagedFiles", [])
    unknown_staged = set(staged) & set(foreign)
    if unknown_staged and transition != "PRESERVE_AND_STOP":
        return fail(f"unknown staged files require PRESERVE_AND_STOP, found transition={transition}")

    if not data["recovery"].get("headMatchesExpected", True) and transition == "RESUME_ASSIGNMENT":
        return fail("RESUME_ASSIGNMENT forbidden when headMatchesExpected is false")

    pRisk = data["workingTree"].get("preservationRisk", "")
    safe_transitions = {"PRESERVE_AND_STOP", "BLOCKED_EXTERNAL", "OWNER_REQUIRED", "HUMAN_ACTION_REQUIRED"}
    if pRisk == "HIGH" and transition not in safe_transitions:
        return fail(f"preservationRisk=HIGH requires transition in {sorted(safe_transitions)}, got {transition}")

    # Every changed file is classified, and no file lands in two buckets.
    cls = data.get("classification", {})
    buckets = {
        "committedAccepted": set(cls.get("committedAccepted", [])),
        "currentAssignmentUncommitted": set(cls.get("currentAssignmentUncommitted", [])),
        "postAssignmentSync": set(cls.get("postAssignmentSync", [])),
        "foreignOrUnknown": set(cls.get("foreignOrUnknown", [])),
        "generated": set(cls.get("generated", [])),
        "scratch": set(cls.get("scratch", [])),
    }
    wt_paths = set(staged + wt.get("modifiedFiles", []) + wt.get("untrackedFiles", []))
    classified: set = set()
    for name, paths in buckets.items():
        overlap = paths & classified
        if overlap:
            return fail(f"classification overlap: {sorted(overlap)} appear in multiple buckets including {name}")
        classified |= paths
    unclassified = sorted(wt_paths - classified)
    if unclassified:
        return fail(f"changed files missing from classification: {unclassified}")

    # Containment: repository and worktree must stay under the project root.
    # Paths are fully resolved first. Lexical comparison alone accepts both
    # "<projectRoot>/../../outside" and a symlink inside the project root that
    # points outside it.
    norm_project = Path(os.path.realpath(project))
    for field in ("repositoryRoot", "assignedWorktree"):
        val = data["recovery"].get(field, "")
        if val and not Path(os.path.realpath(val)).is_relative_to(norm_project):
            return fail(f"recovery.{field}={val!r} not contained within projectRoot={project!r}")

    if args.project_root:
        expected = str(Path(args.project_root).resolve())
        if str(Path(project).resolve()) != expected:
            return fail(f"projectRoot mismatch: report={project} expected={expected}")

    if args.strict_source_files:
        missing = []
        root = Path(project)
        for name, source in data["sources"].items():
            if not isinstance(source, dict) or "path" not in source or not source["found"]:
                continue
            source_path = root / source["path"]
            if not source_path.is_file():
                missing.append(f"{name}:{source['path']}")
        if missing:
            return fail("declared source files missing: " + ", ".join(missing))

    print("RECOVERY_REPORT_VALID")
    return 0

if __name__ == "__main__":
    raise SystemExit(main())
