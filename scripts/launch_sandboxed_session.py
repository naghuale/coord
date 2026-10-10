#!/usr/bin/env python3
from __future__ import annotations
import argparse, hashlib, json, os, subprocess, sys
from pathlib import Path

def digest_token(token):return 'sha256:'+hashlib.sha256(token.encode()).hexdigest()
def main():
 p=argparse.ArgumentParser();p.add_argument('--bootstrap',required=True);p.add_argument('--registry-adapter',required=True);p.add_argument('--registry-adapter-digest');p.add_argument('--sandbox-profiles',required=True);p.add_argument('--sandbox-profile',required=True);p.add_argument('--receipt',required=True);p.add_argument('--execute',action='store_true');p.add_argument('command',nargs=argparse.REMAINDER);a=p.parse_args();b=json.loads(Path(a.bootstrap).read_text());errors=[]
 if b.get('bootstrapValidation',{}).get('result')!='ADMITTED':errors.append('BOOTSTRAP_NOT_ADMITTED')
 assignment=b.get('assignment',{});subject=b.get('actionContext',{}).get('subject',{});token=os.getenv('COORD_SESSION_LEASE_TOKEN','')
 if not token:errors.append('LEASE_TOKEN_MISSING')
 req={'apiVersion':'coord.dev/v1alpha1','kind':'AgentSessionAdmissionRequest','projectId':subject.get('projectId'),'assignmentId':subject.get('assignmentId'),'baseSha':subject.get('baseSha'),'role':b.get('session',{}).get('role'),'agentId':b.get('session',{}).get('agentId'),'leaseTokenDigest':digest_token(token) if token else None}
 scratch=Path(b['workspaceBinding']['projectRoot'])/'.scratch/tmp/session-launch';scratch.mkdir(parents=True,exist_ok=True);rq=scratch/'admit-request.json';rs=scratch/'admit-response.json';rq.write_text(json.dumps(req,indent=2)+'\n')
 if not errors:
  cp=subprocess.run([sys.executable,str(Path(__file__).with_name('session_registry_client.py')),'admit','--adapter-command',a.registry_adapter,'--request',str(rq),'--response',str(rs)]+(['--adapter-digest',a.registry_adapter_digest] if a.registry_adapter_digest else []))
  if cp.returncode:errors.append('SESSION_REGISTRY_ADMISSION_FAILED')
 resp=json.loads(rs.read_text()) if rs.exists() else {}
 for k,v in [('projectId',req['projectId']),('assignmentId',req['assignmentId']),('leaseTokenDigest',req['leaseTokenDigest'])]:
  if resp.get(k)!=v:errors.append('REGISTRY_BINDING_MISMATCH:'+k)
 if resp.get('status')!='ADMITTED':errors.append('REGISTRY_STATUS_NOT_ADMITTED')
 receipt={'kind':'SandboxedAgentSessionLaunchReceipt','registry':resp,'errors':errors,'executed':False};out=Path(a.receipt);out.parent.mkdir(parents=True,exist_ok=True)
 if errors:out.write_text(json.dumps(receipt,indent=2)+'\n');print('LAUNCH_REJECTED:'+','.join(errors),file=sys.stderr);return 1
 env=os.environ.copy();env['COORD_AGENT_SESSION_ID']=resp['sessionId'];env['COORD_AGENT_GENERATION']=str(resp['generation'])
 cmd=[sys.executable,str(Path(__file__).with_name('sandbox_exec.py')),'--profiles',a.sandbox_profiles,'--profile',a.sandbox_profile,'--receipt',str(scratch/'sandbox-receipt.json')]+(['--execute'] if a.execute else [])+['--']+(a.command[1:] if a.command and a.command[0]=='--' else a.command)
 cp=subprocess.run(cmd,env=env,cwd=b['workspaceBinding']['projectRoot']);receipt.update({'executed':a.execute,'sandboxExitCode':cp.returncode,'sessionId':resp['sessionId'],'generation':resp['generation']});out.write_text(json.dumps(receipt,indent=2)+'\n');return cp.returncode
if __name__=='__main__':raise SystemExit(main())
