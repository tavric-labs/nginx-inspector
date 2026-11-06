# Architecture

## Data path

```text
client ──► nginx worker ──► upstream
              │
              ├─ C phase/filter hooks (bounded request context)
              ├─ read-only monotonic capture lease
              └─ nonblocking loopback UDP :19876
                           │
                    Go collector
                    validate → bounded queue → SQLite WAL
                           │
               HTTP :19877 / embedded desktop viewer
               + optional read-only proc worker sampler
                    loopback controls
                    optional read-only interface
```

## Why a dynamic C module

Access logs provide useful final outcomes, but do not expose all internal state.
A module reads nginx request, upstream, buffer and connection structures directly
at known hooks. It uses supported module/filter registration and chains existing
handlers. It neither patches the nginx executable nor wraps traffic in another proxy.

The cost is ABI coupling. Build the module against the exact vendor nginx source
and configuration. A Go collector binary can be portable across Linux releases;
the module cannot be treated as a universal shared object.

## Why a separate Go executable

Storage, HTTP, UI and CLI remain outside nginx workers. A stalled disk cannot
block a worker waiting for the collector: the transport is nonblocking UDP with
bounded serialization. The executable embeds HTML and uses a pure-Go SQLite
driver, allowing `CGO_ENABLED=0` release builds for multiple architectures.

CGO is not required to talk to the module. The two processes share an explicit
event protocol and lease file rather than nginx pointers.

## Failure boundaries

The gate is OFF on collector startup/shutdown and expires after a crash. Queue
overflow and invalid datagrams drop observation records. Storage failures mark
health unhealthy. Existing nginx request handling continues through its normal
filter chain. These boundaries limit failure propagation; they do not claim zero
instrumentation overhead.

## History compatibility

The Go collector keeps the existing `events` SQLite table and accepts legacy
schema 1 plus native schema 2. It does not invent missing native data for old rows.
Schema 2 is closed: unknown fields/types and excessive observation arrays are rejected
before persistence. Query portions are stripped again during validation.

Worker telemetry uses a separate closed, 8 KiB bounded schema and in-memory snapshot
cache. It is not written to request history. See [Workers](/workers) and
[traffic behavior and overhead](/performance).
