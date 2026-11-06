# Development

## Local binaries

Use the Go version declared in `go.mod` or a newer supported toolchain.

```sh
go build -o build/nginx-inspector ./cmd/nginx-inspector
go test -race ./...
```

The production implementation is Go and C. Python supplies the demo upstream,
process orchestration and black-box integration drivers.

## Isolated nginx fixture

```sh
docker compose up --build --detach
python3 tests/integration.py
python3 tests/native_integration.py
python3 tests/on_demand.py
python3 tests/equivalence.py
npm ci
npx playwright install chromium
npm run test:viewer
docker compose down
```

The native integration fixture exercises retries, buffering overrides, cache
MISS/HIT, gzip, file output, auth subrequests, upload spooling and local body-limit
rejection, bandwidth limiting and live worker/resource snapshots. The on-demand suite checks inactive capture and timed stop. The browser
suite checks filtering, a visible Inspect dialog, JSON download and missing-event errors.

## Documentation

```sh
npm ci
npm run docs:dev
npm run docs:build
```

The build rejects dead internal links. Keep the README concise; detailed behavior,
operations and design belong in this manual. Do not put credentials, private host
inventories or real captured request data in source/docs.
