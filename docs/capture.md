# Capture control

| Command | Behavior |
| --- | --- |
| `start` | Manual session with 1% sampling until stopped or collector exits |
| `start --for 30s` | Timed session |
| `start --for 10m` | Ten-minute session |
| `start --for 1h` | One-hour session |
| `stop` | Disable recording; retain saved history |
| `status` | Active state and remaining time |
| `top --once` | Retained history summary |

Use `start --sample 100 --for 30s` for an explicit short full capture, or
`start --sample 0.1 --for 10m` for lighter sampling. Percentages range from 0.01 to 100.
The nginx `inspector_sample` setting remains an upper bound.

Maximum timed duration is 24 hours. A collector restart always starts OFF.

## Lease protocol

The collector publishes an aligned 64-bit word containing a 48-bit `CLOCK_MONOTONIC` deadline
and a 16-bit per-session sample rate to a stable
shared file. It renews a 1.5-second lease every 250 ms. nginx maps it read-only
and checks the deadline before allocating observation context and before emitting.
If the collector crashes, the lease expires within 1.5 seconds.

Never replace the control file inode while workers use it. File permissions and
ownership are checked by the collector. nginx workers need read access and directory
traversal; they do not need write access.

## Scope and sampling

`inspector_sample` is an upper bound in parts per ten thousand: `10000` selects all eligible
requests, `100` selects about one percent. Use `inspector off` for excluded locations.
Capture observations remain bounded even when the selected request is long-lived.

The module chains existing nginx filters and does not force reads or proxy a
second request. Nonblocking UDP can drop observation events under pressure;
nginx does not wait for storage or the dashboard.
