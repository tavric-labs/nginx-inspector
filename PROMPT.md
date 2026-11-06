# nginx-inspector: design prompt

Paste everything below the line into a new session started in this folder.

---

You are helping me design a personal open-source project: an nginx traffic-inspection
plugin. I will own the machine, so assume root, a compiler toolchain and Docker are
available. Do not design around permission limits.

## Goal
A plugin that captures and lets me inspect everything nginx does with each request,
in real time and historically. Two purposes: (1) understand how nginx works
internally (phases, upstreams, buffering, keepalive); (2) a usable observability tool
for a reverse proxy in front of several backend services.

## Start by teaching me, briefly
Before proposing anything, explain how nginx processes a request: master/worker
model, the event loop, the request phases (post-read, rewrite, access, content, log),
filter chains for headers and body, and how upstream proxying works. Use a diagram.
Keep it under one page.

## Compare the integration options, then recommend one
For each option give how it hooks into nginx, what it can see (headers, body,
upstream timings, TLS info, internal redirects), overhead, and build and deploy cost:
1. A native C dynamic module (`ngx_http_module_t`, handlers in phases, header/body
   filters, loaded with `load_module`).
2. njs (nginx JavaScript) with `js_set`, `js_content` and `js_body_filter`.
3. OpenResty/Lua (`log_by_lua`, `body_filter_by_lua`).
4. The `mirror` directive sending a copy of each request to a capture service.
5. A custom `log_format` emitting JSON, plus a log shipper.
6. An eBPF or packet-capture approach outside nginx.
Recommend one primary approach and one fallback, and say why.

## Design the chosen approach
- **Capture schema:** per request: id, timestamps per phase, client info, method,
  URI, headers (with a redaction list for Authorization, cookies, tokens), request
  and response sizes, optional body sampling, upstream name, upstream connect/header/
  response times, status, cache status, TLS version and cipher.
- **Safety:** redaction rules, body-size caps, sampling rate, never log secrets,
  and a clear opt-in for body capture.
- **Performance:** non-blocking writes (shared-memory ring buffer or an async
  socket), no blocking I/O in a worker, and a measured overhead budget.
- **Transport and storage:** a local collector (small Go or Python service) that
  receives events and writes to SQLite or PostgreSQL, with retention.
- **UI/CLI:** a live terminal view in the style of `ngxtop` (requests per route,
  status mix, p50/p95/p99 latency, top clients, errors) and a simple web page for
  drill-down on one request.
- **Config:** the nginx directives the plugin adds, with an example `nginx.conf`.

## Deliverables
1. An architecture document with diagrams and the trade-off table.
2. A repository layout and a milestone plan: M1 minimal module that logs one line
   per request, M2 timings and headers, M3 collector and storage, M4 UI, M5 hardening.
3. A Docker-based dev environment: build nginx from source with the module, run a
   test backend, and a traffic generator, so I can iterate without touching a real host.
4. A test plan: unit tests for the collector, integration tests that compare
   captured data with a known request, and a load test proving the overhead budget.
5. A list of risks and open questions for me to decide.

Ask me at most three questions before you start, only if the answers change the
design. Otherwise state your assumptions and proceed.
