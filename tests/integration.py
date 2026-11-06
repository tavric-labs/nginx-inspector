"""Run against dev/nginx.conf plus the collector and backend. Uses no real credentials."""
import http.client
import json
import os
import time
import urllib.error
import urllib.request

API_URL = os.environ.get('INSPECTOR_URL', 'http://127.0.0.1:19877')


def call(path, method='GET', data=None, headers=None):
    connection = http.client.HTTPConnection('127.0.0.1', 18080, timeout=5)
    connection.request(method, path, body=data, headers=headers or {})
    response = connection.getresponse()
    status, body = response.status, response.read()
    connection.close()
    return status, body


def events():
    with urllib.request.urlopen(API_URL + '/api/events', timeout=5) as response:
        return json.load(response)


request = urllib.request.Request(API_URL + '/api/capture/start',
                                 b'{"seconds":30,"sample_permyriad":10000}', {'Content-Type': 'application/json'})
with urllib.request.urlopen(request, timeout=5) as response:
    assert json.load(response)['active']

marker = str(time.time_ns())
path = '/known-' + marker
assert call(path + '?token=query-secret', 'POST', b'body-secret',
            {'Authorization': 'Bearer header-secret', 'Cookie': 'cookie-secret',
             'Content-Type': 'text/plain', 'X-Api-Key': 'api-secret'}) == (200, b'hello from test backend\n')
assert call('/error-' + marker)[0] == 503
assert call('/slow-' + marker)[0] == 200
assert call('/broken')[0] == 502
assert call('/redirect')[0] == 204
assert call('/disabled')[0] == 204
# Two responses over the same connection must have distinct IDs and request counters.
c = http.client.HTTPConnection('127.0.0.1', 18080, timeout=5)
for _ in range(2):
    c.request('GET', '/keepalive-' + marker)
    c.getresponse().read()
c.close()
deadline = time.monotonic() + 5
while time.monotonic() < deadline:
    rows = events()
    if len([r for r in rows if r['path'] == '/keepalive-' + marker]) == 2:
        break
    time.sleep(.1)
else:
    raise AssertionError('events did not arrive')
known = next(r for r in rows if r['path'] == path)
assert known['method'] == 'POST' and known['status'] == 200
assert known['response_body_bytes'] == len(b'hello from test backend\n'), known
assert known['upstream']['address'] == '127.0.0.1:18081'
assert known['phases_ms']['header_filter'] is not None
assert known['phases_ms']['post_read'] is not None
assert next(r for r in rows if r['path'] == '/slow-' + marker)['duration_ms'] >= 45
assert any(r['status'] == 502 and r['path'] == '/broken' for r in rows)
assert any(r['path'] == '/local' and r['rewrite_visits'] >= 2 for r in rows)
assert not any(r['path'] == '/disabled' for r in rows)
keepalive = [r for r in rows if r['path'] == '/keepalive-' + marker]
assert len({r['id'] for r in keepalive}) == 2
assert sorted(r['connection_requests'] for r in keepalive) == [1, 2]
serialized = json.dumps(rows)
for secret in ('query-secret', 'body-secret', 'header-secret', 'cookie-secret', 'api-secret', 'not-to-be-captured'):
    assert secret not in serialized, secret
print('PASS: proxy, errors, timing, redirects, opt-out, keepalive, header/body/query exclusion')
