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
