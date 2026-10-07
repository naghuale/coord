#!/usr/bin/env python3
"""Isolated CLI/session persistence test. No AI model or user Telepathy session."""
import json
import os
from pathlib import Path
import subprocess
import time

ROOT = Path(__file__).resolve().parents[2]
BINARY = ROOT / "bin" / "coord"
STATE = ROOT / "state" / ("session-smoke-" + str(time.time_ns()))
STATE.mkdir(mode=0o700, parents=True)
BASE = [str(BINARY), "--state", str(STATE), "--server", "none"]
process = None
log = (STATE / "service.log").open("wb")


def cli(*args, stdin=None, success=True):
    result = subprocess.run(BASE + list(args), input=stdin, text=True,
                            capture_output=True, timeout=35)
    if success and result.returncode:
        raise RuntimeError(f"{args[0]} failed: {result.stderr}")
    if not success and result.returncode == 0:
        raise RuntimeError(f"{args[0]} unexpectedly succeeded")
    return result.stdout


def data(*args):
    return json.loads(cli(*args))


def credentials(output):
    return output.split("credential-file=", 1)[1].strip()


def start():
    global process
    process = subprocess.Popen(BASE + ["_serve"], stdout=log, stderr=log)
    for _ in range(150):
        if process.poll() is not None:
            raise RuntimeError("service exited; inspect service.log")
        if (STATE / "coord.sock").exists():
            # A socket left from the previous process is not readiness.
            try:
                cli("rooms")
                return
            except (RuntimeError, subprocess.TimeoutExpired):
                pass
        time.sleep(0.1)
    raise RuntimeError("service did not become ready")


def stop():
    global process
    cli("stop")
    process.wait(timeout=10)
    if process.returncode:
        raise RuntimeError("service did not stop cleanly")
    process = None


report = {"result": "FAIL", "test_kind": "isolated CLI clients; no AI model",
          "evidence": str(STATE)}
try:
    start()
    room = cli("room", "create", "SESSION-SMOKE").strip()
    other = cli("room", "create", "OTHER-TASK").strip()
    registration = cli("session", "register", "--agent-id", "test-client",
                       "--connection", "context-a", "--operation", "registration-1")
    ticket_file = credentials(registration)
    ticket_id = registration.split("session=", 1)[1].split()[0]
    pending = data("session", "registration", ticket_id,
                   "--credential-file", ticket_file)
    assert pending["state"] == "WAITING_ASSIGNMENT"
    cli("session", "context", ticket_id, "--credential-file", ticket_file,
        success=False)
    assigned = data("session", "assign", room, "--registration", ticket_id,
                    "--role", "executor", "--instructions", "Only SESSION-SMOKE.")
    assert assigned["session_id"] == ticket_id
    output = cli("session", "resume", ticket_id, "--connection", "context-a",
                 "--operation", "attach-1", "--credential-file", ticket_file)
    first = credentials(output)
    cli("session", "ready", ticket_id, "--credential-file", first,
        "--operation", "ready-1", "--regulation", "coord-v1",
        "--capabilities", "test-cli")
    cli("say", room, "Owner test message")
    event = data("session", "say", ticket_id, "--credential-file", first,
                 "--operation", "reply-1", "Client test response")
    repeat = data("session", "say", ticket_id, "--credential-file", first,
                  "--operation", "reply-1", "Client test response")
    assert repeat["id"] == event["id"]
    context = data("session", "context", ticket_id, "--credential-file", first)
    assert all(e["room"] == room for e in context["events"])
    assert context["member"]["checkpoint"]["acknowledged_seq"] == 0
    cp = {"progress": "durable progress", "pending_actions":
          [{"id": "side-effect-1", "state": "executing"}],
          "artifact_refs": ["test-artifact"], "acknowledged_seq": event["seq"]}
    cli("session", "checkpoint", ticket_id, "--credential-file", first,
        "--operation", "checkpoint-1", "--stdin", stdin=json.dumps(cp))
    export = data("history", "export", "--content")
    assert export["commit"] and export["backlog"] == 0
    projection = (STATE / "history" / "projection.json").read_text()
    capability = json.loads(Path(first).read_text())["auth"]["token"]
    assert capability not in projection
    stop()
    # Remove the orphaned socket through the service's flock-protected startup.
    start()
    output = cli("session", "resume", ticket_id, "--credential-file", first,
                 "--connection", "context-restored", "--operation", "resume-2")
    second = credentials(output)
    context = data("session", "context", ticket_id, "--credential-file", second)
    assert context["member"]["generation"] == 2
    assert context["member"]["checkpoint"]["progress"] == "durable progress"
    assert context["member"]["checkpoint"]["pending_actions"][0]["state"] == "unknown"
    cli("session", "say", ticket_id, "--credential-file", first,
        "--operation", "stale-1", "must be rejected", success=False)
    session_other = data("session", "assign", other, "--agent-id", "test-client",
                         "--role", "auditor")
    cli("session", "resume", session_other["session_id"],
        "--connection", "context-a", "--operation", "cross-context",
        success=False)
    cli("session", "context", session_other["session_id"],
        "--credential-file", second, success=False)
    export = data("history", "export", "--content")
    assert export["backlog"] == 0
    report.update(result="PASS", room=room, session=ticket_id,
                  owner_event_visible=True, idempotent_message=True,
                  checkpoint_restored=True, generation=2,
                  stale_generation_rejected=True, unassigned_context_denied=True,
                  shared_context_rejected=True, foreign_session_denied=True,
                  unknown_action_not_reexecuted=True, capability_not_exported=True,
                  history_commit=export["commit"])
    stop()
except Exception as error:
    report["error"] = str(error)
finally:
    if process is not None:
        process.terminate()
        try:
            process.wait(timeout=10)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait(timeout=10)
    log.close()
    path = STATE / "report.json"
    path.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
    print(f"{report['result']}: {path}")
if report["result"] != "PASS":
    raise SystemExit(1)
