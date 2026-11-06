# Coverage and limits

| Evidence | Bound / interpretation |
| --- | --- |
| Serialized event | At most 16 KiB; oversized serialization is dropped |
| Hook trace | 40 observations; omitted count included |
| Upstream attempts | 8 emitted; at most 64 states scanned |
| Subrequests | 8 tracked; omitted observations counted |
| Scalar strings | 256 bytes at producer |
| Trace paths | 128 bytes at producer |
| Pool scan | At most 32 blocks/large allocation links |
| Collector queue | 2,000 validated events; full queue drops metadata |
| Live worker snapshot | Once per second; up to 8 sampled live requests; 8 KiB packet |
| OS process sampling | Every 2 seconds; max 64 nginx processes and 8,192 directory entries |
| Recent requests | Latest 100 events |
| Percentiles | Latest 10,000 retained events |
| Default retention | 24 hours and 100,000 events, pruned on insert |

Producer caps and collector validation are independent. Payload truncation is not
stored as malformed JSON. Worker send/drop counters and collector health counters
show different loss boundaries; UDP has no delivery guarantee.

Not captured: raw request/response bodies, query values, sensitive headers, raw
error/debug logs, whole nginx configs, cache keys, temporary filenames, every
rewrite candidate, DNS/TLS substage timing, syscalls, scheduling and CPU profiling.

Completed history events are emitted at the main request log hook. Bounded worker
snapshots provide a separate sampled view of unfinished requests while capture is active. Short hooks and bounded allocations reduce
overhead, but observation still has a cost. Measure under your workload before
using full sampling for long sessions.
