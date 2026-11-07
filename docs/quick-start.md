# First capture

After [installation](/install), start the collector:

```sh
nginx-inspector serve --db ./data/events.sqlite3 --control-file ./control/control
```

The nginx `inspector_control` directive must point to that same control file.
In another terminal:

```sh
nginx-inspector status
nginx-inspector start --for 10m
```

The default is 1% sampling. For a short deliberate full trace of every eligible request,
use `nginx-inspector start --all --for 30s` (or `--sample 100`), or use the
**100% · Trace all** and **Start capture** controls in the header at
**http://127.0.0.1:19877/**.

Send traffic through your existing nginx listener. Open **http://127.0.0.1:19877/**,
select a request, and press **Inspect**. Start with the routing card and hook
sequence; then inspect upstream attempts, policies and buffers.

```sh
nginx-inspector stop
nginx-inspector top --once
```

Stop leaves the viewer and history available. Capture also stops automatically
when the time window expires. An in-flight request is only retained if its log
hook runs while the capture gate remains active.

For a remote collector, tunnel its loopback interface:

```sh
ssh -N -L 19877:127.0.0.1:19877 your-nginx-host
nginx-inspector start --for 1h --url http://127.0.0.1:19877
```
