"""Exercise native nginx internals against the isolated dev/canary configuration."""
import concurrent.futures
import gzip
import http.client
import json
import os
import time
import urllib.request

API = os.environ.get('INSPECTOR_URL', 'http://127.0.0.1:19877')
RUN_STARTED = int(time.time() * 1000)


def api(path, data=None):
    raw = None if data is None else json.dumps(data).encode()
    request = urllib.request.Request(API + path, raw, {'Content-Type': 'application/json'})
    with urllib.request.urlopen(request, timeout=5) as response:
        return json.load(response)


def call(path, method='GET', data=None, headers=None):
    connection = http.client.HTTPConnection('127.0.0.1', 18080, timeout=10)
    connection.request(method, path, body=data, headers=headers or {})
    response = connection.getresponse()
    result = (response.status, dict(response.getheaders()), response.read())
    connection.close()
    return result


def latest(path):
    for _ in range(50):
        for row in api('/api/events'):
            if row['path'] == path and row['schema'] == 2 and row['started_ms'] >= RUN_STARTED:
                return row
        time.sleep(.1)
    raise AssertionError('native event did not arrive for ' + path)


api('/api/capture/start', {'seconds': 60,'sample_permyriad':10000})
try:
    assert call('/retry')[0] == 200
    retry = latest('/retry')
    assert [row['status'] for row in retry['native']['attempts']] == [502, 200], retry['native']['attempts']
    assert {'error', 'timeout'} <= set(retry['native']['policy']['retry_on'])

    assert call('/streamed')[0] == 200
    streamed = latest('/streamed')['native']
    assert not streamed['policy']['response_buffering']
    assert not streamed['policy']['request_buffering']

    assert call('/no-buffer')[0] == 200
    changed = latest('/no-buffer')['native']['policy']
    assert changed['response_buffering_configured'] and not changed['response_buffering']
    assert changed['buffering_header'] == 'no' and not changed['buffering_header_ignored']

    nonce = str(time.time_ns())
    assert call('/cached?case=' + nonce)[0] == 200
    assert call('/cached?case=' + nonce)[0] == 200
    deadline = time.monotonic() + 5
    while time.monotonic() < deadline:
        cached = [r for r in api('/api/events') if r['path'] == '/cached' and r['schema'] == 2 and r['started_ms'] >= RUN_STARTED]
        if {'MISS', 'HIT'} <= {r['upstream']['cache_status'] for r in cached}:
            break
        time.sleep(.1)
    else:
        raise AssertionError('cache MISS/HIT events did not arrive')
    assert any(r['native']['response']['cache_file_used'] for r in cached if r['upstream']['cache_status'] == 'HIT')

    status, headers, body = call('/compressed', headers={'Accept-Encoding': 'gzip'})
    assert status == 200 and headers.get('Content-Encoding') == 'gzip'
    assert gzip.decompress(body).startswith(b'observable nginx buffers')
    compressed = latest('/compressed')['native']['response']
    assert compressed['content_encoding'] == 'gzip' and compressed['gzip_ratio']

    assert len(call('/file')[2]) == len(b'nginx file-backed response\n' * 8192)
    file_output = latest('/file')['native']['response']
    assert file_output['file_bytes'] > 0 and file_output['sendfile_allowed']

    assert call('/with-auth')[0] == 200
    auth = latest('/with-auth')['native']
    assert any(s['path'] == '/auth-check' and s['status'] == 204 for s in auth['subrequests']), auth['subrequests']
    assert any(t['subrequest'] > 0 for t in auth['trace'])

    upload = b'secret-body-' * 6000
    assert call('/upload', 'POST', upload, {'Content-Type': 'application/octet-stream'})[0] == 200
    request_body = latest('/upload')['native']['request_body']
    assert request_body['filter_calls'] > 0 and request_body['observed_bytes'] == len(upload), request_body
    assert request_body['disk_buffered'] and request_body['temp_file_offset'] >= len(upload)

    assert call('/small-upload-limit', 'POST', b'x' * 100)[0] == 413
    rejected = latest('/small-upload-limit')['native']
    assert rejected['request_body']['declared_bytes'] == 100
    assert rejected['request_body']['max_body_bytes'] == 16
    assert not rejected['attempts']

    assert call('/rate-limited')[0] == 200
    assert call('/rate-limited')[0] == 503
    for _ in range(50):
        rate = [r for r in api('/api/events') if r['path'] == '/rate-limited' and r['status'] == 503 and r['started_ms'] >= RUN_STARTED]
        if rate:
            break
        time.sleep(.1)
    assert rate and rate[0]['native']['policy']['rate_limit_status'] == 'REJECTED', rate
    assert not rate[0]['native']['attempts']

    assert len(call('/bandwidth')[2]) == 12288
    bandwidth = latest('/bandwidth')['native']['policy']
    assert bandwidth['limit_rate_bps'] == 8192 and bandwidth['limit_rate_after_bytes'] == 1024, bandwidth

    with concurrent.futures.ThreadPoolExecutor() as pool:
        pending = pool.submit(call, '/worker-wait?token=worker-query-secret')
        for _ in range(60):
            workers = api('/api/workers')
            live = [request for worker in workers['live_workers'] for request in worker['requests']]
            if any(request['path'] == '/worker-wait' for request in live):
                break
            time.sleep(.05)
        else:
            raise AssertionError('no bounded live worker request observation')
        assert workers['available'] and workers['scoped']
        assert any(w['connection_slots_capacity'] > 0 and w['client_connections']['active'] >= 1 for w in workers['live_workers'])
        assert any(p['role'] == 'worker' and p['rss_bytes'] > 0 for p in workers['processes'])
        assert 'worker-query-secret' not in json.dumps(workers)
        assert pending.result()[0] == 200

    trace = retry['native']['trace']
    assert trace[-1]['event'] == 'log'
    assert any(t['event'] == 'header_filter_return' for t in trace)
    assert all(a['at_ms'] <= b['at_ms'] for a, b in zip(trace, trace[1:]))
    assert retry['native']['nginx']['event_backend'] == 'epoll'
    assert retry['native']['nginx']['pool_reserved_bytes'] >= retry['native']['nginx']['pool_used_bytes']
    assert 'secret-body-' not in json.dumps(api('/api/events'))
    print('PASS: native retry attempts, effective buffering, cache, gzip, file buffers, subrequests, upload spool/limit, phase and event-loop state')
finally:
    api('/api/capture/stop', {})
