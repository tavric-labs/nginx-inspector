# nginx inspector

A native nginx dynamic module plus a Python standard-library collector. Inspect
completed requests in a live terminal view or a local web page, with SQLite history.
The worker sends one bounded JSON datagram per sampled main request; it never waits
for the collector. This is a working prototype, not exhaustive nginx tracing.

Capture defaults to **off**. Start it when needed, then stop it manually or set a
duration. The native module checks a shared memory deadline before allocating
request state; stopped capture performs no header serialization or event sends.

```sh
./bin/nginx-inspector start             # capture until stopped
./bin/nginx-inspector start --for 10m   # automatically stop after ten minutes
./bin/nginx-inspector start --for 1h    # automatically stop after one hour
./bin/nginx-inspector status
./bin/nginx-inspector stop
./bin/nginx-inspector top              # inspect saved/live request summaries
```

Durations accept `s`, `m`, or `h`, up to 24 hours. Starting again replaces the
current timer. Stopping preserves history and the viewer. The collector restarts
with capture off. If it crashes or is killed, nginx stops observing within 1.5
seconds when its heartbeat lease expires. Start/stop needs no nginx reload.
These shared-memory controls currently support Linux x86-64; the module and
collector must share the host's monotonic clock (the Docker default).

Read [the architecture and integration comparison](docs/architecture.md) and
[the verification and milestone plan](docs/verification.md).

## Run the isolated development stack

On a Linux Docker host with the two loopback ports available:

```sh
docker compose up --build -d
python3 dev/traffic.py --requests 100
python3 tests/integration.py
python3 tests/on_demand.py
python3 collector/inspector.py top --once
```

Open **http://127.0.0.1:19877**. The demo reverse proxy is
**http://127.0.0.1:18080**. The backend listens only on loopback port 18081.
The compose stack uses the host network to share loopback with the collector;
all three listeners explicitly bind to loopback. This setup is Linux-specific.

For Ubuntu's packaged nginx (including its patched development headers), without
building a Docker image:

```sh
docker run -d --name nginx-inspector-dev --network host \
  -v "$PWD:/workspace" ubuntu:24.04 sh /workspace/dev/build-ubuntu.sh
docker logs -f nginx-inspector-dev
```

This installs build dependencies **inside the disposable container**. It does not
install packages on the host. Wait until nginx starts before running integration
tests. `build/nginx-version.txt` records the package build; `build/` is ignored.
Stop that specific container with `docker stop nginx-inspector-dev`.

## Run the collector separately

Python 3.10+; no pip dependencies:

```sh
python3 collector/inspector.py serve --db /path/to/private-data/events.sqlite3 \
  --retention-hours 24 --max-events 100000 \
  --control-file /var/lib/nginx-inspector/control
./bin/nginx-inspector start --for 10m
python3 collector/inspector.py top
```

The collector listens on 127.0.0.1 UDP 19876 and HTTP 19877. For a remote host,
use an SSH tunnel to the HTTP port. There is no public route or web authentication;
do not expose the listener outside loopback. The storage directory should be 0700.
The CLI reports route counts, status mix, top clients, errors and exact p50/p95/p99
for the latest 10,000 retained events. These are counts in a bounded window, not
requests-per-second rates.

## Load the module

Build against your installed nginx's version, vendor patches and build options.
An arbitrary `.so` from another nginx installation is not interchangeable.

```nginx
# Main context, before events/http:
load_module /absolute/path/ngx_http_inspector_module.so;

http {
    inspector on;                 # default off; http/server/location contexts
    inspector_control /var/lib/nginx-inspector/control;
    inspector_port 19876;         # loopback destination only, 1024..65535
    inspector_sample 10000;       # per ten thousand: 10000 = 100%, 1000 = 10%

    server {
        listen 127.0.0.1:18080;
        location / { proxy_pass http://127.0.0.1:18081; }
        location /sensitive/ { inspector off; }
    }
}
```

Enabling at `http` level ensures early post-read observations are available. If
enabled only in a location, early observations may be null. Sampling is selected
once, when the context first becomes available. `inspector off` in the final
location suppresses the event, including after an internal redirect.

The control file must exist before nginx workers start, have exactly 8 bytes, and
be readable by workers. Give its parent directory mode 0755, its file mode 0644,
and file ownership to the collector's UID. Only a deadline is stored there; keep
the request database in a separate private directory. Bind-mount the control
directory into the collector container so both processes see the same inode.
Start the collector once to initialize the file, then start/reload nginx. Never
replace or unlink the control file while workers have it mapped. Missing/invalid
control disables capture; a reload is needed after fixing a missing mapping.
Install module upgrades with an atomic file replacement rather than truncating a
`.so` mapped into running workers.

The loopback control API uses JSON POSTs to `/api/capture/start` (optional
`seconds`) and `/api/capture/stop`. `GET /api/capture` reports state. Browser
requests must have a same-origin local Host/Origin; no CORS mutation is enabled.
Anyone with access to this host's loopback listener can control inspection.

To share history on a specific private interface, add `--viewer-host PRIVATE_IP`
to `serve`. This adds a second listener on the same HTTP port. It allows read-only
history/status access and rejects every control POST, even with a forged Host.
Capture controls remain on 127.0.0.1. This viewer has no login: only enable it on
a network where everyone who can reach it may view the captured metadata.

Before changing an existing nginx, back up its exact configuration, start the
collector, load the module in an isolated canary using the **existing binary**,
exercise proxying, errors, keepalive, and secret exclusions, then run `nginx -t`
against the live configuration. Reload only after success. Roll back by removing
the module/config include files, testing and reloading again. A reload preserves
old workers until in-flight requests complete; keep the collector running meanwhile.

## Capture boundaries

Captured: generated request ID, epoch start, duration, phase boundary offsets,
client address, method, final normalized URI path (256-byte cap), response status,
request/wire/observed response-body sizes, keepalive request counter, rewrite
visits, upstream attempt variables, cache status and TLS protocol/cipher.

Request headers are allowlisted to `Content-Type` and `Content-Length`; response
headers to `Content-Type`. Authorization, Proxy-Authorization, Cookie, Set-Cookie,
API-key/token headers and every other header are omitted, not stored with secrets
and redacted later. Queries and request/response bodies are never emitted.
Paths, client IPs and content types can still be sensitive. Disable inspection for
routes that put secrets in paths. There is no universal content redactor.

Body capture has **no enabling directive** in this version. A future feature must
require explicit opt-in, an allowlist of safe routes/content types, a 1 KiB cap,
sampling, and structured redaction before bytes leave nginx. It must never force
request-body reads or disk-buffer reads. See the M5 plan before enabling it.

Loopback UDP is best effort. Worker counters report local send errors, not kernel
or receiver losses. Collector health reports rejected events, queue drops and
storage failures. Check `/health` and event arrival; successful sends do not prove
persistence. This tool complements access logs and existing application metrics.

## Check

```sh
python3 -m unittest discover -s tests -v
python3 tests/integration.py
python3 tests/on_demand.py
python3 dev/traffic.py --requests 20000 --concurrency 8 --path /local
```

Run the load command with the same module loaded and `inspector off`/`on` in
alternating order, warm up first, and compare at least three pairs. The target is
less than 5% throughput loss and less than 1 ms additional p95 latency. A shared
VM result is an experiment, not a production performance guarantee. Compare
collector row growth against generated request count and account for any losses.
