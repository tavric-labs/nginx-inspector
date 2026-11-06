FROM ubuntu:24.04 AS build
ARG NGINX_VERSION=1.30.6
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl build-essential libpcre2-dev zlib1g-dev libssl-dev && rm -rf /var/lib/apt/lists/*
WORKDIR /src
RUN curl -fsSLO https://nginx.org/download/nginx-${NGINX_VERSION}.tar.gz && tar xzf nginx-${NGINX_VERSION}.tar.gz
COPY module /src/module
RUN cd nginx-${NGINX_VERSION} && ./configure --prefix=/opt/nginx --with-compat --with-http_ssl_module --add-dynamic-module=/src/module && make -j2 && make install && mkdir /opt/inspector && cp objs/ngx_http_inspector_module.so /opt/inspector/
FROM ubuntu:24.04
RUN apt-get update && apt-get install -y --no-install-recommends python3 libpcre2-8-0 zlib1g libssl3t64 ca-certificates && rm -rf /var/lib/apt/lists/* && useradd -u 10001 -m inspector
COPY --from=build /opt/nginx /opt/nginx
COPY --from=build /opt/inspector /opt/inspector
COPY collector /app/collector
COPY dev /app/dev
RUN mkdir /data && chown 10001:10001 /data && chmod 700 /data
USER 10001:10001
WORKDIR /app
ENV PYTHONUNBUFFERED=1
EXPOSE 18080 19877
ENTRYPOINT ["python3", "dev/run.py"]
