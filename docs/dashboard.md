# Desktop inspector

The dashboard is designed for a desktop operations workspace. It refreshes every
two seconds and displays the latest 100 retained requests. Search by path, client,
request ID, status or upstream; use method/status filters to narrow the view.
Pause refresh while comparing records.

**Inspect** opens an in-viewport dialog. Native details lead the view:

1. Routing and upstream decisions.
2. Derived explanations linked to recorded state.
3. Hook order and event readiness/timer snapshots.
4. Upstream attempts and configured proxy policy.
5. Response filters, request-body buffers and temporary-file use.
6. Worker/connection snapshots, subrequests and request-pool limits.
7. HTTP context, allowlisted headers and raw JSON.

Use **Download JSON** to export the retained event. All dynamic values are inserted
as text. Missing events produce a visible error with a retry action.

The viewer is read-only. Capture control stays in the loopback CLI/API. A private
interface viewer exposes stored request metadata to people who can reach it;
use a tunnel if you want loopback-only access.

## Replay {#replay}

The **Replay** view plays a request's journey through nginx's seven stages (Parsed, Rewrite,
Access, Content, Headers, Body, Done) and shows what nginx did at each one. It has two
sections on the left.

**Completed** lists the latest recorded requests. Each row carries a small ribbon of the
seven stages: a dim segment was not reached, and the amber one is where most of the time
went. Pick one to replay it:

- **Story** mode steps through the stages that were reached, 2.6 seconds each, because the
  real gaps are usually a millisecond or less and cannot be followed by eye.
- **True to scale** plays real elapsed time at a speed you choose, from slow motion to
  x1000, with a scrubber. A four-minute tool call plays in seconds at x100 and the long wait
  shows up in the Content stage.

For each stage the panel lists what nginx did, tagged **observed** (read from nginx at that
hook) or **configured** (policy that does not prove the path ran): the connection and TLS
facts, the selected location and rewrites, access checks and subrequests, each upstream
attempt with its timings, the response status and buffering, the bytes seen at the hook
against the bytes on the wire, and the sockets' state at that moment (see
[Workers](./workers.md#socket-state)). Upstream attempt times overlap and are not added.

**In flight** lists live sampled requests. Only the latest stage is known while a request
is open, so the view shows that stage, marks earlier stages as passed (inferred), and keeps
a short log of what the page noticed since you opened it. It cannot replay the earlier
stages, which pass in about a millisecond; when the request finishes, open it under
**Completed** for the full replay.

A request that began before capture started has no recorded stages and says so.

## Understand the worker readings

Workers starts with four questions: connection capacity, CPU work, request-delay
investigation and memory interpretation. Each answer gives its evidence, purpose
and suggested next check. Stale or missing data stays unknown; a single reading
does not establish a bottleneck or leak. Detailed counters and historical evidence
are expandable, and their open state survives refreshes.

### Kernel and socket context in Replay

The [kernel-to-worker walkthrough](./kernel-to-worker.md) is separate from the
seven HTTP stages. It explains TCP queues, socket subscriptions, epoll readiness
and worker dispatch, with connection-reuse and HTTP/2 context. Captured facts and
HTTP times stay separate from transport steps whose timings were not measured.
