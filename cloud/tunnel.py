#!/usr/bin/env python3
"""Reconnect one owned SSH process until its immutable fleet deadline."""
import argparse
import ctypes
import os
from pathlib import Path
import signal
import subprocess
import time
import urllib.error
import urllib.request


def die_with_parent():
    # Linux: even a forced supervisor kill must not leave its SSH child behind.
    parent = os.getppid()
    if ctypes.CDLL(None, use_errno=True).prctl(1, signal.SIGKILL) != 0:
        raise OSError(ctypes.get_errno(), 'Could not bind child lifetime')
    if os.getppid() != parent or parent == 1:
        os._exit(1)


def healthy(url):
    try:
        urllib.request.urlopen(url, timeout=2).close()
    except urllib.error.HTTPError as error:
        return error.code == 401  # Controller auth runs before the session lock.
    except OSError:
        pass
    return False


def supervise(command, expires_at, pid_file, health_url, startup_grace):
    stopping = False

    def stop(_signum, _frame):
        nonlocal stopping
        stopping = True

    signal.signal(signal.SIGTERM, stop)
    signal.signal(signal.SIGINT, stop)
    delay = 1.0
    while not stopping and time.time() < expires_at:
        child = subprocess.Popen(command, stdin=subprocess.DEVNULL,
                                 preexec_fn=die_with_parent)
        next_check, failures = time.monotonic() + 5, 0
        grace_until, was_ready = time.monotonic() + startup_grace, False
        temporary = pid_file.with_suffix('.tmp')
        try:
            temporary.write_text(str(child.pid) + '\n')
            temporary.chmod(0o600)
            temporary.replace(pid_file)
            while child.poll() is None and not stopping and time.time() < expires_at:
                if time.monotonic() >= next_check:
                    ready = healthy(health_url)
                    was_ready |= ready
                    failures = 0 if ready else failures + 1
                    next_check = time.monotonic() + 5
                    if failures >= 2 and (was_ready or time.monotonic() >= grace_until):
                        print('Forwarded controller unavailable; reconnecting owned tunnel', flush=True)
                        break
                time.sleep(0.1)
        finally:
            if child.poll() is None:
                child.terminate()
                try:
                    child.wait(timeout=1)
                except subprocess.TimeoutExpired:
                    child.kill()
                    child.wait()
            # Do not erase an ownership record replaced by another process.
            if pid_file.exists() and pid_file.read_text().strip() == str(child.pid):
                pid_file.unlink()
        until = min(expires_at, time.time() + delay)
        while not stopping and time.time() < until:
            time.sleep(0.1)
        delay = min(8.0, delay * 2)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--expires-at', type=float, required=True)
    parser.add_argument('--pid-file', type=Path, required=True)
    parser.add_argument('--health-url', required=True)
    parser.add_argument('--startup-grace', type=float, default=120)
    parser.add_argument('command', nargs=argparse.REMAINDER)
    args = parser.parse_args()
    command = args.command[1:] if args.command[:1] == ['--'] else args.command
    if not command:
        parser.error('An SSH command is required')
    supervise(command, args.expires_at, args.pid_file, args.health_url, args.startup_grace)


if __name__ == '__main__':
    main()
