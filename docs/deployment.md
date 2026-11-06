# Deployment

## Runtime

Run the Linux executable as a dedicated user, with a writable private data
directory and a stable control directory readable by nginx workers. Bind UDP to
loopback. The collector always binds loopback HTTP controls; `--viewer-host` adds
a second read-only listener on a specific local IPv4 address.

```sh
nginx-inspector serve \
  --db /data/events.sqlite3 \
  --control-file /control/control \
  --retention-hours 24 --max-events 100000
```

For a container, use host networking to reach host nginx's loopback UDP, a read-only
root filesystem, no added capabilities, `no-new-privileges`, an unprivileged UID,
and explicit memory/CPU/pid limits. Mount only the executable, data and control
paths required by the service. `deploy/Dockerfile` provides a compiled runtime image.

## Upgrade sequence

1. Stop capture and confirm inactive state.
2. Back up the database consistently with SQLite's backup API or a stopped collector.
3. Save the current executable, module and exact nginx config for rollback.
4. Replace the module atomically; never truncate a mapped shared object.
5. Run `nginx -t` before reload. Reload only nginx through the normal service manager.
6. Start the new collector with the same data/control mounts; it starts OFF.
7. Check `/health`, normal nginx traffic, and a short capture, then stop capture.

Keep the control inode stable. If a module upgrade fails validation, restore the
previous module/config before reloading. Retain rollback artifacts until normal
traffic and captures are verified.

## Documentation hosting

This manual uses the shared Tavric Labs documentation host. CI builds immutable
site artifacts and publishes through a per-site forced-command SSH key. Deployment
switches the site's current symlink atomically and verifies the public HTTPS revision.
