# Troubleshooting

## The viewer does not open

Check the collector process and `curl http://127.0.0.1:19877/health` on its host.
The default listener is loopback. Use an SSH tunnel for remote access or explicitly
configure a read-only private listener. Capture being OFF does not stop the viewer.

## No native details in an old event

Schema-1 events lack native state. Install the current module and record a new
short capture. Historical values cannot be reconstructed.

## Capture is active but no events arrive

Check matching UDP port, matching control path, worker read permissions and
sampling/location settings. The control file must exist before workers map it.
Check `/health` for invalid, queue-dropped, storage-dropped or inactive-dropped
counters. Long-lived requests only emit at completion.

## nginx rejects the module

Compare `nginx -V` with the exact module build source/configuration, architecture
and distribution patches. Do not force an ABI-incompatible shared object into nginx.
Restore the previous module and validate configuration before any reload.

## A request is absent after Stop

The gate is checked again at log. A request that finishes outside the capture
window is not emitted. UDP, serialization caps, sampling and collector pressure
can also lose observation records. Request processing does not depend on event delivery.
