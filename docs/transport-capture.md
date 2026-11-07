# Correlated transport capture

Open **Replay** and select a request. Its root view is the full client-to-worker
journey. Click **nginx HTTP processing** for the existing seven-stage replay
and phase details in a modal. Replay is the single entry point. The diagram separates network receive, TCP handshake tracking, the accept
queue, socket buffers, epoll interest/readiness, and the nginx worker event loop.
Only captured event points move the playback marker. Dashed components describe
mechanisms for which the record has no event.

## What becomes measurable

An optional Linux amd64 tracing command uses bpftrace and the native module's
metadata-only `ngx_inspector_request_begin` / `ngx_inspector_request_end` markers.
Those carry exactly the same request ID as the HTTP event, plus its socket FD.
Kernel accept, successful epoll registration, readiness-return and read-syscall
observations are joined by **trace session + host PID + FD + FD generation**.
Numeric FD equality or a matching client address alone is never sufficient.

| Observation | Meaning and boundary |
| --- | --- |
| SYN tracking hook → accept-queue hook | Connection wall time between `inet_csk_reqsk_queue_hash_add` and `inet_csk_reqsk_queue_add`, when both were captured; not NIC arrival or pure CPU |
| Queue hook → accept/accept4 return | Admission interval including kernel transition work; not an exact queue-residence or CPU measurement |
| Epoll registration | Successful add/modify subscription, not a queued HTTP request |
| Epoll ready return | A registered socket event returned to the worker; not the earlier instant it became ready |
| Read syscall | Returned byte count and syscall wall duration; no user buffer or packet data |
| Native request markers | Selected context creation and log observation; begin is generally after headers were parsed |

When the queue-add function is inlined, a captured `inet_csk_complete_hashdance`
entry provides a separately labelled TCP-handoff boundary. This includes work
before enqueue and is never presented as exact queue residence.

The interval between SYN and accept hooks depends on TCP cookies, Fast Open and
kernel paths. Missing hooks remain unknown. The initial implementation requires
the listed kernel symbols and syscall tracepoints; unsupported environments fail
instead of substituting an aggregate estimate.

## Scope and concurrency

Connection setup belongs to the connection. Keepalive requests and HTTP/2 streams
share it. All connection events are labelled shared; a second request marker stays
visibly attributed to that other request. This tool does not charge all reads on
a shared socket to the selected request, nor infer per-request kernel CPU.

Kernel receive-stack CPU, NIC timing, per-byte socket-buffer residence and exact
readiness-to-scheduling latency are **not** measured by these probes. Ordinary
HTTP phase offsets are not used to fill these gaps. Monotonic nanoseconds travel
as decimal strings; the browser subtracts them before rounding for display.

## On-demand operation

Kernel observation needs the host owner's separately approved privileges. Keep
the collector unprivileged. Install a tested marker-enabled native module through
the normal backup, nginx validation and graceful-reload procedure first.

With an approved capture active, run a bounded trace locally on the host:

```sh
nginx-inspector trace --master-pid 1234 \
  --module /usr/lib/nginx/modules/ngx_http_inspector_module.so \
  --for 60s --udp-port 19876 > ./private/transport.ndjson
```

The example PID is synthetic: verify the actual nginx master before running.
The command verifies worker roles/start times and listener ownership, restricts
syscall and user probes to those worker PIDs, and stops if their generation changes.
Listener inode filters scope TCP queue probes. No socket endpoints, raw kernel
pointers, memory dumps or payloads are emitted. Dependencies are bpftrace and ss
and readable kernel BTF; the tracer never installs packages.

Stop with SIGINT/SIGTERM. Sessions allow whole-second durations from 1 to 7,200
seconds and stop at 64 MiB of emitted records. Map capacity is 4,096 keys per map;
at most 32 returned descriptors are examined in an epoll call. These bounds and
kernel/output drops can leave gaps. Probe overhead is real and workload dependent;
no guarantee of unchanged latency is implied.

## Retention and failure behavior

The existing loopback UDP receiver accepts a closed transport schema only while
capture is active. A 2,000-record queue decouples it from SQLite. Ingestion,
validation, queue and storage drops appear in `/health`. Sender output reports
UDP failures; BPF output/map loss can impose additional limits. UDP is best effort.

Transport records share configured history hours, with a separate cap of 32 times
the HTTP event limit, up to 320,000 records. `/api/transport/<request-id>` returns
the explicit marker correlation plus up to the first 256 retained connection
records. Truncation is shown; the selected marker remains available separately.
Ambiguous session/generation matches return no guessed history. Late attachment,
retention or collector restarts may remove evidence; unknown never means zero.

Normal CI compiles the module and tracer without loading kernel probes. The real
privileged canary is opt-in through workflow_dispatch with kernel_trace=true; a
skipped runtime test is not evidence that the target kernel supports the probes.

## Playback and missing timings

Replay advances through recorded connection events at 2.6 seconds per event by
default. Choose 0.5 or 5 seconds, pause, step, or scrub. The blue arrow links
consecutive observations in timestamp order; playback pace is illustrative and
is not the measured interval. The Explore button follows the selected component and opens its captured operations and mechanism details. Selecting nginx HTTP processing opens its seven-stage HTTP replay.

A request marker can join a socket even when capture started after acceptance.
Such a request may have no SYN, handoff, subscription, or readiness history.
Each missing interval states which boundary was not retained. Prefer a request
whose captured history includes accept, registration, readiness, and reads to
follow connection setup. Existing connections share that history across requests.

Kernel receive-stack CPU attribution and per-byte socket-buffer residence are
labelled **Not instrumented**, since the current probes cannot measure them.
They are not replaced by worker CPU averages or HTTP elapsed time.

## One command, one recording window

For correlated kernel and HTTP recording, run the native CLI with the required
host privileges, verified nginx master PID and marker module:

```sh
nginx-inspector start --transport --for 2h --sample 100 \
  --master-pid 1234 --module /usr/lib/nginx/modules/ngx_http_inspector_module.so
```

This command stays running. It starts HTTP sampling, arms BPF and reports
`TRACE_READY`. Both stop at the requested deadline, on SIGINT/SIGTERM, or when
`nginx-inspector stop` turns capture off. A probe or collector failure stops both
instead of silently leaving HTTP-only capture. It never reloads nginx. The
64 MiB trace-output bound and target-generation checks still apply and can end
the session early. Transport starts allow whole-second durations up to two hours.

`nginx-inspector status` reports HTTP and transport states. Transport status uses
a 15-second lease, renewed by the live helper; a dead helper cannot leave the UI
showing indefinite active tracing. The status endpoint is read-only on the private
viewer; updates retain the loopback-only control restrictions.

Plain `start` retains the existing HTTP-only mode. It does not silently acquire
host privileges. Replay defaults to **Transport recorded**, which finds up to 100
retained journeys even after newer HTTP-only requests push them off the recent
request list. Choose **All recent HTTP requests** to inspect HTTP-only history.

## Kernel and socket evidence in replay

The TCP probes add connection evidence to the full journey. They never inspect
payload buffers, packet contents, IP addresses or TLS plaintext. Kernel socket
pointers are used only as private map keys and are not emitted.

| Observation | What it helps explain | Scope and limit |
| --- | --- | --- |
| `tcp_rcv_established` entry/return | When the established TCP receive handler ran, elapsed time and CPU number | Shared socket; elapsed time includes nested work and is not exclusive CPU time |
| `tcp_recvmsg` / `tcp_sendmsg` return | Socket accounting after kernel receive/send operations | Point-in-time samples, not allocated memory owned by one HTTP request |
| Read errors, including `EAGAIN` | Why a worker can stop reading and wait for readiness | Nonblocking result; not an HTTP failure classification |
| `write`, `writev`, `sendto`, `sendmsg`, `sendfile` returns | Bytes accepted by the kernel, syscall elapsed time and backpressure | Connection bytes, potentially encrypted; accepted does not mean delivered |
| TCP state transition hook | Establishment/shutdown state changes while the identified socket is tracked | A socket's history can continue after its original FD is closed |
| TCP retransmission hook | A retransmission tracepoint ran for the selected socket | Does not attribute delay or packet loss to a particular request |

Socket snapshots include receive-memory accounting, queued send memory, unread
TCP sequence distance, smoothed RTT, congestion window in segments, outstanding
segments, lifetime retransmission counter and TCP send/receive window fields.
Memory accounting includes overhead. Sequence distance is not memory allocation
and can include a FIN sequence unit. A zero RTT field can mean no estimate or
rounding, rather than a zero-latency network.

The receive callback can execute in softirq or other task context. Its record's
worker PID identifies the original socket owner, while its CPU field identifies
where the callback returned. Those are different facts. FD generation and trace
session still separate reused descriptors; the socket mapping is removed at
kernel free. Callback nesting and maps are bounded. Missing probes, late attach,
map/output limits, truncation and retention can still leave partial history.

Kernel receive-stack CPU attribution and per-byte buffer residence remain outside
these measurements. Replay shows callback elapsed time and socket state as useful
additional observations, without replacing those unknowns.

References: [nginx event/development guide](https://nginx.org/en/docs/dev/development_guide.html),
[Linux receive semantics](https://www.man7.org/linux/man-pages/man2/recvmsg.2.html),
[Linux send semantics](https://www.man7.org/linux/man-pages/man2/sendto.2.html),
[Linux TCP fields](https://github.com/torvalds/linux/blob/v6.8/include/linux/tcp.h),
[socket accounting](https://github.com/torvalds/linux/blob/v6.8/include/net/sock.h),
[TCP tracepoints](https://github.com/torvalds/linux/blob/v6.8/include/trace/events/tcp.h)
and [socket-state tracepoint](https://github.com/torvalds/linux/blob/v6.8/include/trace/events/sock.h).
