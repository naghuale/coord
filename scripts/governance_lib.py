#!/usr/bin/env python3
from __future__ import annotations
import hashlib, json, os
from pathlib import Path
from typing import Any
import yaml

class GovernanceError(Exception):
    def __init__(self, code: str, message: str):
        super().__init__(message); self.code=code; self.message=message
    def as_dict(self): return {"code":self.code,"message":self.message}

def load_data(path: Path) -> Any:
    if not path.is_file(): raise GovernanceError("FILE_MISSING", str(path))
    try:
        text=path.read_text(encoding="utf-8")
        return json.loads(text) if path.suffix==".json" else yaml.safe_load(text)
    except Exception as exc: raise GovernanceError("DATA_INVALID", f"{path}: {exc}")

def dump_json(path: Path, data: Any) -> None:
    path.parent.mkdir(parents=True,exist_ok=True)
    path.write_text(json.dumps(data,indent=2,ensure_ascii=False)+"\n",encoding="utf-8")

def sha256_file(path: Path) -> str:
    h=hashlib.sha256()
    with path.open("rb") as f:
        for chunk in iter(lambda:f.read(1024*1024),b""): h.update(chunk)
    return "sha256:"+h.hexdigest()

def within(child: Path, parent: Path) -> bool:
    try: child.resolve().relative_to(parent.resolve()); return True
    except ValueError: return False

def require_within(path: Path, root: Path, code="PATH_OUTSIDE_ROOT") -> Path:
    p=path.resolve(); r=root.resolve()
    if not within(p,r): raise GovernanceError(code,f"{p} is outside {r}")
    return p

def registry_index(registry: dict) -> dict[str,dict]:
    if registry.get("kind")!="DocumentRegistry": raise GovernanceError("REGISTRY_KIND_INVALID","kind must be DocumentRegistry")
    out={}
    for d in registry.get("documents",[]):
        for key in ("id","path","class","status","readWhen"):
            if key not in d: raise GovernanceError("REGISTRY_ENTRY_INVALID",f"missing {key}: {d}")
        if d["id"] in out: raise GovernanceError("REGISTRY_ID_DUPLICATE",d["id"])
        out[d["id"]]=d
    return out

def authority_check(doc: dict,event: str,request: dict) -> str|None:
    status=doc.get("status")
    if event not in doc.get("readWhen",[]): return "event_not_allowed"
    if status=="historical" and event!="HISTORICAL_INVESTIGATION": return "historical_not_current"
    if status=="legacy" and not request.get("legacyScope",False): return "legacy_not_canonical"
    if status=="proposed" and not request.get("allowProposedDirection",False): return "proposed_not_accepted"
    if doc.get("currentOperationalAuthority") is False and doc.get("class")=="active_context": return "inactive_context"
    return None
