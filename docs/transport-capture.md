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

Stop with SIGINT/SIGTERM. Sessions allow whole-second durations from 1 to 600
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
