"""Test-only resumable subprocess. SQLite is the authoritative side-effect store."""
import argparse
import json
import os
from pathlib import Path
import sqlite3
import tempfile
import time

def atomic_json(path, value):
    fd, name = tempfile.mkstemp(prefix=".worker-", dir=path.parent)
    try:
        with os.fdopen(fd, "w") as handle:
            json.dump(value, handle, sort_keys=True)
            handle.flush()
            os.fsync(handle.fileno())
        os.replace(name, path)
        directory = os.open(path.parent, os.O_RDONLY)
        try:
            os.fsync(directory)
        finally:
            os.close(directory)
    finally:
        if os.path.exists(name):
            os.unlink(name)

parser = argparse.ArgumentParser()
parser.add_argument("--input", required=True)
parser.add_argument("--pause-after", type=int, default=0)
args = parser.parse_args()
source = Path(args.input).resolve()
params = json.loads(source.read_text())
assert params["resume_mode"] in ("RESUME", "IDEMPOTENT")
identity = (params["task"], params["run"], params["attempt"])
root = source.parent
db = sqlite3.connect(root / "side-effects.sqlite3", timeout=5)
db.execute("PRAGMA journal_mode=WAL")
db.execute("PRAGMA synchronous=FULL")
db.executescript("""
CREATE TABLE IF NOT EXISTS units (
    task TEXT NOT NULL, run TEXT NOT NULL, attempt INTEGER NOT NULL,
    unit INTEGER NOT NULL, payload TEXT NOT NULL, writes INTEGER NOT NULL,
    PRIMARY KEY (task, run, attempt, unit)
);
CREATE TABLE IF NOT EXISTS completions (
    task TEXT NOT NULL, run TEXT NOT NULL, attempt INTEGER NOT NULL,
    writes INTEGER NOT NULL, PRIMARY KEY (task, run, attempt)
);
""")

def count():
    return db.execute("SELECT count(*) FROM units WHERE task=? AND run=? AND attempt=?", identity).fetchone()[0]

for unit in range(4):
    # The side effect and its deduplication key share a transaction. The progress
    # file can lag this commit; on restart the DB, not progress_pct, decides work.
    with db:
        db.execute("INSERT OR IGNORE INTO units VALUES (?,?,?,?,?,1)", (*identity, unit, "data-unit-" + str(unit)))
    done = count()
    checkpoint = {"task": params["task"], "run": params["run"], "attempt": params["attempt"],
        "target_sha": params["target_sha"], "fence": params["fence"], "progress_pct": done * 25,
        "committed_units": done, "pid": os.getpid(), "resume_mode": params["resume_mode"]}
    atomic_json(root / "worker-checkpoint.json", checkpoint)
    if args.pause_after and done == args.pause_after:
        atomic_json(root / "p2-worker-halfway", checkpoint)
        while True:
            time.sleep(1)

with db:
    db.execute("INSERT OR IGNORE INTO completions VALUES (?,?,?,1)", identity)
result = {"outcome": "SUCCESS", "units": count(), "logical_completions": db.execute(
    "SELECT writes FROM completions WHERE task=? AND run=? AND attempt=?", identity).fetchone()[0]}
assert result["units"] == 4 and result["logical_completions"] == 1
atomic_json(root / "worker-result.json", result)
print(json.dumps(result))
db.close()
