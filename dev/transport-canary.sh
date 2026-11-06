#!/usr/bin/env bash
# Disposable CI host only; never run this dependency-installing script on production.
set -euo pipefail
task_dir=$(mktemp -d /tmp/nginx-transport-canary.XXXXXX)
collector_pid=""
trace_pid=""
cleanup() {
  mkdir -p build/transport
  for name in trace.log trace.ndjson collector.log error.log; do if [ -f "$task_dir/$name" ]; then cp "$task_dir/$name" "build/transport/canary-$name"; fi; done
  if [ -n "$trace_pid" ]; then sudo kill -INT "$trace_pid" 2>/dev/null || true; fi
  if [ -f "$task_dir/nginx.pid" ]; then /usr/sbin/nginx -p "$task_dir/" -c "$task_dir/nginx.conf" -s quit || true; fi
  if [ -n "$collector_pid" ]; then kill "$collector_pid" 2>/dev/null || true; wait "$collector_pid" 2>/dev/null || true; fi
}
trap cleanup EXIT
cp -a /usr/share/nginx/src "$task_dir/src"
task_repo=$(pwd)
(
 cd "$task_dir/src"
 NGINX_INSPECTOR_MODULE_NAME=ngx_http_inspector_transport_v3_module ./configure --with-compat --with-debug --with-http_ssl_module --with-http_v2_module --with-http_stub_status_module --with-http_auth_request_module --with-threads --add-dynamic-module="$task_repo/module"
 make -f objs/Makefile modules -j2
)
cp "$task_dir/src/objs/ngx_http_inspector_transport_v3_module.so" "$task_dir/inspector.so"
mkdir -p build/transport
cp "$task_dir/inspector.so" build/transport/ngx_http_inspector_module.so
dpkg-query -W -f='${Version}\n' nginx > build/transport/nginx-package-version.txt
/usr/sbin/nginx -V > build/transport/nginx-build.txt 2>&1
sha256sum build/transport/ngx_http_inspector_module.so > build/transport/SHA256SUMS
CGO_ENABLED=0 go build -trimpath -o "$task_dir/nginx-inspector" ./cmd/nginx-inspector
cp "$task_dir/nginx-inspector" build/transport/nginx-inspector
if [ "${1:-}" = "--build-only" ]; then echo "PASS: native module and tracer binary compiled; privileged runtime test not requested."; exit 0; fi
cat > "$task_dir/nginx.conf" <<EOF
load_module $task_dir/inspector.so;
daemon on;
pid $task_dir/nginx.pid;
error_log $task_dir/error.log notice;
events {worker_connections 128;}
http {access_log off;client_body_temp_path $task_dir/client;proxy_temp_path $task_dir/proxy;inspector on;inspector_sample 10000;inspector_port 19576;inspector_control $task_dir/control;
server {listen 127.0.0.1:19580;location / {return 200 "transport-canary";}}}
EOF
"$task_dir/nginx-inspector" serve --db "$task_dir/events.sqlite3" --control-file "$task_dir/control" --http-port 19577 --udp-port 19576 --proc-root /proc --nginx-pid-file "$task_dir/nginx.pid" > "$task_dir/collector.log" 2>&1 &
collector_pid=$!
for i in $(seq 1 50); do if curl -fsS http://127.0.0.1:19577/health >/dev/null; then break; fi; sleep .1; done
/usr/sbin/nginx -p "$task_dir/" -c "$task_dir/nginx.conf"
curl -fsS http://127.0.0.1:19577/api/capture/start -H 'Content-Type: application/json' -d '{"seconds":120,"sample_permyriad":10000}'
sudo "$task_dir/nginx-inspector" trace --for 12s --master-pid "$(cat "$task_dir/nginx.pid")" --module "$task_dir/inspector.so" --udp-port 19576 > "$task_dir/trace.ndjson" 2> "$task_dir/trace.log" &
trace_pid=$!
for i in $(seq 1 600); do if rg -q TRACE_READY "$task_dir/trace.log"; then break; fi; if ! kill -0 "$trace_pid" 2>/dev/null; then cat "$task_dir/trace.log"; exit 1; fi; sleep .1; done
rg -q TRACE_READY "$task_dir/trace.log"
python3 - <<'PY'
import http.client,time
for i in range(12):
 c=http.client.HTTPConnection('127.0.0.1',19580,timeout=3)
 for j in range(2):
  c.request('GET','/transport-%d-%d'%(i,j));r=c.getresponse();assert r.status==200 and r.read()==b'transport-canary';time.sleep(.12)
 c.close()
PY
if ! wait "$trace_pid"; then cat "$task_dir/trace.log"; exit 1; fi
trace_pid=""
sleep 1
python3 - <<'PY'
import json,urllib.request
def get(path):return json.load(urllib.request.urlopen('http://127.0.0.1:19577'+path))
events=get('/api/events');assert len(events)>=20
views=[get('/api/transport/'+e['id']) for e in events]
matched=[v for v in views if v['status']=='correlated_connection'];assert len(matched)>=20,views[:2]
names={r['event'] for v in matched for r in v['records']}
assert {'accept','request_begin','request_end','read','register','ready'}<=names,names
assert any(v['connection_shared'] for v in matched), 'keepalive markers did not share a generation'
boundaries=[r for v in matched for r in v['records'] if r['event']=='accept' and int(r.get('queued_ns','0'))>0]
if boundaries:
 for r in boundaries:
  assert r.get('queue_source') in (1,2), r
  assert int(r['queued_ns'])<=int(r['at_ns']), r
 print('QUEUE COVERAGE: captured labelled queue/handoff boundaries:',len(boundaries))
else:
 print('::warning::Optional queue/handoff hooks were not observed on this kernel. Exact queue timings remain unavailable; core accept/epoll/read/request correlation is tested.')
health=get('/health');assert health['stats'].get('transport_invalid',0)==0
print('PASS: real kernel accept/epoll/read records joined to native request IDs; keepalive shares a connection; results unchanged.')
PY
cp "$task_dir/trace.log" build/transport/canary-trace.log
