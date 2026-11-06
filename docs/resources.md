# Inspector resource footprint

These are measured examples, not minimum requirements or a zero-overhead claim.
The native module runs inside nginx; the Go collector is a separate process.

## Collector under real traffic

On **2026-11-06, 18:45:53–18:46:53 UTC**, a read-only observation sampled the
collector container 12 times, five seconds apart. Capture stayed at **100%** with
four native workers reporting. The collector received **76 completed request event
datagrams** during the minute: **76 ÷ 60 ≈ 1.27 completed-request events/second**. No load was generated
for this measurement.

| Collector measure | Observed value |
| --- | ---: |
| Completed-request event rate received | **76 events / 60 seconds ≈ 1.27 events/s** |
| Capture sampling | **100%** |
| CPU, mean of sampled readings | **1.59% of one CPU core** |
| CPU, highest sampled reading | **3.18% of one CPU core** |
| Container memory, sampled range | **11.53–14.62 MiB** |
| Container memory, sample median | **12.13 MiB** |
| Container tasks, observed maximum | **8** |
| Configured CPU ceiling | **0.5 CPU core** |
| Configured memory ceiling | **192 MiB** |

The 1.27 events/s rate counts received completed-request datagrams, not every
nginx internal operation or all collector traffic. Worker snapshots are additional:
four reporting workers are configured to send one snapshot each per second. Those
snapshots and dashboard polling also contribute to collector usage.

CPU and container memory come from Docker stats. Container memory is not the Go
heap size or a per-request allocation. These light-traffic readings do not predict
usage at thousands of requests per second, with many viewers, or with long-lived
requests. CPU ceilings apply to the collector, not to work done inside nginx.

## Native module allocation

The tested Linux amd64 module's compiler metadata gives these structure sizes:

| Allocation | Size |
| --- | ---: |
| Request observation context | 384 bytes |
| Trace slots | 40 × 96 bytes |
| Subrequest slots | 8 × 56 bytes |
| Fixed total per selected request | **4,672 bytes, approximately 4.56 KiB** |

The total excludes pool bookkeeping, bounded copied paths/locations and nginx
variable allocations. It is an allocation calculation, not measured process RSS.
Temporary serialization buffers are also separate: up to 16 KiB on the request
log-hook stack and 8 KiB for a worker snapshot.

Contexts live in the request pool and are released with that pool. Native memory
therefore grows with selected in-flight requests. Deleting saved history does not
remove this live request cost. Structure sizes can differ across builds and ABIs.

## nginx process context

A separate two-minute read-only sample, also with full capture active, observed:

| Process | Observed mean CPU per process | Observed RSS |
| --- | ---: | ---: |
| Active nginx workers | About 0.04–0.05% of one core | About 10.8–11.3 MiB each |
| nginx master | 0.00% at the sample resolution | About 9.4 MiB |

These are **whole nginx process numbers**. They include nginx's normal work and
cannot be attributed entirely to the inspector. RSS is approximate and can count
shared pages in multiple processes. Read the [worker metric definitions](/workers).

## History and pressure limits

The observed deployment used a rolling four-hour history with at most 10,000
events. Older rows are pruned on insert; freed SQLite pages can be reused. Deleting
rows does not necessarily shrink the database file immediately. The product
defaults remain 24 hours and 100,000 events, configurable at startup.

The input queue is bounded to 2,000 validated events. Worker snapshots are kept in
bounded memory and expire from the live view. Storage or transport pressure can
drop observation records; nginx does not wait for the database.

For measured latency/throughput costs, see [Traffic behavior and overhead](/performance).
Full capture had a substantial cost in the isolated high-throughput fixture even
though these light-traffic collector readings are small. New sessions default to
1% sampling; full sampling is an explicit operating choice.
