"""Verify the shipped standalone host/worker over an isolated Telepathy 0.8.0."""
import json
import os
from pathlib import Path
import signal
import subprocess
import time

project = Path(__file__).resolve().parents[2]
binary = project / "bin" / "coord-telepathy"
server = os.environ.get("TELEPATHY_SERVER")
if not server or not Path(server).is_file():
    raise SystemExit("Set TELEPATHY_SERVER to the installed Telepathy 0.8.0 server.mjs")
trial = project / "state" / "telepathy-standalone" / str(time.time_ns())
trial.mkdir(parents=True, mode=0o700)
os.chmod(trial, 0o700)
processes = []
logs = []

def spawn(args, directory=None):
    proc = subprocess.Popen(args, cwd=directory or project, start_new_session=True,
                            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    processes.append(proc)
    return proc

def stop(proc):
    try:
        os.killpg(proc.pid, signal.SIGKILL)
    except ProcessLookupError:
        pass
    proc.wait(timeout=10)

def wait_for(path, proc, timeout=25):
    end = time.monotonic() + timeout
    while not path.exists():
        if proc.poll() is not None:
            raise RuntimeError("process exited early: " + str(proc.returncode))
        if time.monotonic() > end:
            raise RuntimeError("timeout waiting for " + str(path))
        time.sleep(0.05)

def write_private(path, value):
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, "w") as output:
        json.dump(value, output)

try:
    repo = trial / "worktree"
    repo.mkdir()
    subprocess.run(["git", "init", "--quiet", str(repo)], check=True)
    subprocess.run(["git", "-C", str(repo), "-c", "user.name=CoordTest",
                    "-c", "user.email=coord@test.invalid", "commit", "--allow-empty",
                    "--quiet", "-m", "fixture"], check=True)
    sha = subprocess.check_output(["git", "-C", str(repo), "rev-parse", "HEAD"], text=True).strip()
    host_anchor = spawn(["sleep", "120"])
    worker_anchor = spawn(["sleep", "120"])
    host_session = "opencode-" + str(host_anchor.pid)
    worker_session = "opencode-" + str(worker_anchor.pid)
    host_key = json.loads(subprocess.check_output([str(binary), "-generate-key"], text=True))
    worker_key = json.loads(subprocess.check_output([str(binary), "-generate-key"], text=True))
    task = {"Task": "STANDALONE-PROOF", "RunID": "standalone-proof-1", "Attempt": 1,
            "Fence": 1, "TargetSHA": sha, "OwnerSession": worker_session,
            "Worktree": str(repo), "PayloadRef": "configured-stdin",
            "Deadline": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime(time.time() + 600))}
    configs = {}
    for role, anchor, session, key, peer, peer_key in [
        ("host", host_anchor, host_session, host_key, worker_session, worker_key),
        ("worker", worker_anchor, worker_session, worker_key, host_session, host_key)
    ]:
        directory = trial / (role + "-peer")
        directory.mkdir()
        configs[role] = {
            "role": role, "server": server, "home": str(trial / "telepathy"),
            "directory": str(directory), "state": str(trial / (role + "-state")),
            "anchor_pid": anchor.pid, "session": session, "coordinator": host_session,
            "private_key": key["private_key"], "peers": {peer: peer_key["public_key"]},
            "task": task, "exit_on_terminal": role == "host"}
        if role == "worker":
            configs[role].update(prompt="standalone signed proof",
                                command=["/bin/sh", "-c", "printf x >> execution-count; sleep 2; cat"])
        write_private(trial / (role + ".json"), configs[role])
    def actor(role):
        out = open(trial / (role + "-stdout.json"), "w")
        err = open(trial / (role + "-stderr.log"), "w")
        logs.extend([out, err])
        proc = subprocess.Popen([str(binary), "-config", str(trial / (role + ".json"))],
                                cwd=project, start_new_session=True, stdout=out, stderr=err)
        processes.append(proc)
        return proc
    worker = actor("worker")
    wait_for(trial / "worker-state" / "executor.json.lock", worker)
    host = actor("host")
    end = time.monotonic() + 20
    while True:
        path = trial / "worker-state" / "executor.json"
        state = json.loads(path.read_text())["runs"].get(task["RunID"], {}) if path.exists() else {}
        if state.get("Status") == "STARTED":
            break
        if time.monotonic() > end:
            raise RuntimeError("worker did not start before host crash test")
        time.sleep(0.02)
    stop(host)
    (trial / "host-before-restart.json").write_bytes((trial / "host-state" / "runs.json").read_bytes())
    host = actor("host")
    assert host.wait(timeout=30) == 0, "host failed; inspect evidence"
    assert worker.wait(timeout=30) == 0, "worker failed; inspect evidence"
    host_run = json.loads((trial / "host-state" / "runs.json").read_text())["runs"][task["RunID"]]
    worker_run = json.loads((trial / "worker-state" / "executor.json").read_text())["runs"][task["RunID"]]
    assert host_run["Status"] == worker_run["Status"] == "DONE"
    assert not host_run["Outbox"] and not worker_run["Outbox"]
    assert worker_run["ResultMessageID"]
    assert (repo / "execution-count").read_text() == "x"
    # A clean worker restart returns its committed checkpoint without executing.
    restarted = actor("worker")
    assert restarted.wait(timeout=20) == 0
    assert (repo / "execution-count").read_text() == "x"
    report = {"result": "PASS", "real_telepathy_mcp": True, "signed_frames": True,
              "host_status": host_run["Status"], "worker_status": worker_run["Status"],
              "work_count": 1, "worker_restart_without_execution": True, "host_restart_during_execution": True,
              "canonical_crewflow_link": False, "real_model": False}
    (trial / "report.json").write_text(json.dumps(report, indent=2) + "\n")
    print(json.dumps({"result": "PASS", "evidence": str(trial)}, indent=2))
finally:
    for proc in reversed(processes):
        stop(proc)
    for output in logs:
        output.close()
