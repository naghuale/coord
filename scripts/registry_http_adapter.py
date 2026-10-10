#!/usr/bin/env python3
from __future__ import annotations
import argparse, hashlib, json, os, random, ssl, sys, time, urllib.error, urllib.request
from email.utils import parsedate_to_datetime
from pathlib import Path
import yaml

def get_path(obj,path):
    for part in path.split('.'):
        if not isinstance(obj,dict) or part not in obj:return None
        obj=obj[part]
    return obj

def canonical_json(value): return json.dumps(value,sort_keys=True,separators=(',',':'),ensure_ascii=False)
def idempotency_key(operation,body,policy):
    values=[]
    for field in policy['idempotency']['keyInputs']:
        values.append(body.get(field))
    material={'operation':operation,'values':values}
    return hashlib.sha256(canonical_json(material).encode()).hexdigest()
def retry_delay(attempt,policy,retry_after=None):
    if retry_after:
        try:return min(float(retry_after),policy['retry']['maxDelayMilliseconds']/1000)
        except ValueError:pass
    base=min(policy['retry']['initialDelayMilliseconds']*(policy['retry']['multiplier']**(attempt-1)),policy['retry']['maxDelayMilliseconds'])/1000
    jitter=base*policy['retry'].get('jitterFraction',0);return max(0,base+random.uniform(-jitter,jitter))
def map_status(value,mapping): return mapping.get(str(value),value)
def main():
    p=argparse.ArgumentParser();p.add_argument('operation',choices=['admit','heartbeat','complete','fence']);p.add_argument('--mapping',required=True);p.add_argument('--retry-policy',required=True);p.add_argument('--request',required=True);p.add_argument('--response',required=True);a=p.parse_args()
    cfg=yaml.safe_load(Path(a.mapping).read_text());policy=yaml.safe_load(Path(a.retry_policy).read_text());base=os.getenv(cfg['transport']['baseUrlEnv'],'').rstrip('/');token=os.getenv(cfg['transport']['auth']['tokenEnv'],'');out=Path(a.response);out.parent.mkdir(parents=True,exist_ok=True)
    body=json.loads(Path(a.request).read_text());errors=[]
    if not base:errors.append('REGISTRY_URL_MISSING')
    if not token:errors.append('REGISTRY_TOKEN_MISSING')
    if errors:out.write_text(json.dumps({'status':'NOT_EVALUATED','errors':errors},indent=2)+'\n');return 1
    if a.operation=='heartbeat' and not isinstance(body.get('heartbeatSequence'),int):
        out.write_text(json.dumps({'status':'NOT_EVALUATED','errors':['HEARTBEAT_SEQUENCE_MISSING']},indent=2)+'\n');return 1
    op=cfg['operations'][a.operation];url=base+op['pathTemplate'].format(**body);key=body.get('idempotencyKey') or idempotency_key(a.operation,body,policy);body['idempotencyKey']=key
    headers={'Content-Type':'application/json',cfg['transport']['auth']['header']:cfg['transport']['auth']['prefix']+' '+token,policy['idempotency']['header']:key,'User-Agent':'coord-session-registry-http/1'}
    context=ssl.create_default_context(); attempts=[];raw=None
    for attempt in range(1,int(policy['retry']['maxAttempts'])+1):
        request=urllib.request.Request(url,data=canonical_json(body).encode(),headers=headers,method=op['method'])
        try:
            with urllib.request.urlopen(request,timeout=10,context=context) as response:
                raw=json.loads(response.read());attempts.append({'attempt':attempt,'result':'response','status':response.status});break
        except urllib.error.HTTPError as exc:
            attempts.append({'attempt':attempt,'result':'http_error','status':exc.code})
            if exc.code not in policy['retry']['retryHttpStatuses'] or attempt==policy['retry']['maxAttempts']:break
            time.sleep(retry_delay(attempt,policy,exc.headers.get('Retry-After') if policy['retry'].get('respectRetryAfter') else None))
        except (urllib.error.URLError,TimeoutError,OSError) as exc:
            attempts.append({'attempt':attempt,'result':'transport_error','type':type(exc).__name__})
            if not policy['retry'].get('retryTransportErrors') or attempt==policy['retry']['maxAttempts']:break
            time.sleep(retry_delay(attempt,policy))
    if raw is None:
        out.write_text(json.dumps({'status':'NOT_EVALUATED','errors':['REGISTRY_RETRY_EXHAUSTED'],'idempotencyKey':key,'attempts':attempts},indent=2)+'\n');return 1
    mapped={k:get_path(raw,v) for k,v in cfg['responseMapping'].items()};mapped['status']=map_status(mapped.get('status'),cfg.get('statusMapping',{}));mapped.update({'apiVersion':'coord.dev/v1alpha1','kind':'AgentSessionRegistryResponse','operation':a.operation,'idempotencyKey':key,'attempts':attempts})
    required=['sessionId','generation','leaseTokenDigest','registryRevision','status'];missing=[k for k in required if mapped.get(k) is None]
    if missing:mapped['status']='NOT_EVALUATED';mapped['errors']=['REGISTRY_RESPONSE_MISSING:'+','.join(missing)]
    out.write_text(json.dumps(mapped,indent=2)+'\n');return 1 if missing else 0
if __name__=='__main__':raise SystemExit(main())
