import json, os, shutil, subprocess, sys, unittest
from pathlib import Path
ROOT=Path(__file__).resolve().parents[1]
SCRIPTS=ROOT/'scripts'
class Tests(unittest.TestCase):
 def setUp(self):
  self.scratch=ROOT/'.scratch/tmp/tests';shutil.rmtree(self.scratch,ignore_errors=True);self.project=self.scratch/'project';shutil.copytree(ROOT/'examples/project',self.project)
  subprocess.run(['git','init','-q',str(self.project)],check=True);subprocess.run(['git','-C',str(self.project),'config','user.email','test@example.invalid'],check=True);subprocess.run(['git','-C',str(self.project),'config','user.name','Test'],check=True);subprocess.run(['git','-C',str(self.project),'add','.'],check=True);subprocess.run(['git','-C',str(self.project),'commit','-qm','init'],check=True);self.sha=subprocess.check_output(['git','-C',str(self.project),'rev-parse','HEAD'],text=True).strip()
 def resolve(self,event='SESSION_STARTED',extra=None):
  req={'event':event,'role':'COORDINATOR','subject':{'projectId':'coord','assignmentId':'a1','baseSha':self.sha},'additionalDocumentIds':extra or []}
  rp=self.scratch/'request.yaml';rp.write_text(__import__('yaml').safe_dump(req));out=self.scratch/'context.json'
  cp=subprocess.run([sys.executable,str(SCRIPTS/'resolve-action-context'),'--project-root',str(self.project),'--request',str(rp),'--budgets',str(ROOT/'config/CONTEXT-BUDGETS.yaml'),'--output',str(out)],text=True,capture_output=True)
  return cp,json.loads(out.read_text())
 def test_resolve_success(self):
  cp,c=self.resolve();self.assertEqual(cp.returncode,0,cp.stderr);self.assertEqual(c['admission']['result'],'ADMITTED');self.assertEqual(len(c['documents']['required']),3)
 def test_unknown_event_fails_closed(self):
  cp,c=self.resolve('NO_SUCH_EVENT');self.assertNotEqual(cp.returncode,0);self.assertEqual(c['admission']['result'],'INVALID_ADMISSION')
 def test_historical_rejected(self):
  cp,c=self.resolve(extra=['history']);self.assertNotEqual(cp.returncode,0);self.assertTrue(c['documents']['rejectedAsAuthority'])
 def test_digest_drift_detected(self):
  cp,c=self.resolve();self.assertEqual(cp.returncode,0);ctx=self.scratch/'context.json';(self.project/'AGENTS.md').write_text('changed')
  v=subprocess.run([sys.executable,str(SCRIPTS/'check-action-context'),str(ctx),'--project-root',str(self.project)]);self.assertNotEqual(v.returncode,0)
 def test_launcher_dry_run(self):
  cp,c=self.resolve();self.assertEqual(cp.returncode,0)
  assignment={'assignmentId':'a1','baseSha':self.sha,'assignedWorktree':str(self.project/'worktrees/a1'),'session':{'role':'COORDINATOR','agentId':'a'},'tools':{'allowedExecutables':['python3']},'realSideEffects':{'allowed':False}}
  (self.project/'worktrees/a1').mkdir(parents=True);ap=self.scratch/'assignment.json';ap.write_text(json.dumps(assignment));wp=self.scratch/'binding.json';wp.write_text(json.dumps({'projectRoot':str(self.project),'repositoryRoot':str(self.project)}));bp=self.scratch/'bootstrap.json'
  b=subprocess.run([sys.executable,str(SCRIPTS/'build-agent-bootstrap'),'--assignment',str(ap),'--context',str(self.scratch/'context.json'),'--workspace-binding',str(wp),'--output',str(bp)]);self.assertEqual(b.returncode,0)
  receipt=self.scratch/'receipt.json';l=subprocess.run([sys.executable,str(SCRIPTS/'launch-agent'),'--bootstrap',str(bp),'--receipt',str(receipt),'--','python3','-c','print(1)'],cwd=self.project);self.assertEqual(l.returncode,0);self.assertFalse(json.loads(receipt.read_text())['executed'])
if __name__=='__main__': unittest.main()
