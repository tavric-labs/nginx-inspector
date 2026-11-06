# From the kernel to an nginx worker

Replay adds a separate **Kernel → socket → nginx worker** walkthrough alongside
the seven HTTP stages. Its Previous/Next controls explain the transport mechanism
independently of HTTP playback.

**This is a mechanism walkthrough with captured connection context, not a packet
trace. Kernel processing and queue residence times remain “Not measured.”**

## The queues mean different things

| Queue or list | What it holds |
| --- | --- |
| SYN queue | Incomplete TCP handshakes |
| Accept queue | Established connections waiting for acceptance |
| Socket receive queue | Received bytes waiting to be consumed |
| Epoll ready list | References to registered descriptors ready for I/O |
| CPU run queue | Runnable threads waiting for a CPU |

Listener backlog concerns completed connections; incomplete handshakes have a
separate limit. SYN cookies, deferred accept and TCP Fast Open can change the
simplified sequence. [Linux listen](https://man7.org/linux/man-pages/man2/listen.2.html)

Epoll's interest list contains subscriptions. Its ready list reports I/O readiness,
not copies of HTTP requests. [Linux epoll](https://man7.org/linux/man-pages/man7/epoll.7.html)

## Follow the transport mechanism

Packets enter through a physical/virtual interface or loopback and undergo network
processing. The selected record does not identify its receive interface or CPU.
[Linux receive scaling](https://docs.kernel.org/networking/scaling.html)

A new connection proceeds through TCP setup and acceptance. Accept returns a
connected socket distinct from the listener.
[Linux accept](https://man7.org/linux/man-pages/man2/accept.2.html)

nginx uses event handlers and nonblocking I/O to advance the connection. TLS when
used, and HTTP parsing, lead toward the observed HTTP lifecycle.
[nginx event loop](https://nginx.org/en/docs/dev/development_guide.html#event_loop)

An accept mutex, EPOLLEXCLUSIVE or reuseport may influence worker selection.
The captured worker PID does not reveal that selection's exact cause or timestamp.
[nginx accept policy](https://nginx.org/en/docs/ngx_core_module.html#accept_mutex)

## Reuse changes the story

When nginx reports a request sequence greater than one, the walkthrough labels
handshake, accept and initial worker assignment as **earlier connection setup**.
Their cost is unknown, rather than zero. The keepalive policy flag alone is not
used to claim the connection was reused.

HTTP/2 readiness belongs to the connection; frames identify individual streams.
One readiness return can advance several streams, and buffered bytes may permit
parsing without another read. A fresh handshake or epoll wakeup is not required
for every request. Missing sequence numbers remain unknown. HTTP/3 or a backend
other than epoll gets an explicit applicability note.

## What was measured

The walkthrough shows captured worker PID, connection ID, protocol and request
sequence when available. A POST_READ offset is relative to **nginx request start**,
not packet arrival or TCP setup. Final HTTP duration is separate; an in-flight
request has no final duration or invented earlier observations.

Kernel-stack CPU time, SYN/accept residence, readiness-to-scheduling delay and the
read/TLS/parsing breakdown are not currently correlated with individual requests.
Worker-wide epoll histograms and CPU profiles cannot supply these missing times.
Unknown intervals are never treated as zero or added to the HTTP timeline.

Measuring them requires separate connection-correlated kernel observation, defined
event boundaries, and handling of descriptor reuse and multiplexed streams.
This interface change starts no tracing and adds no nginx hooks.
