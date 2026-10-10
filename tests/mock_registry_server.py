#!/usr/bin/env python3
import argparse,json
from http.server import BaseHTTPRequestHandler,HTTPServer
class H(BaseHTTPRequestHandler):
 count=0
 def do_POST(self):
  n=int(self.headers.get('Content-Length','0'));d=json.loads(self.rfile.read(n));H.count+=1;status='ADMITTED'
  if '/heartbeat' in self.path and H.count>=3:status='FENCED'
  out={**d,'sessionId':d.get('sessionId','s1'),'generation':1,'registryRevision':str(H.count),'status':status}
  b=json.dumps(out).encode();self.send_response(200);self.send_header('Content-Type','application/json');self.send_header('Content-Length',str(len(b)));self.end_headers();self.wfile.write(b)
 def log_message(self,*a):pass
if __name__=='__main__':
 p=argparse.ArgumentParser();p.add_argument('--port',type=int,required=True);a=p.parse_args();HTTPServer(('127.0.0.1',a.port),H).serve_forever()
