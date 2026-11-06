#!/bin/sh
# Build with Ubuntu's patched nginx headers, inside a disposable container.
set -eu
export DEBIAN_FRONTEND=noninteractive
if [ ! -f /usr/share/nginx/src/configure ]; then
    apt-get update
    apt-get install -y --no-install-recommends nginx-dev nginx python3 build-essential ca-certificates
fi
mkdir -p /workspace/build
if [ ! -d /workspace/build/nginx-src ]; then
    cp -a /usr/share/nginx/src /workspace/build/nginx-src
fi
cd /workspace/build/nginx-src
./configure --with-compat --with-debug --with-http_ssl_module --with-http_v2_module --with-http_stub_status_module --with-http_auth_request_module --with-threads --add-dynamic-module=/workspace/module
make -f objs/Makefile modules -j2
cp objs/ngx_http_inspector_module.so /workspace/build/
nginx -V > /workspace/build/nginx-version.txt 2>&1
mkdir -p /opt/inspector /opt/nginx/sbin /data
cp objs/ngx_http_inspector_module.so /opt/inspector/
ln -sf /usr/sbin/nginx /opt/nginx/sbin/nginx
mkdir -p /app
ln -sfn /workspace/collector /app/collector
ln -sfn /workspace/dev /app/dev
cp /workspace/build/nginx-inspector /app/nginx-inspector
cd /app
exec python3 dev/run.py
