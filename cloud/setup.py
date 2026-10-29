#!/usr/bin/env python3
"""Install owned relay nodes and SSH tunnels from an already-provisioned manifest.

Provisioning, exact ownership and the immutable expiry guard remain external.
This script never creates cloud machines or contacts a production dispatcher.
"""
import argparse
from concurrent.futures import ThreadPoolExecutor
import hashlib
import json
import os
from pathlib import Path
import secrets
import shlex
import subprocess
import time
import uuid
import urllib.error
import urllib.request


def save(path, value):
    path.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    temporary = path.with_suffix(path.suffix + '.tmp')
    temporary.write_text(value if isinstance(value, str) else json.dumps(value, indent=2) + '\n')
    temporary.chmod(0o600)
    temporary.replace(path)


def run(args, **kwargs):
    result = subprocess.run(args, capture_output=True, timeout=300, **kwargs)
    if result.returncode:
        raise RuntimeError('Remote setup failed; inspect the private setup log')
    return result.stdout


def ssh(state, pod):
    return ['ssh', '-F', '/dev/null', '-i', str(state / 'ssh_key'),
            '-p', str(pod['ssh_port']), '-o', 'BatchMode=yes', '-o', 'IdentitiesOnly=yes',
            '-o', 'StrictHostKeyChecking=yes', '-o', 'HostKeyAlgorithms=ssh-ed25519',
            '-o', 'HostKeyAlias=' + pod['id'], '-o', 'UserKnownHostsFile=' + str(state / 'known_hosts'),
            '-o', 'ConnectTimeout=15', '-o', 'ServerAliveInterval=15', '-o', 'ServerAliveCountMax=2']


def config(executor):
    return f'''[identity]
executor_id = "{executor}"
version = "mailbox-research"
[dispatcher]
addr = "127.0.0.1:19001"
yamux_addr = "127.0.0.1:19000"
[tls]
disable = true
[resources]
capacity = 1000000000
max_debuglets = 1
[tesla]
epoch_seconds = 1
[network]
packet_counter = "fallback"
disable_scion_environment = true
public_host = "::1"
public_ports = "20000-20063"
[network.policy]
local_targets = true
[logging]
log_level = "warn"
json_logs = true
[database]
path = "/workspace/mailbox/state/executor.sqlite"
[pricing]
price_per_bw_s = 1
currency = "TEST"
'''


def install(index, pod, args, expires_at):
    folder = args.output / pod['id']
    folder.mkdir(mode=0o700, parents=True, exist_ok=True)
    marker = folder / 'node.json'
    if marker.exists():
        node = json.loads(marker.read_text())
    else:
        token = folder / 'runtime.token'
        save(token, secrets.token_hex(32) + '\n')
        node = {'name': f"Relay {index + 1:02}", 'pod_id': pod['id'],
                'url': f"https://{pod['public_ip']}:{pod['public_port']}",
                'backend': f'[::1]:{29301 + index}',
                'runtime_url': f'http://127.0.0.1:{29401 + index}',
                'token_file': str(token), 'executor_id': str(uuid.uuid4())}
        save(marker, node)
    assert node['pod_id'] == pod['id']
    for name, content in [('executor.toml', config(node['executor_id'])),
                          ('executor-id', node['executor_id'] + '\n'),
                          ('public-url', node['url'] + '\n'),
                          ('backend-port', str(29301 + index) + '\n')]:
        save(folder / name, content)
    options = ssh(args.state, pod)
    target = 'root@' + pod['public_ip']
    # Tunnels are independent of this shell; provider expiry terminates their peer.
    pidfile = folder / 'tunnel.pid'
    if pidfile.exists():
        pid = int(pidfile.read_text())
        try:
            os.kill(pid, 0)
        except ProcessLookupError:
            pidfile.unlink()
        else:
            raise RuntimeError('A tunnel is already recorded; inspect it before rerunning setup')
    with (folder / 'tunnel.log').open('ab') as log:
        process = subprocess.Popen(options + ['-N', '-o', 'ExitOnForwardFailure=yes',
            '-L', f'127.0.0.1:{29401 + index}:127.0.0.1:9100',
            '-R', '127.0.0.1:19000:127.0.0.1:29000',
            '-R', '127.0.0.1:19001:127.0.0.1:29001',
            '-R', f'127.0.0.1:{29301 + index}:[::1]:{29301 + index}', target],
            stdin=subprocess.DEVNULL, stdout=log, stderr=log, start_new_session=True)
    save(pidfile, str(process.pid) + '\n')
    time.sleep(1)
    if process.poll() is not None:
        raise RuntimeError('SSH tunnel failed; inspect its private log')
    run(options + [target, 'umask 077; mkdir -p /workspace/mailbox/bin /workspace/mailbox/state'])
    files = {args.node: 'bin/node', args.guest: 'bin/relay.wasm', args.executor: 'bin/debuglet-executor',
             Path(__file__).with_name('start-node.sh'): 'start-node.sh'}
    files.update({folder / name: name for name in ['runtime.token', 'executor.toml', 'executor-id', 'public-url', 'backend-port']})
    hashes = []
    for source, destination in files.items():
        contents = source.read_bytes()
        run(options + [target, 'umask 077; cat > ' + shlex.quote('/workspace/mailbox/' + destination)], input=contents)
        hashes.append(hashlib.sha256(contents).hexdigest() + '  ' + destination)
    remaining = int(expires_at - time.time())
    if remaining <= 60:
        raise RuntimeError('Fleet expires too soon to start')
    supervisor = f'''[supervisord]
logfile=/workspace/mailbox/state/supervisor.log
pidfile=/workspace/mailbox/state/supervisor.pid
nodaemon=false
[program:mailbox]
command=/usr/bin/timeout --signal=TERM --kill-after=15s {remaining}s /bin/bash /workspace/mailbox/start-node.sh
autorestart=false
stopasgroup=true
killasgroup=true
stdout_logfile=/workspace/mailbox/state/start.log
redirect_stderr=true
'''
    run(options + [target, 'umask 077; cat > /workspace/mailbox/supervisor.conf'], input=supervisor.encode())
    command = ('set -eu; cd /workspace/mailbox; sha256sum -c - >/dev/null; '
               'chmod 700 bin/node bin/debuglet-executor start-node.sh; '
               'if test -f state/supervisor.pid && kill -0 "$(cat state/supervisor.pid)" 2>/dev/null; then exit 20; fi; '
               'supervisord -c /workspace/mailbox/supervisor.conf')
    run(options + [target, command], input=('\n'.join(hashes) + '\n').encode())
    request = urllib.request.Request(node['runtime_url'] + '/sessions', headers={
        'Authorization': 'Bearer ' + Path(node['token_file']).read_text().strip()})
    for _ in range(40):
        try:
            urllib.request.urlopen(request, timeout=2).close()
        except urllib.error.HTTPError as error:
            if error.code == 404:
                break  # Authenticated controller is listening; no guest started.
            raise RuntimeError('Controller readiness returned an unexpected status') from None
        except OSError:
            time.sleep(0.5)
    else:
        raise RuntimeError('Controller did not become ready; inspect private pod logs')
    print(node['name'] + ' controller ready; real guest validation is separate', flush=True)
    return node


def main():
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--state', type=Path, required=True)
    parser.add_argument('--output', type=Path, required=True)
    parser.add_argument('--node', type=Path, required=True)
    parser.add_argument('--guest', type=Path, required=True)
    parser.add_argument('--executor', type=Path, required=True)
    args = parser.parse_args()
    args.state, args.output = args.state.resolve(), args.output.resolve()
    inv = json.loads((args.state / 'inventory.json').read_text())
    pods = sorted(inv['pods'], key=lambda p: int(p['role'].split('-')[-1]))
    if inv['terminate'] or len(pods) != 10 or len({p['public_ip'] for p in pods}) != 10:
        raise RuntimeError('Require ten live owned pods with distinct public IPs')
    with ThreadPoolExecutor(max_workers=5) as pool:
        futures = [pool.submit(install, i, pod, args, inv['expires_at']) for i, pod in enumerate(pods)]
        nodes = [f.result() for f in futures]
    save(args.output / 'fleet.json', [{k: n[k] for k in ('name', 'url', 'backend', 'runtime_url', 'token_file')} for n in nodes])
    print('Fleet saved to ' + str(args.output / 'fleet.json'))


if __name__ == '__main__':
    main()
