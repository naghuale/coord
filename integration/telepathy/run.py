"""Isolated real Telepathy MCP experiment, not a production supervisor."""
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
trial = evidence_root / ("trial-" + str(time.time_ns()))
trial.mkdir(mode=0o700)
server = os.environ.get("TELEPATHY_SERVER")
if not server or not Path(server).is_file():
    raise SystemExit("Set TELEPATHY_SERVER to the installed Telepathy 0.8.0 dist/server.mjs")
sha = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=root.parent.parent, text=True).strip()
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

def wait_for(path, timeout=25):
    end = time.monotonic() + timeout
    while not path.exists():
        if time.monotonic() > end:
            raise RuntimeError("timeout waiting for " + str(path))
        time.sleep(0.05)

def actor(mode, *args):
    proc = subprocess.Popen([str(project / "bin" / "telepathy-experiment"), "-mode", mode, "-state", str(trial), "-server", server, *args], cwd=root.parent.parent, start_new_session=True)
    processes.append(proc)
    return proc

try:
    # Stable transport identity for the test receiver, not an existing agent.
    anchor = subprocess.Popen(["sleep", "90"], start_new_session=True)
    processes.append(anchor)
    ref = "opencode-" + str(anchor.pid)
    first = actor("agent", "-peer-pid", str(anchor.pid), "-drop-ack")
    wait_for(trial / "agent-ready")
    coord = actor("coordinator", "-to", ref, "-sha", sha)
    wait_for(trial / "crash-window-ready")
    checkpoint_before = json.loads((trial / "agent-checkpoint.json").read_text())
    assert checkpoint_before["WorkCount"] == 0
    stop(first)
    replacement = actor("agent", "-peer-pid", str(anchor.pid))
    wait_for(trial / "coordinator-done")
    assert coord.wait(timeout=10) == 0
    end = time.monotonic() + 10
    while True:
        events = [json.loads(line) for line in (trial / "agent-events.jsonl").read_text().splitlines()]
        if any(e["event"] == "duplicate_task_no_execution" for e in events):
            break
        if time.monotonic() > end:
            raise RuntimeError("duplicate TASK was not checked by agent")
        time.sleep(0.05)
    final = json.loads((trial / "agent-checkpoint.json").read_text())
    assert final["WorkCount"] == 1
    snapshot = json.loads((trial / "runs.json").read_text())
    run = snapshot["runs"]["MCP-ROUNDTRIP-run-1"]
    assert run["Status"] == "DONE", run
    assert run["Attempt"] == 3 and run["Fence"] == 1 and run["OwnerSession"] == ref
    assert len(run["TaskMessages"]) >= 2
    report = {"result": "PASS", "installed_telepathy": "0.8.0", "coordinator_commit": sha,
        "real_coord_core": True, "real_telepathy_mcp_stdio": True, "real_model": False,
        "receiver_identity": "stable live test anchor, retained across process restart",
        "crash_window": "after read/archive and durable TASK checkpoint, before ACK",
        "final_status": run["Status"], "attempt": run["Attempt"], "fence": run["Fence"],
        "work_count": final["WorkCount"], "task_messages": len(run["TaskMessages"]),
        "checks": ["MCP initialize and peer discovery", "TASK/read/application ACK",
            "exact-body duplicate rejected by Telepathy", "receiver process group SIGKILL",
            "checkpoint reload after receiver restart", "ACK timeout retry keeps attempt/fence/owner",
            "RESULT_READY then RESULT commits DONE", "semantic duplicate TASK executes no second work"],
        "not_checked": ["real model or agent frontend hooks", "new-owner recovery/fencing",
            "coordinator SIGKILL/restart", "power-loss durability", "arbitrary subprocess idempotency", "RESULT receipt/replay"]}
    (trial / "report.json").write_text(json.dumps(report, indent=2) + "\n")
    print(json.dumps({"result": "PASS", "evidence": str(trial), "status": run["Status"], "work_count": final["WorkCount"]}, indent=2))
except Exception as exc:
    (trial / "failure.json").write_text(json.dumps({"result": "FAIL", "error": str(exc)}, indent=2) + "\n")
    print("Evidence:", trial)
    raise
finally:
    for proc in reversed(processes):
        stop(proc)
