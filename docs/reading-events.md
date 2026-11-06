# Reading the evidence

## A retry returns 200

The browser only sees the final 200. The attempts table might show a 502 followed
by a 200 against another peer. That establishes multiple upstream states with
those outcomes. Configured retry triggers explain which failure classes are
eligible; they do not independently prove which socket operation failed.

Connect, header and response timings overlap. Do not add them into a synthetic
request duration.

## Buffering changes during a request

If configured response buffering is ON, effective buffering is OFF, the normalized
header is `no` and the header is not ignored, the evidence shows the upstream
header changed buffering. A streaming configuration can also disable buffering
without such a header.

## An upload touches disk

A nonzero temporary-file offset and disk-buffered flag establish temporary-file
use. Compare observed bytes, declared length, buffer limit and maximum body size.
No body contents or temporary filename is retained.

## A local rejection

A 413 with declared length larger than a nonzero configured maximum is consistent
with the body-size limit. No upstream attempts supports a local rejection for
that request; missing attempts alone is not a universal proof of its cause.

## Unknown remains unknown

Null or absent values mean unavailable or unobserved. Do not interpret them as
false, zero, or a successful execution. Old schema-1 history cannot be upgraded
into native evidence after the fact.

## Stage timeline {#stage-timeline}

Inspect shows the seven request stages and when each was first reached, measured from the
start of the request. Stages are Parsed, Rewrite, Access, Content, Headers, Body and
Done; the hooks behind each are listed in [Workers](./workers.md#request-stages).

For every stage the timeline shows the offset at which it was first reached and the time
spent before the next observed stage. The longest stage is highlighted. A stage that is
not reached is dimmed: for example a client that closed the connection before the
backend answered never reaches Headers or Body.

This is where time went inside nginx for that request. A request that reaches Headers at
243,409 ms waited about four minutes for the backend's first byte; one that reaches Body
at 4 ms and Done at 4 ms was answered at once. The timeline is built from the observed
hook sequence, so it inherits its limits: at most 40 observations are kept and a missing
hook is unknown, not zero.
