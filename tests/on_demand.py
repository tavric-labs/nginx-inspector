"""Prove off/timer/stop suppress native events, not just collector persistence."""
import http.client
import json
import os
import time
import urllib.request

stamp = str(time.time_ns())
API_URL = os.environ.get('INSPECTOR_URL', 'http://127.0.0.1:19877')


def api(path, body=None):
    data = None if body is None else json.dumps(body).encode()
    request = urllib.request.Request(API_URL + path, data,
                                     {'Content-Type': 'application/json'})
    with urllib.request.urlopen(request, timeout=5) as response:
        return json.load(response)


def proxy(path):
    connection = http.client.HTTPConnection('127.0.0.1', 18080, timeout=3)
    connection.request('GET', path)
    response = connection.getresponse()
    assert response.status == 200
    response.read()
    connection.close()


def captured(path):
    for _ in range(30):
        for row in api('/api/events'):
            if row['path'] == path:
                return row
        time.sleep(.1)
    raise AssertionError('request not captured')


api('/api/capture/stop', {})
assert not api('/api/capture')['active']
proxy('/off-' + stamp)
api('/api/capture/start', {})
first_path = '/manual-' + stamp
proxy(first_path)
first = captured(first_path)
api('/api/capture/stop', {})
for _ in range(20):
    proxy('/stopped-' + stamp)
api('/api/capture/start', {'seconds': 1})
timed_path = '/timed-' + stamp
proxy(timed_path)
timed = captured(timed_path)
assert timed['worker_sent'] == first['worker_sent'] + 1, 'nginx emitted while stopped'
time.sleep(1.15)
assert not api('/api/capture')['active']
for _ in range(20):
    proxy('/expired-' + stamp)
api('/api/capture/start', {})
last_path = '/resumed-' + stamp
proxy(last_path)
last = captured(last_path)
assert last['worker_sent'] == timed['worker_sent'] + 1, 'nginx emitted after timer expired'
api('/api/capture/stop', {})
rows = api('/api/events')
for prefix in ('/off-', '/stopped-', '/expired-'):
    assert not any(row['path'] == prefix + stamp for row in rows)
print('PASS: default/off, manual start/stop, timer expiry and resume; native send counters unchanged while off')
