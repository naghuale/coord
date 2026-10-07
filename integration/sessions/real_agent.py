#!/usr/bin/env python3
"""Opt-in real AI acceptance test, using the common Coord CLI rather than a vendor protocol."""
import argparse
import json
import os
from pathlib import Path
import signal
import subprocess
import time

parser = argparse.ArgumentParser()
parser.add_argument("--owner-message-file", required=True)
parser.add_argument("--owner-source", default="owner-supplied file")
parser.add_argument("--timeout", type=int, default=240)
args = parser.parse_args()
root = Path(__file__).resolve().parents[2]
state = root / "state" / ("role-ai-" + str(time.time_ns()))
state.mkdir(mode=0o700, parents=True)
base = [str(root / "bin" / "coord"), "--state", str(state), "--server", "none"]
env = os.environ.copy()
service = None
agent_process = None
service_log = (state / "service.log").open("wb")
report = {"result": "FAIL", "test_kind": "real separate OpenCode AI through Coord local session API",
          "owner_source": args.owner_source, "evidence": str(state)}


def private(name, value):
    path = state / name
    raw = value if isinstance(value, str) else json.dumps(value, ensure_ascii=False, indent=2)
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(fd, "w") as output:
        output.write(raw)
    return path


def cli(*values, success=True):
    result = subprocess.run(base + list(values), capture_output=True, text=True, timeout=40)
    if success and result.returncode:
        raise RuntimeError(f"coord {values[0]} failed: {result.stderr}")
    if not success and result.returncode == 0:
        raise RuntimeError("stale connection accepted")
    return result.stdout


def data(*values):
    return json.loads(cli(*values))


def credential(output):
    return output.split("credential-file=", 1)[1].strip()


def start():
    global service
    service = subprocess.Popen(base + ["_serve"], stdout=service_log, stderr=service_log)
    time.sleep(0.3)
    for _ in range(100):
        if service.poll() is not None:
            raise RuntimeError("service exited")
        if (state / "coord.sock").exists():
            cli("rooms")
            return
        time.sleep(0.1)
    raise RuntimeError("service startup timeout")


def stop():
    global service
    cli("stop")
    service.wait(timeout=15)
    if service.returncode:
        raise RuntimeError("service shutdown failed")
    service = None


def model(turn, prompt, native_session=None):
    global agent_process
    import shlex
    command = ["opencode", "run", "--pure", "--format", "json", "--dir", str(root)]
    if native_session:
        command += ["--session", native_session]
    command += [prompt]
    private(f"turn-{turn}-command.json", command)
    stdout_path = state / f"turn-{turn}.jsonl"
    stderr_path = state / f"turn-{turn}.stderr"
    with stdout_path.open("wb") as output, stderr_path.open("wb") as error:
        agent_process = subprocess.Popen(command, stdout=output, stderr=error, env=env,
                                         start_new_session=True)
        print(f"AI turn {turn} started; private output: {stdout_path}", flush=True)
        try:
            code = agent_process.wait(timeout=args.timeout)
        except subprocess.TimeoutExpired:
            os.killpg(agent_process.pid, signal.SIGTERM)
            agent_process.wait(timeout=15)
            raise RuntimeError(f"AI turn {turn} timeout")
        agent_process = None
    if code:
        raise RuntimeError(f"AI turn {turn} exit {code}; inspect private stderr")
    native = native_session
    for line in stdout_path.read_text().splitlines():
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            continue
        native = native or event.get("sessionID") or event.get("part", {}).get("sessionID")
    if not native:
        raise RuntimeError("AI native session ID was not reported")
    return native


try:
    import shlex
    start()
    room = cli("room", "create", "ROLE-ACCEPTANCE",
               "--title", "Owner, coordinator and independent AI reviewer").strip()
    coordinator = data("session", "assign", room, "--agent-id", "codex-coordinator",
                       "--role", "coordinator", "--instructions",
                       "Coordinate this acceptance only; do not edit project source.")
    coord_id = coordinator["session_id"]
    coord_file = credential(cli("session", "resume", coord_id, "--connection",
                                "codex-acceptance-context", "--operation", "coord-attach-1"))
    cli("session", "ready", coord_id, "--credential-file", coord_file,
        "--operation", "coord-ready-1", "--regulation", "coord-v1")
    reviewer = data("session", "assign", room, "--agent-id", "opencode-ai-reviewer",
                    "--role", "reviewer", "--instructions",
                    "Review the universal entry prompt. No source edits, no Crewflow access. "
                    "Use your scoped Coord API and checkpoint; never print credentials.")
    reviewer_id = reviewer["session_id"]
    invitation = data("session", "invite", reviewer_id, "--operation", "invite-reviewer")
    owner_message = Path(args.owner_message_file).read_text()
    cli("say", room, owner_message)
    cli("session", "say", coord_id, "--credential-file", coord_file,
        "--operation", "coord-request-1",
        "Review the owner's proposed entry prompt for both assigned and unassigned entry. "
        "Reply in this room, explain role separation, and save a checkpoint.")
    quoted_base = " ".join(shlex.quote(x) for x in base)
    common = (root / "cmd" / "coord" / "bootstrap.md").read_text()
    first_prompt = (
        "You are a separate real AI reviewer for an owner-authorized Coord acceptance test. "
        "Execute CLI commands yourself using your bash tool; a final text answer alone fails. "
        "Do not edit source, run git commits, start other models or access Crewflow.\n"
        + common + "\nAccept exactly this invitation:\n" + invitation["command"]
        + "\nThe accept response gives credential-file. Use it, never print its contents. "
        + f"Your expected session_id is {reviewer_id}; role reviewer; agent_id opencode-ai-reviewer. "
        + "Read session context, ready with regulation coord-v1 and operation reviewer-ready-1, "
        + "reply via session say with operation reviewer-reply-1. Then save checkpoint via --stdin "
        + 'with operation reviewer-checkpoint-1: progress="prompt reviewed", pending_actions=[], '
        + "artifact_refs=[], acknowledged_seq equal to the last event you processed. "
        + "Report only public IDs and your review.\nAll commands use:\n" + quoted_base)
    native = model(1, first_prompt)
    first_file = str(state / "clients" / f"{reviewer_id}-1.json")
    context = data("session", "context", reviewer_id, "--credential-file", first_file)
    replies = [e for e in context["events"]
               if e["kind"] == "message" and e.get("session") == reviewer_id]
    if not replies or not context["member"]["ready"]:
        raise RuntimeError("AI did not send a scoped reply and readiness")
    checkpoint_before = context["member"]["checkpoint"]
    if not checkpoint_before["progress"]:
        raise RuntimeError("AI did not save checkpoint")
    private("checkpoint-before.json", checkpoint_before)
    stop()
    print("Service stopped after real AI checkpoint; restarting same state.", flush=True)
    start()
    coord_file = credential(cli("session", "resume", coord_id, "--credential-file", coord_file,
                                "--connection", "codex-acceptance-context", "--operation", "coord-resume-2"))
    cli("session", "say", coord_id, "--credential-file", coord_file,
        "--operation", "coord-request-2",
        "The service was restarted. Restore the same logical session, confirm your previous "
        "checkpoint and role, then explain what remains unverified. Save a new checkpoint.")
    second_prompt = (
        "Continue the same Coord role session after a real service restart. "
        "Execute commands yourself. Do not edit source or read secrets.\n"
        f"Resume: {quoted_base} session resume {reviewer_id} --credential-file "
        f"{shlex.quote(first_file)} --connection {shlex.quote('opencode:' + native)} "
        "--operation reviewer-resume-2\n"
        "Use the new credential-file. Read context and verify checkpoint progress='prompt reviewed', "
        "role reviewer and generation=2. Ready with operation reviewer-ready-2 and coord-v1; "
        "reply via session say operation reviewer-reply-2, then checkpoint operation reviewer-checkpoint-2 "
        "with progress='resumed and reviewed', no pending actions, and acknowledged_seq of last processed event. "
        "A final answer without these actual commands fails.")
    recovered_native = model(2, second_prompt, native)
    assert recovered_native == native
    second_file = str(state / "clients" / f"{reviewer_id}-2.json")
    after = data("session", "context", reviewer_id, "--credential-file", second_file)
    replies = [e for e in after["events"]
               if e["kind"] == "message" and e.get("session") == reviewer_id]
    assert len(replies) >= 2 and after["member"]["generation"] == 2
    assert after["member"]["checkpoint"]["progress"] == "resumed and reviewed"
    cli("session", "say", reviewer_id, "--credential-file", first_file,
        "--operation", "stale-probe", "stale must fail", success=False)
    history = data("history", "export", "--content")
    assert history["backlog"] == 0
    projection = (state / "history" / "projection.json").read_text()
    for path in (state / "clients").glob("*.json"):
        secret = json.loads(path.read_text())["auth"]["token"]
        assert secret not in projection
    assert (state / "owner.key").read_text() not in projection
    private("transcript.json", after["events"])
    report.update(result="PASS", room=room, reviewer_session=reviewer_id,
                  coordinator_session=coord_id, native_ai_session=native,
                  owner_event_count=sum(e["kind"] == "message" and e["role"] == "owner"
                                        for e in after["events"]),
                  reviewer_reply_count=len(replies), generation=2,
                  checkpoint_before=checkpoint_before["progress"],
                  checkpoint_after=after["member"]["checkpoint"]["progress"],
                  stale_rejected=True, credentials_not_exported=True,
                  history_commit=history["commit"], owner_live_terminal=False,
                  owner_input="verbatim owner message supplied through file",
                  transport="private local Unix API; not a Telepathy test")
    stop()
except Exception as error:
    report["error"] = str(error)
finally:
    if agent_process is not None:
        os.killpg(agent_process.pid, signal.SIGTERM)
        agent_process.wait(timeout=15)
    if service is not None:
        service.terminate()
        service.wait(timeout=15)
    service_log.close()
    path = private("report.json", report)
    print(f"{report['result']}: {path}", flush=True)
if report["result"] != "PASS":
    raise SystemExit(1)
