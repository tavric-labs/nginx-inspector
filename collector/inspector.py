#!/usr/bin/env python3
"""Loopback-only nginx event collector, SQLite history, web viewer and terminal CLI."""
import argparse
import collections
import ctypes
import http.server
import ipaddress
import json
import math
import mmap
import os
import queue
import re
import signal
import socket
import sqlite3
import stat
import threading
import time
import urllib.parse
import urllib.request
from pathlib import Path

FIELDS = {
    'schema', 'id', 'started_ms', 'duration_ms', 'client', 'method', 'path', 'status',
    'request_bytes', 'response_body_bytes', 'wire_bytes', 'connection_requests',
    'rewrite_visits', 'phases_ms', 'request_headers', 'response_headers', 'upstream',
    'tls', 'worker_sent', 'worker_dropped',
}
NESTED = {
    'phases_ms': {'post_read', 'rewrite', 'access', 'content', 'header_filter', 'first_body_filter'},
    'request_headers': {'content-type', 'content-length'},
    'response_headers': {'content-type'},
    'upstream': {'address', 'connect_s', 'header_s', 'response_s', 'status', 'cache_status'},
    'tls': {'version', 'cipher'},
}


def parse_duration(value):
    match = re.fullmatch(r'([1-9][0-9]*)(s|m|h)', value)
    if not match:
        raise ValueError('use a duration such as 30s, 10m or 1h')
    seconds = int(match[1]) * {'s': 1, 'm': 60, 'h': 3600}[match[2]]
    if seconds > 86400:
        raise ValueError('maximum capture duration is 24h')
    return seconds


class CaptureControl:
    """An aligned, shared 64-bit monotonic deadline; supported on Linux x86-64.

    A short heartbeat means nginx stops observing within 1.5s if this process dies.
    The inode is never replaced: already-running workers keep the same mapping.
    """
    def __init__(self, path):
        if ctypes.sizeof(ctypes.c_void_p) != 8:
            raise RuntimeError('capture control requires a 64-bit host')
        self.path = Path(path)
        self.path.parent.mkdir(parents=True, exist_ok=True)
        self.fd = os.open(self.path, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW | os.O_CLOEXEC, 0o644)
        info = os.fstat(self.fd)
        if not stat.S_ISREG(info.st_mode) or info.st_uid != os.geteuid() or info.st_nlink != 1:
            os.close(self.fd)
            raise ValueError('control must be an owned regular file with one link')
        os.ftruncate(self.fd, 8)
        os.fchmod(self.fd, 0o644)
        self.mapping = mmap.mmap(self.fd, 8)
        self.word = ctypes.c_uint64.from_buffer(self.mapping)
        self.word.value = 0
        self.condition = threading.Condition()
        self.until = None
        self.closed = False
        self.thread = threading.Thread(target=self._heartbeat, name='capture-control')
        self.thread.start()

    def _publish(self):
        now = time.monotonic()
        if self.until is not None and self.until <= now:
            self.until = None
        self.word.value = 0 if self.until is None else int(min(now + 1.5, self.until) * 1000)

    def start(self, seconds=None):
        if seconds is not None and (type(seconds) not in (int, float) or not math.isfinite(seconds)
                                    or not 0 < seconds <= 86400):
            raise ValueError('seconds must be positive and at most 86400')
        with self.condition:
            self.until = math.inf if seconds is None else time.monotonic() + seconds
            self._publish()
            self.condition.notify_all()
        return self.status()

    def stop_capture(self):
        with self.condition:
            self.until = None
            self.word.value = 0
            self.condition.notify_all()
        return self.status()

    def status(self):
        with self.condition:
            self._publish()
            remaining = None if self.until is None or math.isinf(self.until) else max(0, math.ceil(self.until - time.monotonic()))
            return dict(active=self.until is not None, remaining_seconds=remaining)

    def _heartbeat(self):
        with self.condition:
            while not self.closed:
                self._publish()
                self.condition.wait(None if self.until is None else .25)

    def close(self):
        if self.closed:
            return
        self.stop_capture()
        with self.condition:
            self.closed = True
            self.condition.notify_all()
        self.thread.join()
        del self.word
        self.mapping.close()
        os.close(self.fd)


def validate(raw):
    if len(raw) > 8192:
        raise ValueError('oversized event')
    event = json.loads(raw)
    if not isinstance(event, dict) or set(event) - FIELDS or event.get('schema') != 1:
        raise ValueError('unknown schema or field')
    for field in ('id', 'client', 'method', 'path'):
        if not isinstance(event.get(field), str) or len(event[field]) > 256:
            raise ValueError('invalid string')
    if not re.fullmatch(r'[0-9-]{1,100}', event['id']):
        raise ValueError('invalid id')
    # Discard query strings even from alternate emitters. No raw event is persisted.
    event['path'] = event['path'].split('?', 1)[0]
    for field in ('started_ms', 'duration_ms', 'status', 'request_bytes', 'response_body_bytes',
                  'connection_requests', 'rewrite_visits', 'worker_sent', 'worker_dropped'):
        value = event.get(field)
        if type(value) is not int or not 0 <= value <= 2**63 - 1:
            raise ValueError('invalid integer')
    if not 100 <= event['status'] <= 599:
        raise ValueError('invalid status')
    if abs(event['started_ms'] - time.time() * 1000) > 86400000:
        raise ValueError('timestamp outside ingestion window')
    if event.get('wire_bytes') is not None and not re.fullmatch(r'\d{1,20}', str(event['wire_bytes'])):
        raise ValueError('invalid wire size')
    for field, allowed in NESTED.items():
        value = event.get(field)
        if not isinstance(value, dict) or set(value) - allowed:
            raise ValueError('unknown nested field')
        for item in value.values():
            if item is not None and (type(item) not in (str, int) or len(str(item)) > 256):
                raise ValueError('invalid nested value')
    return event


class Store:
    def __init__(self, path, retention_hours=24, max_events=100000):
        self.path = str(path)
        self.retention_hours = retention_hours
        self.max_events = max_events
        with self.connect() as db:
            db.execute('PRAGMA journal_mode=WAL')
            db.execute('CREATE TABLE IF NOT EXISTS events ('
                       'seq INTEGER PRIMARY KEY, id TEXT UNIQUE, started_ms INTEGER, '
                       'status INTEGER, duration_ms INTEGER, path TEXT, client TEXT, event TEXT)')
            db.execute('CREATE INDEX IF NOT EXISTS events_started ON events(started_ms)')

    def connect(self):
        return sqlite3.connect(self.path, timeout=5)

    def insert(self, batch):
        rows = [(e['id'], e['started_ms'], e['status'], e['duration_ms'], e['path'], e['client'],
                 json.dumps(e, separators=(',', ':'), allow_nan=False)) for e in batch]
        cutoff = int((time.time() - self.retention_hours * 3600) * 1000)
        with self.connect() as db:
            db.executemany('INSERT OR IGNORE INTO events '
                           '(id,started_ms,status,duration_ms,path,client,event) VALUES (?,?,?,?,?,?,?)', rows)
            db.execute('DELETE FROM events WHERE started_ms < ?', (cutoff,))
            db.execute('DELETE FROM events WHERE seq <= '
                       '(SELECT seq FROM events ORDER BY seq DESC LIMIT 1 OFFSET ?)', (self.max_events,))

    def recent(self, limit=100):
        with self.connect() as db:
            return [json.loads(row[0]) for row in db.execute(
                'SELECT event FROM events ORDER BY seq DESC LIMIT ?', (min(max(limit, 1), 100),))]

    def get(self, event_id):
        with self.connect() as db:
            row = db.execute('SELECT event FROM events WHERE id=?', (event_id,)).fetchone()
            return json.loads(row[0]) if row else None

    def summary(self):
        # Percentiles are exact for the latest 10k events; window is explicit in the response.
        with self.connect() as db:
            total = db.execute('SELECT COUNT(*) FROM events').fetchone()[0]
            rows = db.execute('SELECT duration_ms,path,status,client FROM events '
                              'ORDER BY seq DESC LIMIT 10000').fetchall()
        durations = sorted(row[0] for row in rows)
        def percentile(p):
            return durations[max(0, math.ceil(len(durations) * p) - 1)] if durations else None
        return dict(retained=total, window=len(rows), p50_ms=percentile(.5), p95_ms=percentile(.95),
                    p99_ms=percentile(.99), routes=collections.Counter(r[1] for r in rows).most_common(20),
                    statuses=collections.Counter(r[2] for r in rows),
                    clients=collections.Counter(r[3] for r in rows).most_common(10),
                    errors=sum(r[2] >= 500 for r in rows))


PAGE = '''<!doctype html><meta charset="utf-8"><title>nginx inspector</title>
<meta name="viewport" content="width=device-width"><style>
body{font:16px system-ui;margin:2em auto;max-width:1100px;padding:0 1em;background:#101820;color:#e9f0f5}
button{cursor:pointer}td,th{text-align:left;padding:.6em;border-bottom:1px solid #53616c}
pre{white-space:pre-wrap;overflow-wrap:anywhere}table{width:100%}button{padding:.4em}
</style><h1>nginx inspector</h1><p id="capture"></p><p>Refreshes every 2 seconds. Bodies and query strings are excluded.</p>
<pre id="summary"></pre><table><thead><tr><th>Time</th><th>Method / path</th><th>Status</th><th>ms</th><th>Inspect</th></tr></thead>
<tbody id="rows"></tbody></table><h2>Request detail</h2><pre id="detail">Select a request.</pre>
<script>
async function refresh(){try{
const [s,events,capture]=await Promise.all([fetch('/api/summary').then(r=>r.json()),fetch('/api/events').then(r=>r.json()),fetch('/api/capture').then(r=>r.json())]);
document.getElementById('capture').textContent=capture.active?'Capture running'+(capture.remaining_seconds===null?' until stopped':' — '+capture.remaining_seconds+' seconds remaining'):'Capture stopped. Saved history is still available.';
document.getElementById('summary').textContent=JSON.stringify(s,null,2);
const rows=document.getElementById('rows');rows.replaceChildren();
for(const e of events){const tr=document.createElement('tr');
for(const v of [new Date(e.started_ms).toISOString(),e.method+' '+e.path,e.status,e.duration_ms]){
const td=document.createElement('td');td.textContent=v;tr.append(td);}
const td=document.createElement('td'),b=document.createElement('button');b.textContent='Open';
b.onclick=async()=>{const v=await fetch('/api/events/'+encodeURIComponent(e.id)).then(r=>r.json());
document.getElementById('detail').textContent=JSON.stringify(v,null,2);};td.append(b);tr.append(td);rows.append(tr);}
}catch(e){document.getElementById('summary').textContent='Collector unavailable';}}
refresh();setInterval(refresh,2000);
</script>'''


class Collector:
    def __init__(self, store, udp_port=19876, http_port=19877, control_file=None, viewer_host=None):
        self.store, self.stop = store, threading.Event()
        self.pending = queue.Queue(maxsize=10000)
        self.stats = collections.Counter()
        self.failure = None
        self.udp = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        self.udp.setsockopt(socket.SOL_SOCKET, socket.SO_RCVBUF, 4 * 1024 * 1024)
        self.udp.bind(('127.0.0.1', udp_port))
        self.udp.settimeout(.5)
        owner = self
        class Handler(http.server.BaseHTTPRequestHandler):
            def log_message(self, *args):
                pass

            def reply(self, result, code=200):
                data = json.dumps(result, allow_nan=False).encode()
                self.send_response(code)
                self.send_header('Content-Type', 'application/json')
                self.send_header('Content-Length', str(len(data)))
                self.send_header('Cache-Control', 'no-store')
                self.end_headers()
                self.wfile.write(data)

            def do_POST(self):
                if self.server is not owner.http:
                    return self.reply({'error': 'viewer is read-only; use loopback controls'}, 403)
                host = self.headers.get('Host')
                if host not in {f'127.0.0.1:{self.server.server_port}', f'localhost:{self.server.server_port}'}:
                    return self.reply({'error': 'local host required'}, 403)
                if self.headers.get('Origin') not in (None, 'http://' + host):
                    return self.reply({'error': 'same origin required'}, 403)
                if self.headers.get('Content-Type') != 'application/json':
                    return self.reply({'error': 'application/json required'}, 415)
                try:
                    length = int(self.headers.get('Content-Length', '0'))
                    if not 1 <= length <= 256:
                        raise ValueError('invalid length')
                    self.connection.settimeout(3)
                    payload = json.loads(self.rfile.read(length))
                    if not isinstance(payload, dict) or set(payload) - {'seconds'}:
                        raise ValueError('invalid fields')
                    if self.path == '/api/capture/start':
                        result = owner.control.start(payload.get('seconds'))
                    elif self.path == '/api/capture/stop' and not payload:
                        result = owner.control.stop_capture()
                    else:
                        return self.reply({'error': 'not found'}, 404)
                except (ValueError, TypeError, UnicodeError, TimeoutError):
                    return self.reply({'error': 'invalid capture request'}, 400)
                self.reply(result)

            def do_GET(self):
                parsed = urllib.parse.urlsplit(self.path)
                if parsed.path == '/':
                    data, mime, code = PAGE.encode(), 'text/html; charset=utf-8', 200
                else:
                    code, mime = 200, 'application/json'
                    if parsed.path == '/health':
                        result = dict(status='error' if owner.failure else 'ok', stats=dict(owner.stats), capture=owner.control.status(),
                                      queue=owner.pending.qsize())
                        code = 503 if owner.failure else 200
                    elif parsed.path == '/api/events':
                        result = owner.store.recent()
                    elif parsed.path == '/api/capture':
                        result = owner.control.status()
                    elif parsed.path.startswith('/api/events/'):
                        result = owner.store.get(urllib.parse.unquote(parsed.path.rsplit('/', 1)[1]))
                        if result is None:
                            code, result = 404, {'error': 'not found'}
                    elif parsed.path == '/api/summary':
                        result = owner.store.summary()
                    else:
                        code, result = 404, {'error': 'not found'}
                    data = json.dumps(result, allow_nan=False).encode()
                self.send_response(code)
                self.send_header('Content-Type', mime)
                self.send_header('Content-Length', str(len(data)))
                self.send_header('Cache-Control', 'no-store')
                self.send_header('X-Content-Type-Options', 'nosniff')
                self.send_header('Content-Security-Policy', "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src 'self'; frame-ancestors 'none'")
                self.end_headers()
                self.wfile.write(data)
        self.http = http.server.ThreadingHTTPServer(('127.0.0.1', http_port), Handler)
        self.http.daemon_threads = True
        self.viewer = None
        try:
            if viewer_host and viewer_host != '127.0.0.1':
                address = ipaddress.IPv4Address(viewer_host)
                if address.is_unspecified or address.is_multicast:
                    raise ValueError('viewer-host must be a specific local IPv4 address')
                self.viewer = http.server.ThreadingHTTPServer((str(address), self.http.server_port), Handler)
                self.viewer.daemon_threads = True
            self.control = CaptureControl(control_file or (str(store.path) + '.control'))
        except BaseException:
            self.udp.close()
            self.http.server_close()
            if self.viewer:
                self.viewer.server_close()
            raise

    def receive(self):
        while not self.stop.is_set():
            try:
                raw, _ = self.udp.recvfrom(8193)
                if not self.control.status()['active']:
                    self.stats['inactive_dropped'] += 1
                    continue
                self.pending.put_nowait(validate(raw))
                self.stats['received'] += 1
            except socket.timeout:
                pass
            except queue.Full:
                self.stats['queue_dropped'] += 1
            except (ValueError, TypeError, UnicodeError, RecursionError):
                self.stats['invalid'] += 1

    def persist(self):
        while not self.stop.is_set() or not self.pending.empty():
            batch = []
            try:
                batch.append(self.pending.get(timeout=.25))
                while len(batch) < 200:
                    try:
                        batch.append(self.pending.get_nowait())
                    except queue.Empty:
                        break
                self.store.insert(batch)
                self.stats['persisted'] += len(batch)
            except queue.Empty:
                continue
            except sqlite3.Error:
                self.failure = 'storage failure'
                self.stats['storage_dropped'] += len(batch)
                time.sleep(.25)

    def run(self):
        workers = [threading.Thread(target=self.receive), threading.Thread(target=self.persist)]
        for worker in workers:
            worker.start()
        servers = [self.http] + ([self.viewer] if self.viewer else [])
        http_workers = [threading.Thread(target=server.serve_forever) for server in servers]
        for worker in http_workers:
            worker.start()
        self.stop.wait()
        self.control.stop_capture()
        for server in servers:
            server.shutdown()
        for worker in workers:
            worker.join()
        self.udp.close()
        for server in servers:
            server.server_close()
        for worker in http_workers:
            worker.join()
        self.control.close()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest='command', required=True)
    serve = sub.add_parser('serve')
    serve.add_argument('--db', default='inspector.sqlite3')
    serve.add_argument('--udp-port', type=int, default=19876)
    serve.add_argument('--http-port', type=int, default=19877)
    serve.add_argument('--retention-hours', type=int, default=24)
    serve.add_argument('--max-events', type=int, default=100000)
    serve.add_argument('--control-file', help='shared heartbeat file mapped by nginx')
    serve.add_argument('--viewer-host', help='optional specific IPv4 address for a read-only viewer; controls stay on loopback')
    top = sub.add_parser('top')
    top.add_argument('--url', default='http://127.0.0.1:19877')
    top.add_argument('--once', action='store_true')
    for name in ('start', 'stop', 'status'):
        command = sub.add_parser(name)
        command.add_argument('--url', default='http://127.0.0.1:19877')
        if name == 'start':
            command.add_argument('--for', dest='duration', help='stop automatically after e.g. 10m or 1h')
    args = parser.parse_args()
    if args.command in ('start', 'stop', 'status'):
        path = '/api/capture'
        data = None
        if args.command != 'status':
            payload = {}
            if args.command == 'start' and args.duration:
                try:
                    payload['seconds'] = parse_duration(args.duration)
                except ValueError as error:
                    parser.error(str(error))
            path += '/' + args.command
            data = json.dumps(payload).encode()
        request = urllib.request.Request(args.url + path, data, {'Content-Type': 'application/json'})
        with urllib.request.urlopen(request, timeout=5) as response:
            print(json.dumps(json.load(response), indent=2))
    elif args.command == 'top':
        while True:
            with urllib.request.urlopen(args.url + '/api/summary', timeout=5) as response:
                value = json.load(response)
            print(json.dumps(value, indent=2), flush=True)
            if args.once:
                return
            time.sleep(2)
    else:
        if args.retention_hours < 1 or not 1 <= args.max_events <= 10000000:
            parser.error('positive retention and max-events 1..10000000 required')
        os.umask(0o077)
        Path(args.db).parent.mkdir(parents=True, exist_ok=True)
        collector = Collector(Store(args.db, args.retention_hours, args.max_events), args.udp_port, args.http_port, args.control_file, args.viewer_host)
        for sig in (signal.SIGINT, signal.SIGTERM):
            signal.signal(sig, lambda *_: collector.stop.set())
        collector.run()


if __name__ == '__main__':
    main()
