#!/usr/bin/env python3
from __future__ import annotations
import argparse, hashlib, json, os, shlex, subprocess, sys
from pathlib import Path

def sha256_file(p):
 h=hashlib.sha256();h.update(Path(p).read_bytes());return 'sha256:'+h.hexdigest()
def main():
 p=argparse.ArgumentParser();p.add_argument('operation',choices=['admit','heartbeat','complete','fence']);p.add_argument('--adapter-command',default=os.getenv('COORD_SESSION_REGISTRY_ADAPTER',''));p.add_argument('--request',required=True);p.add_argument('--response',required=True);p.add_argument('--adapter-digest');a=p.parse_args();req=json.loads(Path(a.request).read_text());out=Path(a.response);errors=[]
 if not a.adapter_command: errors.append('SESSION_REGISTRY_ADAPTER_NOT_CONFIGURED')
 exe=shlex.split(a.adapter_command)[0] if a.adapter_command else ''
 if a.adapter_digest and exe and Path(exe).is_file() and sha256_file(exe)!=a.adapter_digest: errors.append('SESSION_REGISTRY_ADAPTER_DIGEST_MISMATCH')
 if errors:
  out.write_text(json.dumps({'kind':'AgentSessionRegistryResponse','status':'NOT_EVALUATED','errors':errors},indent=2)+'\n');print(', '.join(errors),file=sys.stderr);return 1
 cp=subprocess.run(shlex.split(a.adapter_command)+[a.operation,'--request',str(Path(a.request).resolve()),'--response',str(out.resolve())])
 if cp.returncode:return cp.returncode
 resp=json.loads(out.read_text());required=['sessionId','generation','leaseTokenDigest','registryRevision','status'];missing=[k for k in required if k not in resp]
 if missing or resp.get('status') not in ['ADMITTED','REJECTED','FENCED','NOT_EVALUATED']:
  print('SESSION_REGISTRY_RESPONSE_INVALID:'+','.join(missing),file=sys.stderr);return 1
 return 0
if __name__=='__main__':raise SystemExit(main())
