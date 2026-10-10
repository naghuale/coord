#!/usr/bin/env python3
from __future__ import annotations
import argparse, json, os, ssl, sys, urllib.error, urllib.request
from pathlib import Path
import yaml

def get_path(obj,path):
    for part in path.split('.'):
        if not isinstance(obj,dict) or part not in obj:return None
        obj=obj[part]
    return obj

def main():
    p=argparse.ArgumentParser();p.add_argument('operation',choices=['admit','heartbeat','complete','fence']);p.add_argument('--config',required=True);p.add_argument('--request',required=True);p.add_argument('--response',required=True);a=p.parse_args()
    cfg=yaml.safe_load(Path(a.config).read_text());base=os.getenv(cfg['baseUrlEnv'],'').rstrip('/');token=os.getenv(cfg['auth']['tokenEnv'],'');out=Path(a.response)
    errors=[]
    if not base:errors.append('REGISTRY_URL_MISSING')
    if not token:errors.append('REGISTRY_TOKEN_MISSING')
    if errors:out.write_text(json.dumps({'status':'NOT_EVALUATED','errors':errors},indent=2)+'\n');return 1
    reqbody=json.loads(Path(a.request).read_text());op=cfg['operations'][a.operation];path=op['path'].format(**reqbody);url=base+path
    headers={'Content-Type':'application/json',cfg['auth']['header']:cfg['auth']['prefix']+' '+token,'User-Agent':cfg['adapter']['id']+'/'+cfg['adapter']['version']}
    request=urllib.request.Request(url,data=json.dumps(reqbody).encode(),headers=headers,method=op['method'])
    context=ssl.create_default_context() if cfg['http'].get('tlsVerify',True) else ssl._create_unverified_context()
    try:
        with urllib.request.urlopen(request,timeout=cfg['http']['timeoutSeconds'],context=context) as response:
            raw=json.loads(response.read())
    except Exception as exc:
        out.write_text(json.dumps({'status':'NOT_EVALUATED','errors':['REGISTRY_HTTP_ERROR'],'detail':str(exc)},indent=2)+'\n');return 1
    mapped={k:get_path(raw,v) for k,v in cfg['responseMapping'].items()};mapped.update({'apiVersion':'coord.dev/v1alpha1','kind':'AgentSessionRegistryResponse','operation':a.operation})
    required=['sessionId','generation','leaseTokenDigest','registryRevision','status'];missing=[k for k in required if mapped.get(k) is None]
    if missing: mapped['status']='NOT_EVALUATED';mapped['errors']=['REGISTRY_RESPONSE_MISSING:'+','.join(missing)]
    out.write_text(json.dumps(mapped,indent=2)+'\n');return 1 if missing else 0
if __name__=='__main__':raise SystemExit(main())
