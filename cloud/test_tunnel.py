from pathlib import Path
from http.server import BaseHTTPRequestHandler, HTTPServer
import subprocess
import sys
import tempfile
import threading
import time
import unittest

HELPER = Path(__file__).with_name('tunnel.py')


class TunnelTest(unittest.TestCase):
    def run_supervisor(self, duration, child, health_url='http://127.0.0.1:1/sessions'):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        root = Path(directory.name)
        command = [sys.executable, str(HELPER), '--expires-at', str(time.time() + duration),
                   '--pid-file', str(root / 'child.pid'), '--health-url', health_url,
                   '--startup-grace', '0',
                   '--', sys.executable, '-c', child, str(root)]
        process = subprocess.Popen(command, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
        self.addCleanup(process.stderr.close)
        self.addCleanup(lambda: process.kill() if process.poll() is None else None)
        return root, process

    def wait_child(self, root):
        for _ in range(100):
            try:
                return int((root / 'child.pid').read_text())
            except (FileNotFoundError, ValueError):
                time.sleep(0.02)
        self.fail('Child did not start')

    def test_reconnect_and_fixed_expiry(self):
        root, process = self.run_supervisor(2.5, 'import pathlib,sys; p=pathlib.Path(sys.argv[1])/"attempts"; p.open("a").write("attempt\\n"); sys.exit(1)')
        process.wait(timeout=5)
        self.assertEqual(process.returncode, 0, process.stderr.read().decode())
        self.assertEqual(len((root / 'attempts').read_text().splitlines()), 2)
        self.assertFalse((root / 'child.pid').exists())

    def test_termination_reaps_live_child(self):
        root, process = self.run_supervisor(30, 'import time; time.sleep(60)')
        pid = self.wait_child(root)
        process.terminate()
        process.wait(timeout=3)
        self.assertFalse(Path('/proc', str(pid)).exists())
        self.assertFalse((root / 'child.pid').exists())

    def test_forced_supervisor_death_kills_child(self):
        root, process = self.run_supervisor(30, 'import time; time.sleep(60)')
        pid = self.wait_child(root)
        process.kill()
        process.wait(timeout=3)
        for _ in range(100):
            try:
                state = Path('/proc', str(pid), 'stat').read_text().rpartition(')')[2].split()[0]
            except FileNotFoundError:
                break
            if state == 'Z':
                break  # Adopted by init, no longer running.
            time.sleep(0.02)
        else:
            self.fail('SSH child survived forced supervisor death')

    def test_live_but_broken_forward_reconnects(self):
        class Unavailable(BaseHTTPRequestHandler):
            def do_GET(self):
                self.send_response(503)
                self.end_headers()

            def log_message(self, *_args):
                pass

        server = HTTPServer(('127.0.0.1', 0), Unavailable)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        self.addCleanup(server.server_close)
        self.addCleanup(server.shutdown)
        root, process = self.run_supervisor(14,
            'import pathlib,sys,time; p=pathlib.Path(sys.argv[1])/"attempts"; p.open("a").write("attempt\\n"); time.sleep(60)',
            f'http://127.0.0.1:{server.server_port}/sessions')
        process.wait(timeout=18)
        self.assertEqual(process.returncode, 0, process.stderr.read().decode())
        self.assertEqual(len((root / 'attempts').read_text().splitlines()), 2)
        self.assertFalse((root / 'child.pid').exists())


if __name__ == '__main__':
    unittest.main()
