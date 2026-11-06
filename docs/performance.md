# Traffic behavior and overhead

The inspector observes nginx state and chains existing filters. It does not change
request/response bodies or headers, force body reads, choose upstream peers, alter
cache/access decisions, or place storage in the request path.

That design preserves behavior; it does not imply zero overhead. Selected requests
allocate bounded observation context, visit hooks and serialize metadata. Worker
snapshots add a bounded timer callback while capture is active. CPU, memory and
latency depend on concurrency, sampling, response shape and the host workload.

## Failure and pressure behavior

- The capture gate starts OFF and expires after collector failure.
- Nonblocking UDP has no request-path retry or storage acknowledgement.
- Serialization, trace, attempts, child requests and live snapshots have caps.
- The collector queue is bounded; excess metadata is dropped.
- Worker OS sampling runs in the collector, outside nginx's event loop.
- No raw payload or debug-log ingestion is enabled.

## Verification

`tests/equivalence.py` compares capture OFF and ON for exact status, headers
(excluding Date), and body digest. Fixtures cover proxy output, local responses,
rewrite, upstream failure, buffering override, files, gzip and uploads.

Set `INSPECTOR_BENCHMARK=1` while running the isolated canary to alternate OFF/ON
rounds at concurrency eight for OFF, 1% sampling and full capture. It reports latency percentiles and throughput changes.
These measurements include fixture/client overhead and host noise; they are not a
production latency guarantee. Never run this load harness against a shared production
proxy. Reduce sampling for demanding or long capture sessions and measure the actual
workload before choosing an operating window.

## Isolated fixture measurement

On 2026-11-06, the fixture ran three rounds per mode, 2,048 requests per round,
concurrency eight, with a single nginx worker and TCP_NODELAY on the test backend.
These are client-observed measurements of the entire local fixture, including its
Python client/backend and shared-host noise.

| Sampling | Median p95 increase versus OFF | Median throughput change |
| --- | ---: | ---: |
| 1% | +0.67 ms | -6.95% |
| 100% | +3.88 ms | -35.40% |

Response-equivalence checks passed for every tested mode. These figures explain
why new sessions default to 1% and full sampling is an explicit choice. They do
not establish a production SLO or prove the same overhead on another workload.
Old-history pruning controls storage; it does not remove instrumentation CPU cost.
