# Workers, memory and concurrency

The Workers view combines two independent sources. Read-only Linux process samples
describe resource use. On-demand native worker snapshots describe nginx work.

## Process footprint

Configure the collector with an explicitly selected proc filesystem and nginx PID file:

```sh
nginx-inspector serve --db ./data/events.sqlite3 --control-file ./control/control \
  --proc-root /proc --nginx-pid-file /run/nginx.pid
nginx-inspector workers --once
```

The PID file scopes discovery to that master and its direct children. The view
includes master, workers, draining workers and cache helpers when present. It reports
state, threads, CPU delta, RSS, anonymous memory, file-backed memory, shared memory
and virtual size. Missing/denied values stay unavailable.

RSS is an approximate resident-memory measure. Summing it across workers can count
the same shared pages more than once. Virtual size is address space, not physical
consumption. These numbers differ from the per-request pool estimates. The sampler
does not read process environments, dump memory or publish command-line arguments.
[Linux status fields](https://man7.org/linux/man-pages/man5/proc_pid_status.5.html)

CPU percentage uses user/system tick deltas between two samples, normalized to one
CPU core. The first sample has no delta. PID start time distinguishes reused PIDs.
Samples are taken every two seconds, outside nginx workers; they do not attribute
CPU time to individual requests.
[Linux stat fields](https://man7.org/linux/man-pages/man5/proc_pid_stat.5.html)

## Three different concurrency measures

| Measure | Meaning | Boundary |
| --- | --- | --- |
| Active/reading/writing/waiting clients | nginx instance-wide shared counters | Requires stub-status build support; not summed across worker copies |
| Occupied worker connection slots | Allocated slots in that worker's connection array | Includes clients, upstream, listening and internal connections |
| Sampled in-flight requests | Eligible selected request contexts still alive | Includes only requests observed while capture was active |

Connection count, slot occupancy and request concurrency are not interchangeable.
Multiplexing and upstream connections make their relationship workload-dependent.

## Current request snapshots

While capture is active, each worker sends a bounded snapshot once per second.
It lists up to eight sampled request IDs, methods, queryless paths, elapsed time,
last observed hook, status, response-buffer bytes and I/O flags. It also reports
how many live request rows were omitted.

Each row answers two different questions, in two columns. The next two sections explain them.

This is not every transition. A short request can finish between snapshots; use its
completed event for retained history. A snapshot expires after four seconds and
live request rows disappear immediately from the API when capture stops. The
collector keeps these worker snapshots in bounded memory, outside SQLite history.

## Where it is: the seven stages {#request-stages}

The **Where it is** column shows the last hook the module saw this request reach, drawn
as a ladder of seven stages. It is a position in the request's life. It is not a live
stack trace and not a promise about what nginx is executing at this instant.

| Stage | Hooks that count as reaching it | What nginx is doing |
|---|---|---|
| 1 Parsed | post-read | The request line and headers have been read |
| 2 Rewrite | server rewrite, location rewrite | Choosing the location and applying rewrites |
| 3 Access | preaccess, access | Access modules, rate limits, `auth_request` |
| 4 Content | precontent, request-body buffers | Reading an upload, running the content handler (often the proxy) |
| 5 Headers | header filter | The response headers are being sent |
| 6 Body | first, middle and last response buffer | Response data is flowing |
| 7 Done | log | The request finished and is being logged |

**Why a request can sit at "stage 6 of 7: Body" for a long time.** MCP servers answer with
an event stream: the headers go out at once, then data arrives in pieces over minutes or
an hour. Such a request reaches Body quickly and stays there until the stream ends, so it
never shows Done while it is open. A long time at Body with low CPU is normal, not stuck.

**Why you rarely see stages 1 to 5 live.** For an ordinary request they pass in about a
millisecond in total, and the live table only reports the latest stage once per second. A
person cannot follow that by eye, and neither can a one-second snapshot. The full
sequence is recorded for every completed request instead: open it with **Inspect** to see
the stage timeline and the hook sequence (see
[Stage timeline](./reading-events.md#stage-timeline)).

**Why it matters.** A request waiting at Headers has not received a first byte from the
backend. A request at Body is being served. Together with the socket state below, this
tells slow backends from slow clients without guessing.

## What nginx's sockets are doing: client, upstream and bits {#socket-state}

The **What nginx's sockets are doing now** column decodes flags nginx keeps on two
connections at the instant of the snapshot:

- **Client** is the connection between the browser or MCP client and nginx.
- **Upstream** is the connection between nginx and the backend it forwards to. In our
  deployment that is the MCP gateway or the Slashdocs service. A request answered by
  nginx itself has no upstream, so the upstream row stays empty.
- **Bits** is the raw number the flags are packed into, shown for exact comparison.
  Each bit is one flag:

| Bit value | Flag | Meaning |
|---|---|---|
| 1 | client can read | nginx may read from the client without waiting |
| 2 | client can write | nginx may write to the client without waiting |
| 4 | client write delayed | nginx is deliberately holding output back (for example a bandwidth limit) |
| 8 | client write timer armed | a send timeout is scheduled |
| 16 | client read timer armed | a read timeout is scheduled |
| 32 | client connection error | the client connection reported an error |
| 64 | upstream connected | an upstream connection is attached |
| 128 | upstream can read | the backend has data waiting |
| 256 | upstream can write | nginx may write to the backend |
| 512 | upstream read timer armed | nginx is waiting for the backend, with a timeout scheduled |
| 1024 | upstream write timer armed | a send timeout to the backend is scheduled |
| 2048 | upstream EOF | the backend closed its side |
| 4096 | upstream error | the upstream connection reported an error |
| 8192 | pipe blocked | upstream data is held back by a slow client |
| 32768, 65536 | client read or write timed out | the timeout actually fired |
| 131072, 262144 | upstream read or write timed out | the timeout actually fired |

For example, **834** is 2 + 64 + 256 + 512: the client can be written to, the upstream
is connected and writable, and nginx is waiting for the backend to send, with a read
timer armed. That is a healthy idle stream.

The one-line reading above the chips is only a plain-language summary of these flags at
one instant. It is not a diagnosis. An armed timer has not expired, and "can write" does
not mean bytes are moving. Only a timed-out flag means a timeout fired.

**Why it matters.** These flags separate the usual causes of a slow request: waiting on
the backend (upstream read timer armed, nothing to read), a slow client (client write
delayed, or pipe blocked), and a timeout that fired. Without them every slow request
looks the same.

## Containers

For host nginx, mount the host proc filesystem and the host nginx PID file read-only,
then pass their container paths to `--proc-root` and `--nginx-pid-file`. Host PID
namespace, root UID and extra capabilities are not required. Kernel proc access
policy can still make some fields unavailable.

Process sampling is optional and bounded to 8,192 directory entries and 64 nginx
processes per scan. Configure the PID scope to avoid combining separate nginx
instances on the same host.
