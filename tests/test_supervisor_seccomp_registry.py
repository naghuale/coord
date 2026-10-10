import json, os, socket, subprocess, sys, tempfile, time, unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
SCRIPTS = ROOT / 'scripts'
CONFIG = ROOT / 'config'
TESTS = ROOT / 'tests'


class SupervisorSeccompRegistryTests(unittest.TestCase):
    def setUp(self):
        self.scratch = ROOT / '.scratch/tmp/tests/supervisor'
        self.scratch.mkdir(parents=True, exist_ok=True)

    def test_http_adapter_and_supervisor_fence(self):
        s = socket.socket()
        s.bind(('127.0.0.1', 0))
        port = s.getsockname()[1]
        s.close()

        server = subprocess.Popen(
            [sys.executable, str(TESTS / 'mock_registry_server.py'), '--port', str(port)],
        )
        time.sleep(0.2)

        try:
            with tempfile.TemporaryDirectory(dir=self.scratch) as td:
                td = Path(td)
                boot = {
                    'bootstrapValidation': {'result': 'ADMITTED'},
                    'actionContext': {
                        'subject': {
                            'projectId': 'coord',
                            'assignmentId': 'a1',
                            'baseSha': '0' * 40,
                        }
                    },
                    'session': {'role': 'EXECUTOR', 'agentId': 'a'},
                    'workspaceBinding': {'projectRoot': str(td)},
                }
                (td / 'b.json').write_text(json.dumps(boot))
                env = os.environ.copy()
                env.update({
                    'COORD_SESSION_REGISTRY_URL': f'http://127.0.0.1:{port}',
                    'COORD_SESSION_REGISTRY_TOKEN': 't',
                    'COORD_SESSION_LEASE_TOKEN': 'lease',
                })
                receipt = td / 'receipt.json'
                cp = subprocess.run(
                    [sys.executable, str(SCRIPTS / 'session_supervisor.py'),
                     '--bootstrap', str(td / 'b.json'),
                     '--registry-adapter', str(SCRIPTS / 'registry_http_adapter.py'),
                     '--registry-config', str(CONFIG / 'SESSION-REGISTRY-HTTP.yaml'),
                     '--policy', str(CONFIG / 'SUPERVISOR.yaml'),
                     '--receipt', str(receipt),
                     '--', sys.executable, '-c', 'import time; time.sleep(60)'],
                    env=env,
                )
                self.assertNotEqual(cp.returncode, 0)
                self.assertTrue(json.loads(receipt.read_text())['fenced'])
        finally:
            server.terminate()
            server.wait()

    def test_seccomp_profile_parses(self):
        import yaml
        p = yaml.safe_load((CONFIG / 'SECCOMP-PROFILES.yaml').read_text())
        self.assertIn('python-agent-v1', p['profiles'])
        self.assertIn('ptrace', p['profiles']['python-agent-v1']['denyExplicit'])


if __name__ == '__main__':
    unittest.main()
