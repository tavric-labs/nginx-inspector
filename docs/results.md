# Prototype verification — 2026-11-06

## On-demand capture update

- **15 unit tests passed**, including manual/timed controls, restart default-off,
  stable mapping inode, heartbeat renewal, duration validation and control-API
  cross-origin/DNS-rebinding rejection.
- Native proxy/error/redirect/keepalive/exclusion integration passed locally and
  against the deployment binary in an isolated canary.
- Manual stop and timer expiry were checked using `worker_sent`: it did not
  increase across 20 requests while stopped or 20 after expiry. Capturing resumed
  without reloading nginx, and off-period paths did not appear in history.
- The host canary also passed collector absence and recovery checks.

The load measurements below apply to the initial continuous-capture prototype.
The later on-demand heartbeat gate is verified separately; its additional active
capture overhead has not been benchmarked.

Built against Ubuntu nginx **1.24.0-2ubuntu7.19** development headers in a
disposable Ubuntu 24.04 container. No packages were installed on either host.
The deployment host's own nginx binary accepted the module in a private canary.

- Collector unit tests: **7 passed**.
- Black-box integration: known proxy request/response, 503, connection-refused
  502, delay timing, internal redirect, location opt-out and keepalive IDs passed.
- Secret exclusions: fake authorization, cookies, API-key, query and body values
  were absent from captured JSON, including the backend's Set-Cookie value.
- Host canary outage check: **20 requests succeeded** with the collector stopped;
  event delivery resumed after restart. Canary processes were then stopped.

## Shared-VM load experiment

One nginx worker, loopback `/local` 204 responses, Python traffic generator,
8 concurrent keepalive clients, 1,000 warm-up and 20,000 measured requests per run.
Module loaded for all runs; enabled state alternated off/on/on/off/off/on.

| Run | Inspector | RPS | p95 ms | HTTP errors |
| --- | --- | ---: | ---: | ---: |
| 1 | off | 2317.50 | 8.270 | 0 |
| 2 | on | 2989.47 | 8.639 | 0 |
| 3 | on | 2853.04 | 9.044 | 0 |
| 4 | off | 2805.14 | 8.947 | 0 |
| 5 | off | 2767.57 | 9.414 | 0 |
| 6 | on | 2701.54 | 9.360 | 0 |

Median p95 increase: **0.097 ms**. Median throughput showed a 3.09% increase with
inspection enabled, which is evidence of noise, not an acceleration claim. This
experiment meets the initial numerical target but does not establish production
overhead. CPU and RSS were not measured. Payload, TLS, HTTP/2, gzip and streaming
load tests remain to be run.

The three enabled runs plus warm-ups expected **63,000** events. Receiver and
persistence counts increased by exactly **63,000**; no invalid-event, queue-drop
or storage-failure counters appeared. Existing integration events brought the
total to 63,014. These observations account for this controlled run only.
