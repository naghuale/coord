#!/usr/bin/env python3
from __future__ import annotations
import argparse, errno, os, platform, sys
from pathlib import Path
import yaml
try:
    import seccomp
except ImportError:
    try: import pyseccomp as seccomp
    except ImportError: seccomp=None

def main():
    p=argparse.ArgumentParser();p.add_argument('--profiles',required=True);p.add_argument('--profile',required=True);p.add_argument('command',nargs=argparse.REMAINDER);a=p.parse_args()
    cmd=a.command[1:] if a.command and a.command[0]=='--' else a.command
    if platform.system()!='Linux': print('SECCOMP_UNSUPPORTED_OS',file=sys.stderr);return 1
    if seccomp is None: print('SECCOMP_BINDING_UNAVAILABLE',file=sys.stderr);return 1
    if not cmd: print('COMMAND_MISSING',file=sys.stderr);return 1
    prof=yaml.safe_load(Path(a.profiles).read_text())['profiles'][a.profile]
    if prof.get('noNewPrivileges',True):
        # libseccomp's CTL_NNP requests no_new_privs while loading.
        pass
    action=seccomp.ERRNO(int(prof.get('errno',errno.EPERM))) if prof['defaultAction']=='ERRNO' else seccomp.KILL_PROCESS
    flt=seccomp.SyscallFilter(defaction=action)
    missing=[]
    for name in prof.get('allow',[]):
        try: flt.add_rule(seccomp.ALLOW,name)
        except Exception: missing.append(name)
    if missing: print('SECCOMP_UNKNOWN_SYSCALLS:'+','.join(missing),file=sys.stderr);return 1
    flt.load()
    os.execvp(cmd[0],cmd)
if __name__=='__main__': raise SystemExit(main())
