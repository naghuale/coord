#!/usr/bin/env python3
import argparse, json, re, sys
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
    if data["recovery"]["requiredLaunchCwd"].rstrip("/") != project:
        return fail("requiredLaunchCwd must equal projectRoot")

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
