#!/usr/bin/env python3
from __future__ import annotations
import argparse, hashlib, json, os, signal, subprocess, sys, time
from pathlib import Path
import yaml

def token_digest(token):return 'sha256:'+hashlib.sha256(token.encode()).hexdigest()
def invoke(adapter,config,op,request,response,env):
    cmd=[sys.executable,adapter,op,'--config',config,'--request',str(request),'--response',str(response)]
    return subprocess.run(cmd,env=env,stdout=subprocess.PIPE,stderr=subprocess.PIPE,text=True)
def terminate(proc,policy,events,reason):
    events.append({'type':'FENCE','reason':reason,'at':time.time()})
    if proc.poll() is not None:return
    target=-proc.pid if policy['termination']['killProcessGroup'] else proc.pid
    os.kill(target,getattr(signal,policy['termination']['gracefulSignal']))
    try:proc.wait(timeout=policy['termination']['gracefulTimeoutSeconds'])
    except subprocess.TimeoutExpired:os.kill(target,getattr(signal,policy['termination']['forceSignal']));proc.wait()
def main():
    p=argparse.ArgumentParser();p.add_argument('--bootstrap',required=True);p.add_argument('--registry-adapter',required=True);p.add_argument('--registry-config',required=True);p.add_argument('--policy',required=True);p.add_argument('--receipt',required=True);p.add_argument('command',nargs=argparse.REMAINDER);a=p.parse_args()
    b=json.loads(Path(a.bootstrap).read_text());policy=yaml.safe_load(Path(a.policy).read_text());token=os.getenv('COORD_SESSION_LEASE_TOKEN','');events=[];errors=[]
    if not token:errors.append('LEASE_TOKEN_MISSING')
    if b.get('bootstrapValidation',{}).get('result')!='ADMITTED':errors.append('BOOTSTRAP_NOT_ADMITTED')
    subj=b.get('actionContext',{}).get('subject',{});session=b.get('session',{});scratch=Path(b['workspaceBinding']['projectRoot'])/'.scratch/tmp/supervisor';scratch.mkdir(parents=True,exist_ok=True)
    req={'projectId':subj.get('projectId'),'assignmentId':subj.get('assignmentId'),'baseSha':subj.get('baseSha'),'role':session.get('role'),'agentId':session.get('agentId'),'leaseTokenDigest':token_digest(token) if token else None}
    rq=scratch/'admit-request.json';rs=scratch/'admit-response.json';rq.write_text(json.dumps(req));env=os.environ.copy();cp=invoke(a.registry_adapter,a.registry_config,'admit',rq,rs,env)
    resp=json.loads(rs.read_text()) if rs.exists() else {}
    if cp.returncode or resp.get('status')!='ADMITTED':errors.append('REGISTRY_ADMISSION_FAILED')
    for k in ['projectId','assignmentId','leaseTokenDigest']:
        if resp.get(k)!=req.get(k):errors.append('REGISTRY_BINDING_MISMATCH:'+k)
    out=Path(a.receipt);out.parent.mkdir(parents=True,exist_ok=True)
    if errors:out.write_text(json.dumps({'kind':'SupervisorReceipt','errors':errors,'events':events},indent=2)+'\n');return 1
    cmd=a.command[1:] if a.command and a.command[0]=='--' else a.command
    if not cmd:return 1
    env.update({'COORD_AGENT_SESSION_ID':resp['sessionId'],'COORD_AGENT_GENERATION':str(resp['generation'])})
    proc=subprocess.Popen(cmd,cwd=b['workspaceBinding']['projectRoot'],env=env,start_new_session=True)
    failures=0;last_revision=str(resp['registryRevision']);fenced=False
    while proc.poll() is None:
        time.sleep(policy['heartbeatIntervalSeconds']);hreq={**req,'sessionId':resp['sessionId'],'generation':resp['generation'],'registryRevision':last_revision};rq.write_text(json.dumps(hreq));cp=invoke(a.registry_adapter,a.registry_config,'heartbeat',rq,rs,env)
        h=json.loads(rs.read_text()) if rs.exists() else {};reason=None
        if cp.returncode:failures+=1
        else: failures=0
        if failures>=policy['maxConsecutiveHeartbeatFailures']:reason='heartbeat_failure_limit'
        if h.get('status') in ['FENCED','REVOKED','EXPIRED','REJECTED']:reason='registry_status_fenced'
        if h and h.get('generation')!=resp['generation']:reason='generation_mismatch'
        if h and h.get('leaseTokenDigest')!=req['leaseTokenDigest']:reason='lease_digest_mismatch'
        if h and str(h.get('registryRevision',''))<last_revision:reason='registry_revision_rollback'
        if reason:terminate(proc,policy,events,reason);fenced=True;break
        if h.get('registryRevision') is not None:last_revision=str(h['registryRevision'])
        events.append({'type':'HEARTBEAT','status':h.get('status'),'revision':last_revision,'at':time.time()})
    exitcode=proc.wait();creq={**req,'sessionId':resp['sessionId'],'generation':resp['generation'],'exitCode':exitcode,'fenced':fenced};rq.write_text(json.dumps(creq));invoke(a.registry_adapter,a.registry_config,'complete',rq,rs,env)
    receipt={'kind':'SupervisorReceipt','sessionId':resp['sessionId'],'generation':resp['generation'],'registryRevision':last_revision,'fenced':fenced,'exitCode':exitcode,'events':events,'errors':errors};out.write_text(json.dumps(receipt,indent=2)+'\n');return 1 if fenced else exitcode
if __name__=='__main__':raise SystemExit(main())
