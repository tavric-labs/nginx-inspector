import ctypes
import http.client
import json
from pathlib import Path
import sys
import tempfile
import threading
import time
import unittest
import urllib.error
import urllib.request

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / 'collector'))
from inspector import CaptureControl, Collector, Store, parse_duration


class CaptureTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.path = Path(self.temp.name) / 'control'
        self.control = CaptureControl(self.path)

    def tearDown(self):
        self.control.close()
        self.temp.cleanup()

    def deadline(self):
        return int.from_bytes(self.path.read_bytes(), sys.byteorder)

    def test_default_off_manual_start_and_stop(self):
        self.assertFalse(self.control.status()['active'])
        self.assertEqual(self.deadline(), 0)
        self.control.start()
        self.assertTrue(self.control.status()['active'])
        self.assertIsNone(self.control.status()['remaining_seconds'])
        self.assertGreater(self.deadline(), time.monotonic() * 1000)
        self.control.stop_capture()
        self.assertFalse(self.control.status()['active'])
        self.assertEqual(self.deadline(), 0)

    def test_timed_capture_expires(self):
        self.control.start(.1)
        time.sleep(.15)
        self.assertFalse(self.control.status()['active'])
        self.assertEqual(self.deadline(), 0)

    def test_restart_clears_gate_without_replacing_inode(self):
        self.control.start()
        inode = self.path.stat().st_ino
        self.control.close()
        self.control = CaptureControl(self.path)
        self.assertEqual(self.path.stat().st_ino, inode)
        self.assertEqual(self.deadline(), 0)

    def test_heartbeat_is_short_and_renewed(self):
        self.control.start()
        first = self.deadline()
        self.assertLessEqual(first - time.monotonic() * 1000, 1500)
        time.sleep(.35)
        self.assertGreater(self.deadline(), first)

    def test_invalid_durations_do_not_change_state(self):
        for duration in (-1, 0, True, float('nan'), float('inf'), 86401, '10m'):
            with self.assertRaises(ValueError):
                self.control.start(duration)
        self.assertFalse(self.control.status()['active'])
        for value, expected in [('10s', 10), ('10m', 600), ('1h', 3600)]:
            self.assertEqual(parse_duration(value), expected)
        for value in ('0m', '-1m', 'forever', '25h'):
            with self.assertRaises(ValueError):
                parse_duration(value)


class ControlAPITests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        root = Path(self.temp.name)
        self.collector = Collector(Store(root / 'data.sqlite3'), 0, 0, root / 'control', '127.0.0.2')
        self.thread = threading.Thread(target=self.collector.run)
        self.thread.start()
        self.url = 'http://127.0.0.1:' + str(self.collector.http.server_port)

    def tearDown(self):
        self.collector.stop.set()
        self.thread.join(timeout=5)
        self.assertFalse(self.thread.is_alive())
        self.temp.cleanup()

    def post(self, path, payload, extra=None):
        headers = {'Content-Type': 'application/json', **(extra or {})}
        request = urllib.request.Request(self.url + path, json.dumps(payload).encode(), headers=headers)
        with urllib.request.urlopen(request, timeout=3) as response:
            return json.load(response)

    def test_start_stop_and_status(self):
        result = self.post('/api/capture/start', {'seconds': 60})
        self.assertTrue(result['active'])
        self.assertLessEqual(result['remaining_seconds'], 60)
        self.assertFalse(self.post('/api/capture/stop', {})['active'])

    def test_cross_origin_and_rebinding_rejected(self):
        for header in ({'Origin': 'https://attacker.invalid'}, {'Host': 'attacker.invalid'}):
            with self.assertRaises(urllib.error.HTTPError) as error:
                self.post('/api/capture/start', {}, header)
            self.assertEqual(error.exception.code, 403)
        self.assertFalse(self.collector.control.status()['active'])

    def test_invalid_request_does_not_start_capture(self):
        for payload in ({'seconds': -1}, {'seconds': True}, {'unexpected': 1}):
            with self.assertRaises(urllib.error.HTTPError) as error:
                self.post('/api/capture/start', payload)
            self.assertEqual(error.exception.code, 400)
        self.assertFalse(self.collector.control.status()['active'])

    def test_secondary_viewer_is_read_only_even_with_spoofed_host(self):
        connection = http.client.HTTPConnection('127.0.0.2', self.collector.http.server_port, timeout=3)
        connection.request('GET', '/api/summary')
        response = connection.getresponse()
        self.assertEqual(response.status, 200)
        self.assertEqual(json.loads(response.read())['retained'], 0)
        connection.close()
        connection = http.client.HTTPConnection('127.0.0.2', self.collector.http.server_port, timeout=3)
        connection.request('POST', '/api/capture/start', '{}',
                           {'Content-Type': 'application/json',
                            'Host': f'127.0.0.1:{self.collector.http.server_port}'})
        response = connection.getresponse()
        self.assertEqual(response.status, 403)
        response.read()
        connection.close()
        self.assertFalse(self.collector.control.status()['active'])
