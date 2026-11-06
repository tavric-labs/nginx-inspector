"""All services share loopback; nginx sees only the demo backend."""
import signal
import subprocess
import sys
import time
from pathlib import Path

processes = []
Path('/data/static-example.txt').write_bytes(b'nginx file-backed response\n' * 8192)
stopping = False


def stop(*_):
    global stopping
    stopping = True


for sig in (signal.SIGINT, signal.SIGTERM):
    signal.signal(sig, stop)
for command in (['/app/nginx-inspector', 'serve', '--db', '/data/inspector.sqlite3', '--control-file', '/data/control'],
                [sys.executable, 'dev/backend.py'],
                ['/opt/nginx/sbin/nginx', '-c', '/app/dev/nginx.conf', '-g', 'daemon off;']):
    processes.append(subprocess.Popen(command))
    if len(processes) == 1:
        # The shared file must exist before nginx workers map it.
        for _ in range(50):
            if Path('/data/control').exists():
                break
            time.sleep(.1)
while not stopping and all(p.poll() is None for p in processes):
    time.sleep(.25)
for p in reversed(processes):
    if p.poll() is None:
        p.terminate()
for p in processes:
    try:
        p.wait(timeout=5)
    except subprocess.TimeoutExpired:
        p.kill()
sys.exit(0 if stopping else 1)
