#!/usr/bin/env python3
import argparse,json
from http.server import BaseHTTPRequestHandler,HTTPServer
class H(BaseHTTPRequestHandler):
 count=0; seen={}
 def do_POST(self):
  n=int(self.headers.get('Content-Length','0'));d=json.loads(self.rfile.read(n));key=self.headers.get('Idempotency-Key');H.count+=1
  if ':heartbeat' in self.path and d.get('heartbeatSequence')==1 and key not in H.seen:
   H.seen[key]='retry';self.send_response(503);self.send_header('Retry-After','0');self.end_headers();return
  status='active'
  if ':heartbeat' in self.path and d.get('heartbeatSequence',0)>=2:status='revoked'
  out={**d,'sessionId':d.get('sessionId','s1'),'trustedAgentId':d.get('agentId'),'generation':1,'revision':str(H.count),'status':status}
  H.seen[key]=out;b=json.dumps(out).encode();self.send_response(200);self.send_header('Content-Type','application/json');self.send_header('Content-Length',str(len(b)));self.end_headers();self.wfile.write(b)
 def log_message(self,*a):pass
if __name__=='__main__':
 p=argparse.ArgumentParser();p.add_argument('--port',type=int,required=True);a=p.parse_args();HTTPServer(('127.0.0.1',a.port),H).serve_forever()
