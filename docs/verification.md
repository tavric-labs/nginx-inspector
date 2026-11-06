# Milestones and verification

## Layout

```text
module/                 Dynamic module and nginx build integration
collector/inspector.py  Receiver, validator, SQLite, HTTP viewer and terminal CLI
dev/                    Source-build Dockerfile, packaged-Ubuntu builder, backend, traffic tool
tests/                  Collector unit tests and black-box integration test
docs/                   Architecture, verification and later measured results
compose.yaml            Linux loopback development stack
PROMPT.md               Original project brief
```

## Milestones

| Milestone | Prototype scope | Remaining work |
| --- | --- | --- |
| M1 | Dynamic module, enable/disable/sample controls, one JSON datagram per main request | CI build matrix and module packaging |
| M2 | Phase boundary offsets, allowlisted headers, upstream/TLS variables, response-size observations, keepalive correlation | Full redirect timeline, precise access-phase entry/exit, buffering and upstream connection reuse trace |
| M3 | Bounded UDP receiver and queue, batched SQLite WAL, age/count retention, health counters | Idle-time expiry, disk quota enforcement, durable loss accounting, optional PostgreSQL |
| M4 | Live polling terminal summary, web recent requests and detail view | Time-range filtering, pagination, route templating and request-rate calculation |
| M5 | Bodies/queries excluded; bounded payloads; loopback-only interfaces; schema validation | Opt-in body sampling design, sanitizers/fuzzing, long-stream/TLS/HTTP2 matrix, repeated controlled load budget, multi-host auth |

## Automated checks

1. Unit tests: malformed/oversized JSON, unknown fields and sensitive headers,
   invalid numbers/timestamps, duplicate ingestion, parameterized request lookup,
   count/age retention, exact percentile calculation and error counts.
2. Integration: known POST body and secret headers reach the backend but do not
   appear in captured events; response size matches; upstream is identified;
   50 ms backend delay is reflected; 503 and connection-refused 502 appear;
   internal redirect survives context clearing; disabled routes are absent;
   two keepalive requests have distinct IDs/counters.
3. Collector absence: stop only the collector, send requests, confirm successful
   proxy responses and bounded worker memory. Restart and confirm new arrivals.
4. Reload: use a canary with the deployment binary and module; run `nginx -t`.
   Confirm no undefined-symbol/signature errors and no worker crash. Validate
   fresh rows and public routes after the final reload.
5. On-demand: default-off, manual start/stop, timer expiry and resume. Compare the
   native `worker_sent` counters around stopped periods to prove the worker stops
   emitting, rather than only dropping events in the collector. Unit tests cover
   heartbeat renewal, duration validation, restart disabling, stable control inode
   and rejected cross-origin/rebinding control requests.

## Load experiment

Run the same binary with the module loaded for both controls. Change only
`inspector off` versus `inspector on`, reload, warm up, and alternate three or more
pairs. Generate at least 20,000 requests per run at fixed concurrency. Record RPS,
p50/p95/p99, errors, nginx CPU/RSS, receiver/queue/storage drops, generated requests
and database deltas. Test both small local responses and representative upstream
payloads, SSE, gzip and TLS. Never load-test an existing production route.

Initial target: <5% median throughput loss and <1 ms p95 latency increase, with no
unexplained loss at the representative target rate. A result below target in one
loopback/shared-VM run is not proof of production overhead. Preserve measured
results and limitations; do not fill in unmeasured CPU/RSS or transport-loss fields.

For hardening, add compiler sanitizers, malformed request fuzzing, client aborts,
upstream retries, HTTP/2 multiplexing, TLS, temp-file buffers, early rewrite exits,
collector disk-full conditions, WAL recovery and simultaneous reload/shutdown.
