# Proxy, cache, access and bandwidth

| nginx responsibility | Evidence in the inspector | What is not established |
| --- | --- | --- |
| Reverse proxy | Upstream group, peers, attempts, timing, byte counters and buffering | Exact backend application work |
| Load balancing | Chosen peers, retries and observed cached-connection reuse | Every candidate's weight/score or complete selection arithmetic |
| Caching | Outcome, cache-file use, configured lock/revalidation | Cache keys, contents, eviction history or every bypass expression |
| Access control | Access-phase hooks, auth subrequests and `satisfy` policy | Every allow/deny rule evaluated by every module |
| Request-rate limiting | Native limiter outcome, including rejection/delay/dry-run when supplied | Raw accounting key or every bucket update |
| Connection limiting | Native connection-limiter outcome | Every shared-zone counter transition |
| Bandwidth control | Effective `limit_rate`, `limit_rate_after`, delayed-write snapshots and observed byte counters | A continuous throughput series or proof every delayed write was caused by throttling |

## Effective bandwidth policy

The inspector reads the request's resolved byte-rate limit and unthrottled prefix
after nginx has evaluated them. An unset value is unknown; zero can mean no rate
limit when resolved. A configured limit differs from measured network throughput.
Buffer metadata bytes also differ from wire bytes after compression and framing.

Use the live worker view to see delayed writes and the completed record to inspect
output buffers and effective policy. Policy values are never changed by the inspector.

## Investigating a result

Begin with the direct outcome: limiter status, cache outcome, auth child status,
or upstream attempt. Then compare the configured policy and hook sequence. Keep
inferences separate: a 503 alone is not proof of rate limiting; a final 200 can
hide an earlier failed upstream attempt.

See [Reading the evidence](/reading-events), [Workers](/workers), and the
[primary references](/research).
