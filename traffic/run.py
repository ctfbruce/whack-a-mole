#!/usr/bin/env python3
"""Bounded, verified-TLS traffic to an explicitly allowed owned entrance."""

import argparse
import datetime
import http.client
import importlib.util
import json
import math
import multiprocessing
import os
from pathlib import Path
import random
import signal
import ssl
import threading
import time
import urllib.error
import urllib.parse
import urllib.request


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, request, fp, code, msg, headers, url):
        raise ValueError("redirect refused")


def origin(value):
    parsed = urllib.parse.urlsplit(value)
    if (parsed.scheme != "https" or not parsed.hostname or parsed.username
            or parsed.password or parsed.path not in ("", "/")
            or parsed.query or parsed.fragment):
        raise ValueError("an HTTPS origin without credentials or path is required")
    parsed.port  # Reject malformed ports before any request.
    return value.rstrip("/")


def read_session(path, allowed):
    if path.stat().st_mode & 0o077:
        raise ValueError("session file must be owner-only (chmod 600)")
    with path.open() as source:
        value = json.loads(source.read(16385))
    url = origin(value["url"])
    if url not in {origin(item) for item in allowed}:
        raise ValueError("session URL is not explicitly allowed")
    token = value["token"]
    if not isinstance(token, str) or not token or not token.isascii() or not token.isalnum():
        raise ValueError("invalid session token")
    expires = datetime.datetime.fromisoformat(value["expires_at"].replace("Z", "+00:00"))
    if expires.tzinfo is None or expires.timestamp() <= time.time():
        raise ValueError("session has expired or has no timezone")
    return url, token, expires.timestamp()


def load_profile(name):
    path = Path(name) if name.endswith(".py") else Path(__file__).parent / "profiles" / (name + ".py")
    spec = importlib.util.spec_from_file_location("traffic_profile", path.resolve())
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module.next_request


def sample(profile, rng, index, rate):
    delay, padding, response = profile(rng, index)
    if (not isinstance(delay, (int, float)) or not math.isfinite(delay) or delay < 0
            or type(padding) is not int or not 0 <= padding <= 60000
            or type(response) is not int or not 0 <= response <= 262144):
        raise ValueError("profile exceeds request, response or delay bounds")
    return max(delay, 1 / rate), padding, response


def request_worker(pipe, url, token, ca, body, expected):
    signal.signal(signal.SIGINT, signal.SIG_IGN)
    received, outcome = 0, "ok"
    try:
        context = ssl.create_default_context(cafile=str(ca))
        context.minimum_version = ssl.TLSVersion.TLSv1_3
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect(),
                                            urllib.request.HTTPSHandler(context=context))
        request = urllib.request.Request(url + "/api/cover", data=body, method="POST", headers={
            "Authorization": "Bearer " + token, "Content-Type": "application/json"})
        with opener.open(request, timeout=10) as response:
            received = len(response.read(expected + 1))
            if response.status != 200 or received != expected or response.headers.get_content_type() != "application/octet-stream":
                outcome = "invalid_response"
    except urllib.error.HTTPError as error:
        outcome = "http_" + str(error.code)
        error.close()
    except (OSError, ValueError, urllib.error.URLError, http.client.HTTPException):
        outcome = "transport_error"
    pipe.send((received, outcome))
    pipe.close()


def exchange(url, token, ca, body, expected, stop, deadline):
    # A process boundary bounds DNS, TLS and slow-drip bodies, not just idle reads.
    context = multiprocessing.get_context("spawn")
    receiver, sender = context.Pipe(duplex=False)
    worker = context.Process(target=request_worker, args=(sender, url, token, ca, body, expected))
    worker.start()
    sender.close()
    deadline = min(deadline, time.monotonic() + 10)
    try:
        while worker.is_alive() and not stop.is_set() and time.monotonic() < deadline:
            worker.join(min(.05, max(0, deadline - time.monotonic())))
        if worker.is_alive():
            worker.terminate()
            worker.join(1)
            if worker.is_alive():
                worker.kill()
                worker.join()
            return 0, "cancelled" if stop.is_set() else "deadline"
        return receiver.recv() if receiver.poll() else (0, "transport_error")
    except EOFError:
        return 0, "transport_error"
    finally:
        receiver.close()
        worker.close()


def run(args, stop):
    url, token, expiry = read_session(args.session, args.allow_url)
    profile, rng = load_profile(args.profile), random.Random(args.seed)
    started = time.time() if args.started_at is None else args.started_at
    deadline = time.monotonic() + min(args.seconds, expiry - time.time())
    used, count = 0, 0
    with os.fdopen(os.open(args.trace, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600), "w") as trace:
        while not stop.is_set():
            delay, padding, expected = sample(profile, rng, count, args.max_rate)
            remaining = deadline - time.monotonic()
            if delay >= remaining or stop.wait(delay):
                break
            if stop.is_set() or time.monotonic() >= deadline:
                break
            body = json.dumps({"response_bytes": expected, "padding": "x" * padding},
                              separators=(",", ":")).encode()
            if used + len(body) + expected + 1 > args.max_bytes:
                break
            begin, wall = time.monotonic(), time.time()
            received, outcome = exchange(url, token, args.ca, body, expected, stop, deadline)
            latency = round((time.monotonic() - begin) * 1000, 3)
            for direction, size, stamp in (("tx", len(body), wall), ("rx", received, time.time())):
                trace.write(json.dumps(dict(elapsed_s=round(stamp - started, 6), direction=direction,
                    app_bytes=size, latency_ms=latency, outcome=outcome, profile=Path(args.profile).stem,
                    request_id=count, kind="cover")) + "\n")
            trace.flush()
            used += len(body) + received
            count += 1
            if outcome != "ok":
                print("Stopped after a rejected or incomplete exchange; see the trace outcome.")
                return 1
    print(f"Complete: {count} exchanges; {used} recorded application bytes. No mailbox messages sent.")
    return 0


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--session", required=True, type=Path)
    parser.add_argument("--ca", required=True, type=Path)
    parser.add_argument("--allow-url", required=True, action="append")
    parser.add_argument("--profile", default="idle", help="idle, browsing, steady, or trusted .py file")
    parser.add_argument("--seconds", type=float, default=60)
    parser.add_argument("--max-rate", type=float, default=2)
    parser.add_argument("--max-bytes", type=int, default=4 * 1024 * 1024)
    parser.add_argument("--seed", type=int, default=1)
    parser.add_argument("--started-at", type=float, help="shared UNIX time origin for comparison traces")
    parser.add_argument("--trace", required=True, type=Path)
    args = parser.parse_args()
    if (not 0 < args.seconds <= 1200 or not 0 < args.max_rate <= 5
            or not 1 <= args.max_bytes <= 16 * 1024 * 1024
            or args.started_at is not None and not math.isfinite(args.started_at)):
        parser.error("duration <= 1200s, rate <= 5/s, budget <= 16 MiB; all must be positive")
    stop = threading.Event()
    for name in (signal.SIGINT, signal.SIGTERM):
        signal.signal(name, lambda *_: stop.set())
    try:
        return run(args, stop)
    except (OSError, ValueError, KeyError, TypeError, AttributeError):
        parser.exit(2, "Invalid private session, profile, CA or output file; no secret details printed.\n")


if __name__ == "__main__":
    raise SystemExit(main())
