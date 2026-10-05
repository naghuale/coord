"""P0 and P1 with real MCP processes; all state is isolated from user sessions."""
import fcntl
import hashlib
import json
import os
from pathlib import Path
import signal
import subprocess
import time

root = Path(__file__).resolve().parent
project = root.parent.parent
evidence_root = project / "state" / "telepathy-experiments"
evidence_root.mkdir(parents=True, exist_ok=True)
base = evidence_root / ("p0-p1-" + str(time.time_ns()))
base.mkdir(mode=0o700)
server = os.environ.get("TELEPATHY_SERVER")
if not server or not Path(server).is_file():
    raise SystemExit("Set TELEPATHY_SERVER to the installed Telepathy 0.8.0 dist/server.mjs")
sha = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=project, text=True).strip()
diff = subprocess.check_output(["git", "diff"], cwd=project)
(base / "tested-tracked.patch").write_bytes(diff)
source_paths = [project / "coordinator.go", project / "metrics.go", project / "fencing_test.go"] + list(root.glob("*.go")) + list(root.glob("*.py"))
source_hashes = {str(p.relative_to(project)): __import__("hashlib").sha256(p.read_bytes()).hexdigest() for p in source_paths}
(base / "tested-source-sha256.json").write_text(json.dumps(source_hashes, indent=2) + "\n")
processes = []

def stop(proc):
    # Also clean up node children after an already exited actor.
    try:
        os.killpg(proc.pid, signal.SIGKILL)
    except ProcessLookupError:
        pass
    proc.wait(timeout=10)

def anchor():
    proc = subprocess.Popen(["sleep", "120"], start_new_session=True)
    processes.append(proc)
    return proc

def actor(trial, mode, *args):
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

def check_lock(trial, available):
    with (trial / "writer.lock").open("r+") as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            assert not available, "writer lock still held after fencing"
            return
        assert available, "writer exclusion was not enforced"
        fcntl.flock(lock, fcntl.LOCK_UN)

reports = {}
try:
    trial = base / "p0"
    trial.mkdir(mode=0o700)
    a_identity, b_identity = anchor(), anchor()
    a_ref, b_ref = "opencode-" + str(a_identity.pid), "opencode-" + str(b_identity.pid)
    a = actor(trial, "p0-oldwriter", "-peer-pid", str(a_identity.pid))
    b = actor(trial, "p0-newwriter", "-peer-pid", str(b_identity.pid))
    wait_for(trial / "p0-a-ready")
    wait_for(trial / "p0-b-ready")
    c = actor(trial, "p0-coordinator", "-to", a_ref, "-replacement", b_ref, "-sha", sha)
    writing_a = wait_for(trial / "p0-a-writing")
    before = wait_for(trial / "p0-await-fence")
    assert before["fence"] == 1 and before["owner"] == a_ref
    check_lock(trial, False)
    stop(a)
    assert a.returncode == -signal.SIGKILL
    check_lock(trial, True)
    (trial / "p0-fenced.json").write_text(json.dumps({"old_pid": a.pid, "exit": a.returncode, "lock_released": True, "at_ns": time.time_ns()}))
    transfer = wait_for(trial / "p0-transferred")
    assert transfer["fence"] == 2 and transfer["owner"] == b_ref and transfer["attempt"] == 3
    writing_b = wait_for(trial / "p0-b-writing")
    assert writing_b["fence"] == 2
    check_lock(trial, False)
    late = actor(trial, "p0-late", "-peer-pid", str(a_identity.pid))
    abort = wait_for(trial / "p0-abort-observed")
    assert late.wait(timeout=10) == 0
    final = wait_for(trial / "p0-done")
    assert c.wait(timeout=10) == 0 and b.wait(timeout=10) == 0
    assert final["status"] == "DONE" and final["fence"] == 2 and final["owner"] == b_ref
    assert "coord_stale_fence_rejections_total 1\n" in (trial / "metrics.txt").read_text()
    check_lock(trial, True)
    reports["P0"] = {"result": "PASS", "old_owner": a_ref, "new_owner": b_ref, "old_writer_pid": a.pid,
        "new_writer_pid": b.pid, "old_writer_exit": a.returncode, "attempt": 3, "fence_before": 1, "fence_after": 2,
        "final_status": "DONE", "stale_fence_rejections": 1, "late_old_session_abort_observed": True,
        "writer_lock_exclusion_checked": True, "physical_fencing": "SIGKILL process group + wait + flock availability before transfer",
        "late_sender": "new scripted messenger replays old session/fence; original killed writer is not resurrected"}

    trial = base / "p1"
    trial.mkdir(mode=0o700)
    c_identity = anchor()
    agent = actor(trial, "p1-agent")
    agent_ref = wait_for(trial / "p1-agent-ready")
    c1 = actor(trial, "p1-coordinator", "-peer-pid", str(c_identity.pid), "-to", agent_ref, "-sha", sha)
    before = wait_for(trial / "p1-state-ready")
    assert before["status"] == "STARTED" and before["progress"] == 42 and before["fence"] == 7
    (trial / "snapshot-before-kill.json").write_bytes((trial / "runs.json").read_bytes())
    stop(c1)
    assert c1.returncode == -signal.SIGKILL
    (trial / "p1-send-during-outage").write_text("true")
    wait_for(trial / "p1-outage-heartbeat-sent")
    c2 = actor(trial, "p1-coordinator", "-peer-pid", str(c_identity.pid), "-to", agent_ref, "-sha", sha)
    restored = wait_for(trial / "p1-restored")
    for field in ("status", "progress", "fence", "owner", "attempt", "sha", "task_messages"):
        assert restored[field] == before[field], (field, before, restored)
    resumed = wait_for(trial / "p1-heartbeat-resumed")
    assert resumed["progress"] == 43
    (trial / "p1-continue").write_text("true")
    final = wait_for(trial / "p1-done")
    assert c2.wait(timeout=10) == 0 and agent.wait(timeout=10) == 0
    assert final["status"] == "DONE" and final["fence"] == 7 and final["owner"] == agent_ref
    assert len(final["task_messages"]) == 1
    reports["P1"] = {"result": "PASS", "killed_coordinator_pid": c1.pid, "replacement_pid": c2.pid,
        "coordinator_session": "opencode-" + str(c_identity.pid), "status_restored": restored["status"],
        "progress_restored": 42, "queued_heartbeat_progress": 43, "fence_restored": 7,
        "owner_restored": agent_ref, "attempt_restored": 3, "task_messages": 1, "final_status": "DONE",
        "transport_identity": "retained test session anchor", "outage_heartbeat": "sent and stored while coordinator process was dead"}
    report = {"result": "PASS", "telepathy": "0.8.0", "base_commit": sha,
        "tracked_patch_sha256": hashlib.sha256(diff).hexdigest(), "checks": reports,
        "boundaries": ["scripted peers, no model frontend", "isolated TELEPATHY_HOME", "stable session anchors are a test contract",
            "flock is advisory: exclusion proven for cooperating test writers", "no power-loss test", "no lease implementation or production watchdog added"]}
    (base / "report.json").write_text(json.dumps(report, indent=2) + "\n")
    print(json.dumps({"result": "PASS", "P0": "PASS", "P1": "PASS", "evidence": str(base)}, indent=2))
except Exception as exc:
    (base / "failure.json").write_text(json.dumps({"result": "FAIL", "error": str(exc), "completed": reports}, indent=2) + "\n")
    print("Evidence:", base)
    raise
finally:
    for proc in reversed(processes):
        stop(proc)
