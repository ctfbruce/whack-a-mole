import argparse
import datetime
import http.server
import json
import os
from pathlib import Path
import random
import ssl
import subprocess
import tempfile
import threading
import time
import unittest
from unittest.mock import patch

import compare
import run


class TrafficTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.temp = tempfile.TemporaryDirectory()
        cls.root = Path(cls.temp.name)
        def openssl(*args):
            subprocess.run(["openssl", *args], cwd=cls.root, check=True,
                           stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        openssl("req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "1",
                "-subj", "/CN=Traffic test CA", "-keyout", "ca.key", "-out", "ca.pem",
                "-addext", "basicConstraints=critical,CA:TRUE",
                "-addext", "keyUsage=critical,keyCertSign,cRLSign")
        openssl("req", "-newkey", "rsa:2048", "-nodes", "-subj", "/CN=127.0.0.1",
                "-keyout", "server.key", "-out", "server.csr")
        (cls.root / "extensions").write_text("subjectAltName=IP:127.0.0.1\nbasicConstraints=critical,CA:FALSE\n"
            "keyUsage=critical,digitalSignature,keyEncipherment\nextendedKeyUsage=serverAuth\n"
            "authorityKeyIdentifier=keyid,issuer\nsubjectKeyIdentifier=hash\n")
        openssl("x509", "-req", "-in", "server.csr", "-CA", "ca.pem", "-CAkey", "ca.key",
                "-CAcreateserial", "-days", "1", "-extfile", "extensions", "-out", "server.pem")
        (cls.root / "fast.py").write_text("def next_request(rng, index): return 0, 8, 16\n")

    @classmethod
    def tearDownClass(cls):
        cls.temp.cleanup()

    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.path = Path(self.directory.name)
        self.calls, self.redirect, self.slow, self.malformed = [], False, False, False
        outer = self

        class Handler(http.server.BaseHTTPRequestHandler):
            def log_message(self, *_):
                pass

            def do_POST(self):
                outer.calls.append(self.path)
                data = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
                if outer.redirect:
                    self.send_response(302)
                    self.send_header("Location", outer.url + "/must-not-follow")
                    self.end_headers()
                    return
                if self.headers.get("Authorization") != "Bearer privateSessionToken":
                    self.send_response(401)
                    self.end_headers()
                    return
                self.send_response(200)
                self.send_header("Content-Type", "application/octet-stream")
                if outer.malformed:
                    self.send_header("Transfer-Encoding", "chunked")
                self.end_headers()
                if outer.malformed:
                    self.wfile.write(b"Z\r\nbad chunk\r\n")
                elif outer.slow:
                    for _ in range(data["response_bytes"]):
                        try:
                            self.wfile.write(b"x")
                            self.wfile.flush()
                            time.sleep(.1)
                        except OSError:
                            break
                else:
                    self.wfile.write(b"x" * data["response_bytes"])

        self.server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        tls = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        tls.minimum_version = ssl.TLSVersion.TLSv1_3
        tls.load_cert_chain(self.root / "server.pem", self.root / "server.key")
        self.server.socket = tls.wrap_socket(self.server.socket, server_side=True)
        self.thread = threading.Thread(target=self.server.serve_forever)
        self.thread.start()
        self.url = "https://127.0.0.1:" + str(self.server.server_port)
        self.session = self.path / "session.json"
        self.session.write_text(json.dumps(dict(url=self.url, token="privateSessionToken",
            expires_at=(datetime.datetime.now(datetime.timezone.utc) + datetime.timedelta(seconds=30)).isoformat())))
        self.session.chmod(0o600)
        self.args = argparse.Namespace(session=self.session, ca=self.root / "ca.pem", allow_url=[self.url],
            profile=str(self.root / "fast.py"), seed=1, started_at=None, seconds=.65,
            max_rate=5, max_bytes=10000, trace=self.path / "trace.jsonl")

    def tearDown(self):
        self.server.shutdown()
        self.thread.join()
        self.server.server_close()
        self.directory.cleanup()

    def execute(self):
        return run.run(self.args, threading.Event())

    def test_verified_tls_exchange_and_private_nonsecret_trace(self):
        self.args.seconds = 5
        request_bytes = len(json.dumps({"response_bytes": 16, "padding": "x" * 8}, separators=(",", ":")).encode())
        self.args.max_bytes = 2 * (request_bytes + 16) + 1
        # An ambient proxy cannot receive the session token.
        with patch.dict(os.environ, {"HTTPS_PROXY": "http://127.0.0.1:1", "NO_PROXY": ""}):
            self.assertEqual(self.execute(), 0)
        self.assertEqual(len(self.calls), 2)
        rows = compare.read(self.args.trace)
        self.assertEqual(len(rows), len(self.calls) * 2)
        self.assertEqual({row["outcome"] for row in rows}, {"ok"})
        self.assertTrue(all(row["app_bytes"] == 16 for row in rows if row["direction"] == "rx"))
        text = self.args.trace.read_text()
        self.assertNotIn("privateSessionToken", text)
        self.assertNotIn(self.url, text)
        self.assertEqual(self.args.trace.stat().st_mode & 0o777, 0o600)

    def test_ip_certificate_is_not_accepted_for_wrong_hostname(self):
        value = json.loads(self.session.read_text())
        value["url"] = self.url.replace("127.0.0.1", "localhost")
        self.session.write_text(json.dumps(value))
        self.args.allow_url = [value["url"]]
        self.assertEqual(self.execute(), 1)
        self.assertEqual(self.calls, [])
        self.assertEqual(compare.read(self.args.trace)[0]["outcome"], "transport_error")

    def test_redirect_never_receives_credentials(self):
        self.redirect = True
        self.assertEqual(self.execute(), 1)
        self.assertEqual(self.calls, ["/api/cover"])

    def test_byte_budget_and_existing_evidence_are_preserved(self):
        self.args.max_bytes = 1
        self.assertEqual(self.execute(), 0)
        self.assertEqual(self.calls, [])
        self.assertEqual(self.args.trace.read_text(), "")
        self.args.trace.write_text("retained")
        with self.assertRaises(FileExistsError):
            self.execute()
        self.assertEqual(self.args.trace.read_text(), "retained")

    def test_allowlist_permissions_expiry_and_profile_bounds(self):
        with self.assertRaises(ValueError):
            run.read_session(self.session, ["https://example.invalid"])
        self.session.chmod(0o644)
        with self.assertRaises(ValueError):
            run.read_session(self.session, [self.url])
        self.session.chmod(0o600)
        value = json.loads(self.session.read_text())
        value["expires_at"] = "2000-01-01T00:00:00Z"
        self.session.write_text(json.dumps(value))
        with self.assertRaises(ValueError):
            run.read_session(self.session, [self.url])
        for proposal in [(0, 60001, 1), (0, 1, 262145), (float("nan"), 1, 1)]:
            with self.assertRaises(ValueError):
                run.sample(lambda *_: proposal, random.Random(1), 0, 5)
        self.assertEqual(run.sample(lambda *_: (0, 0, 0), random.Random(1), 0, 5)[0], .2)

    def test_cancel_during_wait_sends_nothing(self):
        stop = threading.Event()
        stop.set()
        self.assertEqual(run.run(self.args, stop), 0)
        self.assertEqual(self.calls, [])

    def test_slow_response_has_total_deadline(self):
        self.slow = True
        self.args.seconds = .65
        started = time.monotonic()
        self.assertEqual(self.execute(), 1)
        self.assertLess(time.monotonic() - started, 1.3)
        self.assertEqual(compare.read(self.args.trace)[0]["outcome"], "deadline")

    def test_malformed_http_records_failure(self):
        self.malformed = True
        self.assertEqual(self.execute(), 1)
        self.assertEqual(compare.read(self.args.trace)[0]["outcome"], "transport_error")


if __name__ == "__main__":
    unittest.main()
