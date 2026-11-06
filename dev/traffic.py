"""Generate a bounded request mix; use only against the loopback demo."""
import argparse
import concurrent.futures
import http.client
import json
import math
import time

parser = argparse.ArgumentParser()
parser.add_argument('--requests', type=int, default=5000)
parser.add_argument('--concurrency', type=int, default=8)
parser.add_argument('--port', type=int, default=18080)
parser.add_argument('--path', default='/local')
args = parser.parse_args()


def run(count):
    connection = http.client.HTTPConnection('127.0.0.1', args.port, timeout=10)
    latencies, errors = [], 0
    for _ in range(count):
        started = time.perf_counter()
        connection.request('GET', args.path)
        response = connection.getresponse()
        response.read()
        errors += response.status >= 500
        latencies.append((time.perf_counter() - started) * 1000)
    connection.close()
    return latencies, errors


start = time.perf_counter()
with concurrent.futures.ThreadPoolExecutor(args.concurrency) as pool:
    counts = [args.requests // args.concurrency + (i < args.requests % args.concurrency)
              for i in range(args.concurrency)]
    results = list(pool.map(run, counts))
elapsed = time.perf_counter() - start
samples = sorted(value for values, _ in results for value in values)
print(json.dumps(dict(requests=len(samples), elapsed_s=elapsed, rps=len(samples)/elapsed,
                      p50_ms=samples[math.ceil(len(samples)*.5)-1],
                      p95_ms=samples[math.ceil(len(samples)*.95)-1],
                      p99_ms=samples[math.ceil(len(samples)*.99)-1],
                      errors=sum(value for _, value in results))))
