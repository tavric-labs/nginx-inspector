"""Exercise the host binary against a private canary config, without host-root changes."""
import http.client
import json
import os
from pathlib import Path
import socket
import subprocess
import sys
import time
import urllib.request

root = Path(sys.argv[1]).resolve()
api_url = os.environ.get('INSPECTOR_URL', 'http://127.0.0.1:19877')
http_port = int(api_url.rsplit(':', 1)[1])
udp_port = int(os.environ.get('INSPECTOR_UDP_PORT', '19876'))
children = []
for port, kind in ((18080, socket.SOCK_STREAM), (18081, socket.SOCK_STREAM),
                   (udp_port, socket.SOCK_DGRAM), (http_port, socket.SOCK_STREAM)):
    with socket.socket(socket.AF_INET, kind) as sock:
        sock.bind(('127.0.0.1', port))
subprocess.run(['/usr/sbin/nginx', '-t', '-p', str(root) + '/', '-c', str(root / 'canary.conf')], check=True)


def start(command, name):
    with (root / (name + '.log')).open('ab') as log:
        child = subprocess.Popen(command, stdin=subprocess.DEVNULL, stdout=log, stderr=log)
    children.append(child)
    return child


def collector():
    return start([sys.executable, str(root / 'inspector.py'), 'serve', '--db', str(root / 'events.sqlite3'),
                  '--control-file', str(root / 'control'), '--udp-port', str(udp_port),
                  '--http-port', str(http_port)], 'collector')


def get(path):
    with urllib.request.urlopen(api_url + path, timeout=3) as response:
        return json.load(response)


def wait_for_collector():
    for _ in range(50):
        try:
            if get('/health')['status'] == 'ok':
                return
        except OSError:
            time.sleep(.1)
    raise RuntimeError('collector did not become healthy')


try:
    receiver = collector()
    wait_for_collector()
    start([sys.executable, str(root / 'backend.py')], 'backend')
    start(['/usr/sbin/nginx', '-p', str(root) + '/', '-c', str(root / 'canary.conf'), '-g', 'daemon off;'], 'nginx')
    time.sleep(.5)
    subprocess.run([sys.executable, str(root / 'integration.py')], check=True)
    subprocess.run([sys.executable, str(root / 'on_demand.py')], check=True)
    receiver.terminate()
    receiver.wait(timeout=5)
    for _ in range(20):
        connection = http.client.HTTPConnection('127.0.0.1', 18080, timeout=2)
        connection.request('GET', '/collector-absent')
        response = connection.getresponse()
        assert response.status == 200
        response.read()
        connection.close()
    receiver = collector()
    wait_for_collector()
    request = urllib.request.Request(api_url + '/api/capture/start', b'{"seconds":30}',
                                     {'Content-Type': 'application/json'})
    with urllib.request.urlopen(request, timeout=3) as response:
        assert json.load(response)['active']
    connection = http.client.HTTPConnection('127.0.0.1', 18080, timeout=2)
    connection.request('GET', '/collector-restored')
    response = connection.getresponse()
    assert response.status == 200
    response.read()
    connection.close()
    for _ in range(50):
        if any(r['path'] == '/collector-restored' for r in get('/api/events')):
            break
        time.sleep(.1)
    else:
        raise AssertionError('no event after collector restart')
    print('PASS: existing host binary loads module; requests survive collector absence; collection resumes')
    print(json.dumps(get('/health')))
finally:
    for child in reversed(children):
        if child.poll() is None:
            child.terminate()
    for child in children:
        try:
            child.wait(timeout=5)
        except subprocess.TimeoutExpired:
            child.kill()
