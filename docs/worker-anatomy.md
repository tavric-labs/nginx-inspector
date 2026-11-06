# Worker anatomy: CPU, memory, sockets and epoll

The Workers view separates **live process samples**, **native request observations**
and **dated operator diagnostics**. Each answers a different question. A dated
snapshot never supplies a missing live counter.

## Start with the question

The overview connects evidence to connection headroom, CPU work, request state
and memory interpretation. It reports sample coverage and missing data without
claiming a bottleneck or leak from one observation. Open the detailed evidence
when investigating a specific question.

## Master and workers

The master manages configuration and worker generations; workers process request
events. Several workers can share a listening socket. A worker's connection array
can contain client, upstream, listening and internal connections. An OS descriptor
inventory cannot by itself reproduce that array or identify idle HTTP requests.
HTTP/2 multiplexing also prevents treating socket counts as request counts.
See the [nginx development guide](https://nginx.org/en/docs/dev/development_guide.html#processes).

The anatomy view explains the cycle: wait for readiness or a timer deadline,
dispatch handlers, advance HTTP/proxy/filter work, handle timers and posted work,
then repeat. Its mechanism table is educational; it does not animate imagined
kernel events. Request Replay remains a replay of observed HTTP hooks.

## Live CPU and scheduler readings

The existing two-second sampler reads one additional bounded file per discovered
nginx process: `schedstat`, at most 256 bytes. It also uses fields in the existing
`stat` and `status` reads. Sampling continues with capture OFF and runs outside
nginx. No ptrace, process-memory reads or capabilities are added.

| Field | Source and interpretation |
| --- | --- |
| User / system CPU percentage | `stat` tick deltas divided by clock ticks and elapsed time; 100% means one core |
| Lifetime user / system CPU | This process's ticks since start; excludes reaped-child CPU |
| Voluntary / involuntary context switches per second | Main thread's `status` counter deltas; several causes, including blocking and preemption |
| Scheduler dispatches per second | Main thread's third `schedstat` counter delta divided by interval |
| Run-queue wait in ms per second | Main thread's runnable-but-unscheduled nanoseconds delta, converted to ms/interval second |
| CPU in µs per scheduler slice | Main thread's runtime delta / dispatch delta / 1,000; unavailable when no slices occurred |
| VmData | Virtual data address space; physical allocation is a different measure |
| Minor / major faults | Process-lifetime fault totals; major faults require I/O |

Scheduler data describes the **main thread**. Process CPU may include other
threads when nginx has a thread pool. Neither scheduler dispatches nor context
switches are event-loop wakeups. CPU per scheduler slice is not CPU per epoll
return or per request. Run-queue delay excludes time blocked on network I/O.
See [Linux scheduler statistics](https://docs.kernel.org/scheduler/sched-stats.html),
[stat](https://man7.org/linux/man-pages/man5/proc_pid_stat.5.html) and
[status](https://man7.org/linux/man-pages/man5/proc_pid_status.5.html).

The first sample, missing fields, counter regressions and PID reuse do not yield
fabricated rates. Start ticks identify a process generation. A zero schedstat
triple is treated as unavailable because scheduler statistics may be disabled.

## Dated protected diagnostics

An operator can collect protected measurements separately and pass a sanitized
JSON snapshot into the collector. This is optional; the collector never starts
root helpers or tracing tools. It reads the file **once at startup**:

```sh
nginx-inspector serve --db ./data/events.sqlite3 --control-file ./control \
  --proc-root /proc --nginx-pid-file /run/nginx.pid \
  --worker-diagnostics ./private/worker-diagnostics.json
```

For a container, mount that one file read-only and pass its container path. Keep
the collector's existing unprivileged UID, capability restrictions and read-only
proc/PID-file mounts. Updating the diagnostic file requires a collector restart;
preserve capture's expiration and sampling settings around that restart.

The API returns it under `/api/workers` → `diagnostics`. Schema 1 is defined by
`WorkerDiagnostics` and its component types in `collector/worker_diagnostics.go`.
The loader rejects unknown fields, trailing JSON, files above 256 KiB, invalid
time ranges, unscoped PIDs and unknown syscall/epoll labels. Startup fails on an
invalid configured file. It validates structure; **the operator must sanitize
content**. Do not include credentials, raw logs, client addresses, query strings,
memory dumps or raw perf recordings. Snapshot symbols are rendered as text.

The view displays the collection interval and age. PID plus start ticks indicate
whether the same process is still visible. A matching identity does not make an
old measurement current. After a master/worker replacement, rows remain explicitly
historical. The JSON must include the target master process and its matching start
ticks, along with any observed worker/helper processes.

### What each protected reading means

| Reading | Evidence | Limit |
| --- | --- | --- |
| FD categories and epoll registrations | `/proc/PID/fd` and selected `fdinfo` fields | Races with closes/opens; no exact nginx slot decomposition |
| Established socket distribution | Time-stamped `ss` samples with owning worker PID | Configured ports only; socket ownership is different from HTTP request distribution |
| Shared listener | Listener inode and owning master/worker PIDs | Multiple listeners must remain separate; sharing does not guarantee fairness |
| PSS / private / shared memory | `smaps_rollup` | Dated mapped-process pages; excludes kernel socket buffers |
| Read / write syscall totals | `/proc/PID/io` | Lifetime counters; do not include every networking syscall |
| Epoll calls and returned events | Bounded, PID-scoped syscall trace | Includes timeout returns; no attribution to a particular timer |
| Epoll wall-time histogram | Entry/exit timestamps | Includes descheduling; not pure sleeping or CPU time |
| Top sampled instruction locations | Timeboxed CPU profile | Sparse samples, stripped symbols and incomplete stacks limit attribution |

`IN`/`OUT` describe registered readiness interests. `ET` selects edge-triggered
notification. `EXCLUSIVE` reduces competing wakes on a shared registration; it
does not promise equal traffic. `ERR`/`HUP` can appear irrespective of requested
interests. [epoll_ctl documentation](https://man7.org/linux/man-pages/man2/epoll_ctl.2.html)

PSS apportions each shared mapping among its users. RSS counts resident pages in
each process, so summing worker RSS can double-count shared pages.
[Linux mapping memory fields](https://man7.org/linux/man-pages/man5/proc_pid_smaps.5.html)

### Collection procedure and overhead

Obtain the host owner's authorization separately. Scope tracing to workers of the
verified PID-file master, including process start identities; avoid a global
`comm == nginx` filter that includes unrelated nginx instances. Capture aggregate
counts and histograms with a time limit. Never attach `strace -f` to production
workers as a default diagnostic step. BPF/perf add observation overhead; a successful
health check does not prove unchanged request latency.

Keep socket endpoints and raw log lines out of published files. Preserve coverage
limits when reporting zero error categories: log levels, rotation, truncation and
time zones affect the conclusion. Matching NAT rules and a docker-proxy listener
show configuration; proving a particular connection's packet path needs further
evidence. These log/NAT findings belong in the private operator report, not the
public dashboard snapshot schema.

## CPU and memory per request

The Workers view reports the median observed pool used/reserved in the loaded
native completed-event window, including the event count. Pool accounting includes
inspector allocations and omits some large allocations; it is not process RSS,
PSS or kernel socket memory.

There is no direct CPU attribution to an individual request. A workload average
requires CPU and nginx request-counter deltas over the **same interval and scope**.
It includes TLS, timers and background work. Do not divide lifetime cgroup CPU by
a differently reset request counter, or divide CPU by just sampled completions.
The sampler does not automatically mount or read the host cgroup filesystem.

An OFF/ON experiment can estimate observation overhead only with matched traffic
and separate authorization to change capture. Preserve the original expiration;
an experiment must not silently extend capture. Low-load profiles with a handful
of samples cannot establish precise function shares or zero overhead.
