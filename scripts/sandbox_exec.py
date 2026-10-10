#!/usr/bin/env python3
from __future__ import annotations
import argparse, json, os, platform, resource, shutil, subprocess, sys
from pathlib import Path
import yaml

def expand(value:str,env:dict)->str:
    for k,v in env.items(): value=value.replace('${'+k+'}',v)
    return value

def preexec(limits):
    os.setsid()
    resource.setrlimit(resource.RLIMIT_CPU,(int(limits['cpuSeconds']),int(limits['cpuSeconds'])))
    resource.setrlimit(resource.RLIMIT_AS,(int(limits['addressSpaceBytes']),int(limits['addressSpaceBytes'])))
    resource.setrlimit(resource.RLIMIT_NOFILE,(int(limits['openFiles']),int(limits['openFiles'])))
    resource.setrlimit(resource.RLIMIT_NPROC,(int(limits['processes']),int(limits['processes'])))

def main():
    p=argparse.ArgumentParser();p.add_argument('--profiles',required=True);p.add_argument('--profile',required=True);p.add_argument('--receipt',required=True);p.add_argument('--execute',action='store_true');p.add_argument('command',nargs=argparse.REMAINDER);a=p.parse_args()
    cfg=yaml.safe_load(Path(a.profiles).read_text());prof=cfg['profiles'][a.profile];errors=[];system=platform.system().lower();cmd=a.command[1:] if a.command and a.command[0]=='--' else a.command
    envkeys=['COORD_PROJECT_ROOT','COORD_REPOSITORY_ROOT','COORD_ASSIGNED_WORKTREE','COORD_ACTION_CONTEXT','COORD_AGENT_SESSION_ID','COORD_AGENT_GENERATION'];env={k:os.environ.get(k,'') for k in envkeys}
    if system!=prof['os']: errors.append('SANDBOX_OS_MISMATCH')
    if system!='linux': errors.append('NO_SUPPORTED_OS_SANDBOX_BACKEND')
    bwrap=shutil.which('bwrap')
    if not bwrap: errors.append('BUBBLEWRAP_UNAVAILABLE')
    for k in envkeys[:3]:
        if not env[k]: errors.append('ENV_MISSING:'+k)
    if not cmd: errors.append('COMMAND_MISSING')
    args=[]
    if bwrap and not errors:
        args=[bwrap,'--die-with-parent','--new-session','--unshare-user','--unshare-pid','--unshare-ipc','--unshare-uts','--unshare-cgroup','--ro-bind','/usr','/usr','--ro-bind','/bin','/bin','--ro-bind','/lib','/lib','--proc','/proc','--dev','/dev','--tmpfs','/tmp']
        if Path('/lib64').exists(): args += ['--ro-bind','/lib64','/lib64']
        if prof.get('network')=='deny': args += ['--unshare-net']
        for raw in prof.get('readOnlyRoots',[]):
            path=Path(expand(raw,env)).resolve();
            if path.exists(): args += ['--ro-bind',str(path),str(path)]
        for raw in prof.get('writableRoots',[]):
            path=Path(expand(raw,env)).resolve();path.mkdir(parents=True,exist_ok=True);args += ['--bind',str(path),str(path)]
        clean={k:os.environ[k] for k in prof.get('environmentAllowlist',[]) if k in os.environ}
        clean['HOME']=env['COORD_PROJECT_ROOT']+'/.scratch/home';Path(clean['HOME']).mkdir(parents=True,exist_ok=True)
        args += ['--chdir',env['COORD_PROJECT_ROOT'],'--clearenv']
        for k,v in clean.items(): args += ['--setenv',k,v]
        args += ['--']+cmd
    receipt={'kind':'SandboxLaunchReceipt','profile':a.profile,'backend':'bubblewrap','platform':system,'command':cmd,'sandboxCommand':args,'executed':False,'errors':errors}
    out=Path(a.receipt);out.parent.mkdir(parents=True,exist_ok=True)
    if errors:
        out.write_text(json.dumps(receipt,indent=2)+'\n');print('SANDBOX_REJECTED: '+', '.join(errors),file=sys.stderr);return 1
    if not a.execute:
        out.write_text(json.dumps(receipt,indent=2)+'\n');print('SANDBOX_DRY_RUN_PASS');return 0
    cp=subprocess.run(args,env={},preexec_fn=lambda:preexec(prof['limits']))
    receipt.update({'executed':True,'exitCode':cp.returncode});out.write_text(json.dumps(receipt,indent=2)+'\n');return cp.returncode
if __name__=='__main__': raise SystemExit(main())
