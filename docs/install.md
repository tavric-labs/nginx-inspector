# Installation

## 1. Install the Go executable

Download the matching archive and `SHA256SUMS` from
[GitHub Releases](https://github.com/tavric-labs/nginx-inspector/releases).
Verify the archive before extracting it. Linux supports native capture serving;
macOS and Windows binaries provide remote CLI commands.

```sh
sha256sum --check --ignore-missing SHA256SUMS
tar -xzf nginx-inspector_VERSION_linux_amd64.tar.gz
./nginx-inspector version
```

Replace `VERSION` with the release version and select `arm64` on an ARM64 host.
No Python interpreter is required by the collector.

## 2. Build the nginx module

The `.so` must match your nginx version, architecture, configure options, and vendor
patches. The Go executable's architecture alone does not determine module ABI.
Inspect `nginx -V` and use your distribution's matching development headers.

```sh
./configure --with-compat --add-dynamic-module=/path/to/nginx-inspector/module
make -f objs/Makefile modules
```

The current source produces `objs/ngx_http_inspector_v2_module.so`. Install it
at a versioned path and use that path in `load_module` when upgrading an existing
loaded generation. The generic path below is suitable for a first installation.

Run those commands in the matching nginx source tree, preserving the relevant
options from your installed nginx. Ubuntu's patched packages require matching
Ubuntu source/headers, not an upstream tarball with the same version number.

## 3. Provision data and control

Run the collector as a dedicated unprivileged user. Its database directory should
be mode `0700`. Provision a separate traversable control directory if nginx workers
use another account. The stable control file is owned by the collector, mode `0644`.
The collector creates it before nginx workers start and maps exactly eight bytes.

```sh
nginx-inspector serve --db /var/lib/inspector-data/events.sqlite3 \
  --control-file /var/lib/nginx-inspector/control
```

## 4. Load and validate

At nginx's top level:

```nginx
load_module /usr/lib/nginx/modules/ngx_http_inspector_module.so;
```

In `http` (the control path is an http-level directive):

```nginx
inspector on;
inspector_control /var/lib/nginx-inspector/control;
inspector_port 19876;
inspector_sample 10000;
```

Validate with `nginx -t`, then use your normal service manager to reload nginx.
The collector remains OFF until explicitly started. See [deployment](/deployment)
for rollback and container operation.

Enable/disable, sample and port settings can be overridden in a `server` or `location`.
