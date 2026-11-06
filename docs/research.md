# Research and source map

Selected primary references guide the implementation and explanations. Older
engineering articles explain design intent; the matching vendor source and current
directive reference decide version-specific behavior.

| Reference | Use in the inspector |
| --- | --- |
| [NGINX developer guide](https://nginx.org/en/docs/dev/development_guide.html) | Hook registration, requests, pools, filters and event model |
| [Inside NGINX: performance and scale](https://blog.nginx.org/blog/inside-nginx-how-we-designed-for-performance-scale) | Master/worker architecture and graceful reload |
| [How nginx processes a request](https://nginx.org/en/docs/http/request_processing.html) | Server and location interpretation |
| [HTTP core](https://nginx.org/en/docs/http/ngx_http_core_module.html) | Body, output, access and timeout policy |
| [Proxy module](https://nginx.org/en/docs/http/ngx_http_proxy_module.html) | Buffering, retries and cache behavior |
| [Upstream module](https://nginx.org/en/docs/http/ngx_http_upstream_module.html) | Peer selection, reuse and timing variables |
| [Caching guide](https://blog.nginx.org/blog/nginx-caching-guide) | Cache outcomes and lock/revalidation concepts |
| [Request limiting](https://nginx.org/en/docs/http/ngx_http_limit_req_module.html) | Direct rate-limit outcome variable |
| [Connection limiting](https://nginx.org/en/docs/http/ngx_http_limit_conn_module.html) | Outcome and multiplexed-request accounting |
| [Auth subrequests](https://nginx.org/en/docs/http/ngx_http_auth_request_module.html) | Child request interpretation |
| [Thread pools](https://www.f5.com/company/blog/nginx/thread-pools-boost-performance-9x) | File-I/O offload versus policy-only observations |
| [HTTP/2](https://nginx.org/en/docs/http/ngx_http_v2_module.html) | Protocol-specific limits and coverage boundaries |
| [HTTP/3](https://nginx.org/en/docs/http/ngx_http_v3_module.html) | QUIC coverage boundary |
| [Debug logging](https://nginx.org/en/docs/debugging_log.html) | Optional deeper investigation outside this collector |
| [Official source](https://github.com/nginx/nginx) | Verify concrete structure and state-machine behavior |

## Coverage standard for new insights

Each new claim needs a producer observation, a bounded schema field, validation,
a real nginx fixture, a UI explanation and a documented limit. A plausible story
based only on elapsed time or an HTTP status is insufficient.

Read [The nginx engine](/nginx-engine) to connect these references to captured
evidence. See [coverage](/coverage) for explicit unobserved areas.
