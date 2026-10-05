"""P2/P3: actual Python worker, SIGKILL at 50%, transaction-backed resume."""
import hashlib
import json
import os
from pathlib import Path
import signal
import sqlite3
import subprocess
import time

root = Path(__file__).resolve().parent
project = root.parent.parent
evidence_root = project / "state" / "telepathy-experiments"
evidence_root.mkdir(parents=True, exist_ok=True)
trial = evidence_root / ("p2-p3-" + str(time.time_ns()))
trial.mkdir(mode=0o700)
server = os.environ.get("TELEPATHY_SERVER")
if not server or not Path(server).is_file():
    raise SystemExit("Set TELEPATHY_SERVER to the installed Telepathy 0.8.0 dist/server.mjs")
sha = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=project, text=True).strip()
diff = subprocess.check_output(["git", "diff"], cwd=project)
(trial / "tested-tracked.patch").write_bytes(diff)
source_paths = [project / "coordinator.go", project / "metrics.go", project / "fencing_test.go"] + list(root.glob("*.go")) + list(root.glob("*.py"))
source_hashes = {str(p.relative_to(project)): __import__("hashlib").sha256(p.read_bytes()).hexdigest() for p in source_paths}
(trial / "tested-source-sha256.json").write_text(json.dumps(source_hashes, indent=2) + "\n")
processes = []

def stop(proc):
    try:
        os.killpg(proc.pid, signal.SIGKILL)
    except ProcessLookupError:
        pass
    proc.wait(timeout=10)

def actor(mode, *args):
    proc = subprocess.Popen([str(project / "bin" / "telepathy-experiment"), "-mode", mode, "-state", str(trial), "-server", server, *args], cwd=project, start_new_session=True)
    processes.append(proc)
    return proc

def wait_for(path, timeout=25):
    end = time.monotonic() + timeout
    while not path.exists():
        if time.monotonic() > end:
            raise RuntimeError("timeout waiting for " + str(path))
        time.sleep(0.05)
    return json.loads(path.read_text())

def rows():
    with sqlite3.connect(trial / "side-effects.sqlite3") as db:
        return list(db.execute("SELECT unit,payload,writes FROM units ORDER BY unit")), list(db.execute("SELECT task,run,attempt,writes FROM completions"))

try:
    agent = actor("p2-agent", "-worker", str(root / "worker.py"))
    ref = wait_for(trial / "p2-agent-ready")
    coord = actor("p2-coordinator", "-to", ref, "-sha", sha)
    halfway = wait_for(trial / "p2-worker-halfway")
    kill_point = wait_for(trial / "p2-ready-to-kill")
    accepted = wait_for(trial / "p2-progress-accepted")
    assert halfway["progress_pct"] == 50 and accepted["progress"] == 50
    before_kill, completions = rows()
    assert len(before_kill) == 2 and not completions
    (trial / "side-effects-at-50.json").write_text(json.dumps(before_kill, indent=2) + "\n")
    os.kill(kill_point["pid"], signal.SIGKILL)
    restarted = wait_for(trial / "p2-agent-completed")
    final = wait_for(trial / "p2-done")
    assert coord.wait(timeout=10) == 0 and agent.wait(timeout=10) == 0
    assert final["status"] == "DONE"
    after_resume, completed = rows()
    assert len(after_resume) == 4 and after_resume[:2] == before_kill
    assert all(row[2] == 1 for row in after_resume)
    assert len(completed) == 1 and completed[0][3] == 1
    # Replay the entire completed subprocess input once more.
    replay = subprocess.run(["python3", str(root / "worker.py"), "--input", str(trial / "worker-input.json")], cwd=project, check=True, capture_output=True, text=True, timeout=10)
    after_replay, completions_after = rows()
    assert after_replay == after_resume and completions_after == completed
    report = {"result": "PASS", "telepathy": "0.8.0", "base_commit": sha,
        "tracked_patch_sha256": hashlib.sha256(diff).hexdigest(), "P2": {"result": "PASS", "actual_subprocess": "python3 worker.py",
            "killed_pid": restarted["killed_pid"], "replacement_pid": restarted["replacement_pid"],
            "crash": "SIGKILL at committed 50%", "bridge_progress_before_resume": 50, "final_status": final["status"]},
        "P3": {"result": "PASS", "committed_units_before_kill": 2, "committed_units_after_resume": 4,
            "original_units_preserved": True, "writes_per_unit": [row[2] for row in after_resume],
            "logical_completion_count": 1, "full_input_replay_changes": 0},
        "boundaries": ["experimental controller, no production bridge", "test worker implements explicit RESUME/IDEMPOTENT contract",
            "proof applies to SQLite side effects in this worker, not arbitrary subprocess or external services", "no power-loss test"]}
    (trial / "report.json").write_text(json.dumps(report, indent=2) + "\n")
    print(json.dumps({"result": "PASS", "P2": "PASS", "P3": "PASS", "evidence": str(trial)}, indent=2))
except Exception as exc:
    (trial / "failure.json").write_text(json.dumps({"result": "FAIL", "error": str(exc)}, indent=2) + "\n")
    print("Evidence:", trial)
    raise
finally:
    for proc in reversed(processes):
        stop(proc)
