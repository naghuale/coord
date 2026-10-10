import hashlib, json, os, subprocess, sys, tempfile, unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
SCRIPTS = ROOT / 'scripts'


class SandboxSessionRegistryTests(unittest.TestCase):
    def setUp(self):
        self.scratch = ROOT / '.scratch/tmp/tests/sandbox'
        self.scratch.mkdir(parents=True, exist_ok=True)

    def test_registry_client_mock(self):
        with tempfile.TemporaryDirectory(dir=self.scratch) as td:
            td = Path(td)
            token = 'secret'
            req = {
                'projectId': 'coord',
                'assignmentId': 'a1',
                'leaseTokenDigest': 'sha256:' + hashlib.sha256(token.encode()).hexdigest(),
            }
            rq = td / 'r.json'
            rs = td / 's.json'
            rq.write_text(json.dumps(req))
            cp = subprocess.run(
                [sys.executable, str(SCRIPTS / 'session_registry_client.py'),
                 'admit',
                 '--adapter-command', f'{sys.executable} {SCRIPTS}/mock_session_registry.py',
                 '--request', str(rq),
                 '--response', str(rs)],
            )
            self.assertEqual(cp.returncode, 0)
            self.assertEqual(json.loads(rs.read_text())['status'], 'ADMITTED')

    def test_sandbox_fails_closed_without_bwrap_or_env(self):
        with tempfile.TemporaryDirectory(dir=self.scratch) as td:
            receipt = Path(td) / 'r.json'
            env = os.environ.copy()
            for k in ['COORD_PROJECT_ROOT', 'COORD_REPOSITORY_ROOT', 'COORD_ASSIGNED_WORKTREE']:
                env.pop(k, None)
            cp = subprocess.run(
                [sys.executable, str(SCRIPTS / 'sandbox_exec.py'),
                 '--profiles', str(ROOT / 'config/SANDBOX-PROFILES.yaml'),
                 '--profile', 'evaluation-linux',
                 '--receipt', str(receipt),
                 '--', 'true'],
                env=env,
            )
            self.assertNotEqual(cp.returncode, 0)
            self.assertTrue(json.loads(receipt.read_text())['errors'])


if __name__ == '__main__':
    unittest.main()
