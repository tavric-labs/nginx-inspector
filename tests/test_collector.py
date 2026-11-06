import copy
import json
import sys
import tempfile
import time
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / 'collector'))
from inspector import Store, validate


def event(i=1):
    return dict(schema=1, id=f'1-1-{i}', started_ms=int(time.time() * 1000), duration_ms=i,
                client='127.0.0.1', method='GET', path='/test', status=200, request_bytes=70,
                response_body_bytes=23, wire_bytes='123', connection_requests=1, rewrite_visits=1,
                phases_ms={'post_read': 0, 'header_filter': i},
                request_headers={'content-type': None}, response_headers={'content-type': 'text/plain'},
                upstream={'address': '127.0.0.1:18081', 'connect_s': '0.001'},
                tls={'version': None, 'cipher': None}, worker_sent=0, worker_dropped=0)


class Validation(unittest.TestCase):
    def test_valid_and_query_removed(self):
        value = event()
        value['path'] = '/test?token=secret'
        result = validate(json.dumps(value).encode())
        self.assertEqual(result['path'], '/test')
        self.assertNotIn('secret', json.dumps(result))

    def test_secret_headers_and_body_rejected(self):
        for field in ('authorization', 'cookie', 'set-cookie', 'x-api-key'):
            value = event()
            value['request_headers'][field] = 'secret'
            with self.assertRaises(ValueError):
                validate(json.dumps(value).encode())
        value = event()
        value['body'] = 'secret'
        with self.assertRaises(ValueError):
            validate(json.dumps(value).encode())

    def test_bad_input(self):
        for raw in (b'not json', b'[]', b'null', b'x' * 8193):
            with self.assertRaises(ValueError):
                validate(raw)

    def test_invalid_numbers_and_timestamps(self):
        for key, value in [('status', 999), ('duration_ms', -1), ('duration_ms', True),
                           ('started_ms', 0), ('request_bytes', float('nan'))]:
            sample = event()
            sample[key] = value
            with self.assertRaises(ValueError):
                validate(json.dumps(sample).encode())


class Storage(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.store = Store(Path(self.tmp.name) / 'events.sqlite3', max_events=3)

    def tearDown(self):
        self.tmp.cleanup()

    def test_history_duplicates_and_count_retention(self):
        self.store.insert([event(i) for i in range(1, 6)])
        self.store.insert([event(5)])
        self.assertEqual([e['id'] for e in self.store.recent()], ['1-1-5', '1-1-4', '1-1-3'])
        self.assertIsNone(self.store.get("' OR 1=1 --"))
        self.assertEqual(self.store.get('1-1-5')['duration_ms'], 5)

    def test_age_retention(self):
        old = event()
        old['started_ms'] -= 25 * 3600 * 1000
        self.store.insert([old, event(2)])
        self.assertIsNone(self.store.get(old['id']))

    def test_summary_percentiles_and_errors(self):
        values = [event(i) for i in range(1, 4)]
        values[2]['status'] = 503
        self.store.insert(values)
        summary = self.store.summary()
        self.assertEqual((summary['retained'], summary['p50_ms'], summary['p95_ms']), (3, 2, 3))
        self.assertEqual(summary['errors'], 1)


if __name__ == '__main__':
    unittest.main()
