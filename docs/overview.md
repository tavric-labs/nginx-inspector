# System overview

nginx-inspector records a bounded account of a completed request from inside nginx.
Use it to understand a rewrite, a retry, an unexpected buffer, an auth subrequest,
a cache result, or the path between request parsing and output filters.

| Component | Responsibility |
| --- | --- |
| C dynamic module | Observe nginx hooks; publish a bounded nonblocking UDP event |
| Go collector | Validate events, maintain capture lease, persist SQLite history |
| Embedded desktop viewer | Explain native state and display request evidence |
| CLI | Start/stop/timed capture, status and summary |

Capture starts OFF. Starting a session does not reload nginx. Stopping a session
does not delete history. Module installation is the only step that needs an nginx
configuration change and reload.

Continue with [installation](/install), then [first capture](/quick-start).
