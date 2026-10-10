#!/usr/bin/env python3
import argparse,hashlib,json,sys
from pathlib import Path
import yaml
p=argparse.ArgumentParser();p.add_argument('--openapi',required=True);p.add_argument('--mapping',required=True);p.add_argument('--report',required=True);a=p.parse_args();spec=yaml.safe_load(Path(a.openapi).read_text());m=yaml.safe_load(Path(a.mapping).read_text());found={}
for path,item in spec.get('paths',{}).items():
 for method,op in item.items():
  if isinstance(op,dict) and op.get('operationId'):found[op['operationId']]={'path':path,'method':method.upper()}
missing=[];mismatch=[]
for name,cfg in m['operations'].items():
 oid=cfg['operationId']
 if oid not in found:missing.append(oid)
 elif found[oid]!={'path':cfg['pathTemplate'],'method':cfg['method']}:mismatch.append({'operationId':oid,'expected':cfg,'actual':found[oid]})
report={'kind':'OpenApiMappingReport','openApiDigest':'sha256:'+hashlib.sha256(Path(a.openapi).read_bytes()).hexdigest(),'mappingDigest':'sha256:'+hashlib.sha256(Path(a.mapping).read_bytes()).hexdigest(),'missingOperationIds':missing,'mismatches':mismatch,'result':'PASS' if not missing and not mismatch else 'FAIL'};Path(a.report).write_text(json.dumps(report,indent=2)+'\n');print(report['result']);raise SystemExit(0 if report['result']=='PASS' else 1)
