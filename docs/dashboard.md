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
