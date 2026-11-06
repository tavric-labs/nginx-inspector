# Native observations

## Request execution

The hook sequence includes post-read, server/location rewrite boundaries,
preaccess, access, precontent, header-filter entry/return, response-body filters,
request-body filters and log. Each record carries a request-relative offset.
Rewrite observations include path/location; child observations reference a bounded
subrequest index.

These hooks are boundaries, not instrumentation of every module handler. A module
that finalizes early can skip later hooks. Repeated visits can indicate internal
redirect work; the original queryless path and final normalized path provide context.

## Upstreams and policy

Individual nginx upstream states expose peer, status, connect/header/response time
and byte counters. Retry triggers, limits and timeout values describe configured
policy. Buffering reports both configured response buffering and effective upstream
buffering, including normalized `X-Accel-Buffering` handling.

An upstream status is not an exact error cause. Read readiness, errors and timeout
flags only establish the values observed at the hook.

## Filters, buffers and body handling

Response observations include filter results, memory/file buffers, flush/sync
markers, buffered masks, upstream temporary-file offset, range/conditional flags,
content encoding and gzip ratio when available. Request-body observations count
buffers and bytes and report temporary-file use, configured limits and streaming.

The filter observes buffer metadata without copying payload. Downstream filters
can transform data; pre-transform byte counts differ from wire size.

## Worker, event loop and pool

Snapshots include nginx version, worker/master PID, event backend, worker connection
capacity, accept state, stub-status counters when compiled in, and a bounded scan
of request pool blocks. Large allocation sizes and process RSS are unknown.

Readiness bits and timer-armed bits explain the state visible at an observation.
They do not reconstruct every epoll wakeup or timer transition.
