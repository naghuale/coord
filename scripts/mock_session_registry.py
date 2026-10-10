#!/usr/bin/env python3
import argparse,hashlib,json
from pathlib import Path
p=argparse.ArgumentParser();p.add_argument('operation');p.add_argument('--request',required=True);p.add_argument('--response',required=True);a=p.parse_args();r=json.loads(Path(a.request).read_text());seed=(r.get('projectId','')+r.get('assignmentId','')).encode();sid='session_'+hashlib.sha256(seed).hexdigest()[:16]
out={'apiVersion':'coord.dev/v1alpha1','kind':'AgentSessionAdmissionResponse','projectId':r.get('projectId'),'assignmentId':r.get('assignmentId'),'sessionId':sid,'generation':1,'leaseTokenDigest':r.get('leaseTokenDigest'),'registryRevision':'mock-rev-1','status':'ADMITTED'};Path(a.response).write_text(json.dumps(out,indent=2)+'\n')
