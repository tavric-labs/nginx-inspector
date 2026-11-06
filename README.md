# nginx-inspector

See how a request moves through nginx: locations, phase hooks, upstream attempts,
buffering, cache, filters, subrequests, and worker state.

A native **C nginx module** observes bounded metadata. A compiled **Go executable**
stores history and serves a desktop inspection dashboard. Capture starts **OFF**.

**[Documentation](https://nginx-inspector.tavric-labs.net)** ·
**[Download binaries](https://github.com/tavric-labs/nginx-inspector/releases)** ·
**[Architecture](https://nginx-inspector.tavric-labs.net/architecture)**

## Run a capture

Download the executable for your platform, verify its checksum, and install a module
built against your exact nginx version and vendor patches. Follow the
[installation guide](https://nginx-inspector.tavric-labs.net/install) for the one-time setup.

```sh
nginx-inspector serve --db ./data/events.sqlite3 --control-file ./data/control
# In another terminal:
nginx-inspector start --for 10m
nginx-inspector status
nginx-inspector stop
```

Open **http://127.0.0.1:19877/**. Use `start` without `--for` for a manual session,
or `--for 1h` for an automatic stop. History remains available after capture stops.

## What you can inspect

- Original and normalized paths, final location, internal redirects and subrequests.
- Hook order, filter returns, readiness/timer snapshots, buffer and temporary-file use.
- Individual upstream attempts and timings, configured retries and timeouts.
- Configured versus effective buffering, cache outcomes, gzip and file-backed output.
- Worker/event backend, connection counters and bounded request-pool estimates.

Bodies, query strings, authorization, cookies and raw error logs are excluded.
This is bounded hook observation: it does not record every syscall or reconstruct
every nginx decision. The [coverage guide](https://nginx-inspector.tavric-labs.net/coverage)
explains what each observation can establish.

## Build and contribute

```sh
go build -o build/nginx-inspector ./cmd/nginx-inspector
go test -race ./...
docker compose up --build --detach
python3 tests/integration.py
python3 tests/native_integration.py
python3 tests/on_demand.py
```

The production runtime is Go and C. Python is used for development fixtures and
integration drivers. See [development](https://nginx-inspector.tavric-labs.net/development)
and [release builds](https://nginx-inspector.tavric-labs.net/releases).
