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

This is not every transition. A short request can finish between snapshots; use its
completed event for retained history. A snapshot expires after four seconds and
live request rows disappear immediately from the API when capture stops. The
collector keeps these worker snapshots in bounded memory, outside SQLite history.

## Containers

For host nginx, mount the host proc filesystem and the host nginx PID file read-only,
then pass their container paths to `--proc-root` and `--nginx-pid-file`. Host PID
namespace, root UID and extra capabilities are not required. Kernel proc access
policy can still make some fields unavailable.

Process sampling is optional and bounded to 8,192 directory entries and 64 nginx
processes per scan. Configure the PID scope to avoid combining separate nginx
instances on the same host.
