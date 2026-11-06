# The nginx engine

This chapter connects nginx mechanisms to the evidence available in the inspector.
It separates a mechanism's behavior from what a particular capture proves.

## 01 / Processes and graceful generations

The master manages configuration and workers. Workers handle many connections
through an event-driven design. Reload introduces a new worker generation while
old workers drain existing work. A request's worker PID identifies the process
that observed it; a mixed set of PIDs after reload can be expected during draining.
The inspector does not enumerate every master/helper process or establish that all
old workers have exited. [NGINX architecture article](https://blog.nginx.org/blog/inside-nginx-how-we-designed-for-performance-scale)

## 02 / Readiness, timers and suspended work

A request can yield and resume as events arrive. Read/write readiness, timer flags
and filter return codes describe different things. `NGX_AGAIN` means work cannot
complete immediately; it does not identify a particular broken backend. Inspect
the hook's I/O flags and later observations. This tool samples those boundaries;
it does not record every loop iteration. [Developer reference](https://nginx.org/en/docs/dev/development_guide.html)

## 03 / Virtual server and location selection

Listen address/port, server-name matching and location rules determine which
configuration applies. A default server belongs to a listening endpoint. Internal
URI changes can trigger more location processing. Compare original path, normalized
path, rewrite observations and final location. The final location does not expose
the full list of candidates nginx tested. [Request processing](https://nginx.org/en/docs/http/request_processing.html)

## 04 / Upstream selection and connection reuse

An upstream group can select peers using different balancing methods. Idle upstream
keepalive connections form a reuse cache; its size is not a universal cap on active
connections. The inspector records chosen peers and whether a cached peer was
observed. It does not currently expose every candidate's weight, failure score,
shared-zone state or the selection algorithm's intermediate arithmetic.
[Upstream reference](https://nginx.org/en/docs/http/ngx_http_upstream_module.html)

## 05 / Buffering and backpressure

Response buffering can let nginx read ahead of a slow client; data exceeding memory
buffers can use temporary storage under policy limits. With buffering off, data is
forwarded as it is received. `X-Accel-Buffering` can change the behavior unless
configured to be ignored. Compare configured/effective policy, file offsets, filter
returns and readiness flags. A gap between two hooks is not a measured backpressure
duration. [Proxy reference](https://nginx.org/en/docs/http/ngx_http_proxy_module.html#proxy_buffering)

## 06 / Cache versus upstream work

Caching introduces request outcomes such as HIT, MISS, BYPASS and stale/revalidation
paths. A HIT is a cache outcome, not a claim that no other internal work occurred.
Read attempts and subrequests alongside it. The inspector records the outcome,
cache-file use and configured cache/lock/revalidation policy; it does not retain
cache keys, object contents or eviction history.
[NGINX caching article](https://blog.nginx.org/blog/nginx-caching-guide)

## 07 / Request-rate limiting

The request limiter uses shared-zone accounting and supports delays, rejections and
dry-run operation. A 503 alone cannot distinguish rate limiting from an upstream
503. The native `rate_limit_status` observation can directly report the limiter's
outcome when its module supplies that variable. Missing status remains unobserved;
the inspector does not expose the zone's raw key or every bucket update.
[Request limiter reference](https://nginx.org/en/docs/http/ngx_http_limit_req_module.html)

## 08 / Connection limiting and multiplexing

Connection limiting has accounting semantics beyond simply counting open TCP
sockets; HTTP/2 and HTTP/3 concurrent requests are counted separately for this
module. Use `connection_limit_status` for its outcome. The collector's connection
counter and protocol label do not reconstruct stream flow-control windows or
all frames on a multiplexed connection.
[Connection limiter reference](https://nginx.org/en/docs/http/ngx_http_limit_conn_module.html)

## 09 / Authentication subrequests

`auth_request` authorizes a main request using a subrequest result. A 2xx result
allows access; 401/403 denies it; other results are errors. The inspector links
bounded child paths/statuses to the main request's trace. A child response is not
a second external client request. Authentication content and credentials remain
excluded. [Auth-request reference](https://nginx.org/en/docs/http/ngx_http_auth_request_module.html)

## 10 / File output, sendfile and thread pools

File-backed buffers and configured AIO mode explain which output facilities are
available. nginx can offload eligible file I/O to thread pools when configured and
compiled to support it. A sendfile-permitted flag or `aio threads` policy is not
proof of a particular syscall or thread-pool job. Those require separate syscall
or job-level instrumentation. [Thread-pool engineering article](https://www.f5.com/company/blog/nginx/thread-pools-boost-performance-9x)

## 11 / Client I/O and connection cleanup

Client body limits, body buffering, TCP policies, output postponement and lingering
timeouts govern different stages. The inspector records these selected policy
values and body/file observations. A configured timeout is an upper policy value,
not elapsed wait time; lingering cleanup can continue beyond the final request
observation. [HTTP core reference](https://nginx.org/en/docs/http/ngx_http_core_module.html)

## 12 / Debug traces and protocol-specific engines

nginx debug logging requires build support and can be limited to selected clients
or a cyclic memory buffer. It can reveal lower-level detail, but raw debug output
is not ingested by this inspector. [Debug-log reference](https://nginx.org/en/docs/debugging_log.html)

HTTP/3 has a separate QUIC transport and module-specific controls. This HTTP hook
inspector is not a QUIC packet/frame analyzer. Version, compiled modules and the
target protocol determine which observations can exist.
[HTTP/3 reference](https://nginx.org/en/docs/http/ngx_http_v3_module.html)

## Where to look in the source

Use the source matching the running vendor build. These are navigation pointers,
not promises that the inspector hooks every function:

| Area | Source path | Captured boundary |
| --- | --- | --- |
| Event dispatch | `src/event/ngx_event.c` | Readiness/timer snapshots only |
| Worker lifecycle | `src/os/unix/ngx_process_cycle.c` | Worker/master PID snapshot |
| Request parsing | `src/http/ngx_http_request.c` | First observation after headers |
| Phase engine/location | `src/http/ngx_http_core_module.c` | Registered phase boundaries |
| Upstream state machine | `src/http/ngx_http_upstream.c` | States, attempts, effective flags |
| Request bodies | `src/http/ngx_http_request_body.c` | Body filter, temporary file offset |
| Output event pipe | `src/event/ngx_event_pipe.c` | Pipe/blocking flags at hooks |
| Gzip | `src/http/modules/ngx_http_gzip_filter_module.c` | Encoding and indexed ratio variable |
| Rate limiting | `src/http/modules/ngx_http_limit_req_module.c` | Native outcome variable |

[Official nginx source](https://github.com/nginx/nginx) · [Coverage contract](/coverage)
