"""Alternate inspection on/off on the isolated demo, never on a production proxy."""
import json
from pathlib import Path
import statistics
import subprocess
import sys
import time
import urllib.request

root = Path(__file__).resolve().parents[1]
config = root / 'dev/nginx.conf'
original = config.read_text()
results = []
def capture(command):
    request = urllib.request.Request('http://127.0.0.1:19877/api/capture/' + command, b'{}',
                                     {'Content-Type': 'application/json'})
    with urllib.request.urlopen(request, timeout=5) as response:
        return json.load(response)

capture('start')
try:
    for enabled in (False, True, True, False, False, True):
        config.write_text(original.replace('    inspector on;',
                                          '    inspector on;' if enabled else '    inspector off;'))
        subprocess.run(['docker', 'exec', 'nginx-inspector-dev', '/usr/sbin/nginx',
                        '-s', 'reload', '-c', '/app/dev/nginx.conf'], check=True, capture_output=True)
        time.sleep(.5)
        subprocess.run([sys.executable, str(root / 'dev/traffic.py'), '--requests', '1000'],
                       check=True, capture_output=True)
        result = subprocess.run([sys.executable, str(root / 'dev/traffic.py'), '--requests', '20000'],
                                check=True, capture_output=True, text=True)
        value = dict(enabled=enabled, **json.loads(result.stdout))
        results.append(value)
        print(json.dumps(value), flush=True)
finally:
    config.write_text(original)
    subprocess.run(['docker', 'exec', 'nginx-inspector-dev', '/usr/sbin/nginx',
                    '-s', 'reload', '-c', '/app/dev/nginx.conf'], check=True, capture_output=True)
    capture('stop')
off = [r for r in results if not r['enabled']]
on = [r for r in results if r['enabled']]
base_rps = statistics.median(r['rps'] for r in off)
observed_rps = statistics.median(r['rps'] for r in on)
p95_delta = statistics.median(r['p95_ms'] for r in on) - statistics.median(r['p95_ms'] for r in off)
print(json.dumps(dict(throughput_loss_percent=100 * (1 - observed_rps / base_rps),
                      p95_increase_ms=p95_delta,
                      target_pass=observed_rps >= base_rps * .95 and p95_delta < 1)))
