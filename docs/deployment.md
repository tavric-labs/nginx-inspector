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

## Automated module reload (`nginx-inspector reload`)

To compile or install a new module generation with a distinct `ngx_http_inspector_<gen>_module`
symbol (including the `ngx_inspector_request_begin` / `ngx_inspector_request_end` markers
used for correlated kernel transport capture), update `load_module`, validate `nginx -t`
(with automatic config rollback on failure), reload nginx, and optionally start a full
capture immediately:

```sh
nginx-inspector reload --build-module --all --for 30s
```

Use `nginx-inspector doctor` at any time to verify the privilege boundaries, 8-byte
control file, directory traversal permissions, marker ABI, `/proc` worker visibility,
and collector health.

## Manual upgrade sequence

1. Stop capture and confirm inactive state.
2. Back up the database consistently with SQLite's backup API or a stopped collector.
3. Save the current executable, module and exact nginx config for rollback.
4. Install the module at an immutable versioned path and update its `load_module` directive. Never truncate a mapped shared object or assume replacing a same-path `.so` activates new code.
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

A new module generation uses a distinct exported module symbol to avoid symbol
interposition with a still-loaded generation. Verify a fresh captured event from the
new worker generation; a successful signal command alone is not activation proof.
See the [dynamic-module engineering article](https://blog.nginx.org/blog/nginx-dynamic-modules-how-they-work).

For a later C-module generation, build with a new identifier, for example
`NGINX_INSPECTOR_MODULE_NAME=ngx_http_inspector_r20261106_module ./configure ...`.
Use the resulting versioned shared object in `load_module`. This avoids reusing
the exported symbol of a still-loaded generation.
