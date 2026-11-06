package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/tavric-labs/nginx-inspector/collector"
	"io"
	"math"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

var version = "dev"
var commit = "unknown"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "nginx-inspector:", err)
		os.Exit(1)
	}
}
func run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: nginx-inspector serve|start [--for 10m]|stop|status|top|workers|health|version")
	}
	if args[0] == "version" || args[0] == "--version" {
		fmt.Printf("nginx-inspector %s (%s) %s/%s\n", version, commit, runtime.GOOS, runtime.GOARCH)
		return nil
	}
	f := flag.NewFlagSet(args[0], flag.ContinueOnError)
	switch args[0] {
	case "serve":
		c := collector.Config{}
		f.StringVar(&c.DB, "db", "inspector.sqlite3", "SQLite history file")
		f.StringVar(&c.ControlFile, "control-file", "", "shared nginx lease file")
		f.StringVar(&c.ProcRoot, "proc-root", "", "optional Linux proc filesystem for worker CPU/RSS sampling")
		f.StringVar(&c.NginxPIDFile, "nginx-pid-file", "", "scope worker process sampling to this master PID file")
		f.StringVar(&c.ViewerHost, "viewer-host", "", "optional specific local IPv4 address for a read-only viewer")
		f.IntVar(&c.UDPPort, "udp-port", 19876, "loopback UDP event port")
		f.IntVar(&c.HTTPPort, "http-port", 19877, "loopback HTTP/control port")
		f.IntVar(&c.RetentionHours, "retention-hours", 24, "retention in hours")
		f.IntVar(&c.MaxEvents, "max-events", 100000, "retained event limit")
		if e := f.Parse(args[1:]); e != nil {
			return e
		}
		if c.RetentionHours < 1 || c.RetentionHours > 87600 || c.MaxEvents < 1 || c.MaxEvents > 10000000 {
			return fmt.Errorf("invalid retention limits")
		}
		if c.NginxPIDFile != "" && c.ProcRoot == "" {
			return fmt.Errorf("--nginx-pid-file requires --proc-root")
		}
		if c.ControlFile == "" {
			c.ControlFile = c.DB + ".control"
		}
		for _, p := range []string{c.DB, c.ControlFile} {
			if e := os.MkdirAll(filepath.Dir(p), 0700); e != nil {
				return e
			}
		}
		if _, e := os.Stat(c.DB); os.IsNotExist(e) {
			file, e := os.OpenFile(c.DB, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if e != nil {
				return e
			}
			file.Close()
		}
		s, e := collector.NewServer(c)
		if e != nil {
			return e
		}
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		fmt.Fprintln(os.Stderr, "Collector ready; capture OFF; controls on loopback")
		return s.Run(ctx)
	case "start", "stop", "status", "top", "health", "workers":
		url := f.String("url", "http://127.0.0.1:19877", "collector URL")
		sample := f.Float64("sample", 1, "capture percentage, 0.01..100; default 1")
		duration := f.String("for", "", "capture duration, e.g. 10m or 1h")
		once := f.Bool("once", false, "print once")
		if e := f.Parse(args[1:]); e != nil {
			return e
		}
		path, method := "/api/capture", "GET"
		var body []byte
		if args[0] == "start" || args[0] == "stop" {
			method = "POST"
			path += "/" + args[0]
			body = []byte("{}")
			payload := map[string]any{}
			if args[0] == "start" {
				if math.IsNaN(*sample) || math.IsInf(*sample, 0) || *sample < .01 || *sample > 100 {
					return fmt.Errorf("sample must be 0.01..100 percent")
				}
				payload["sample_permyriad"] = int(math.Round(*sample * 100))
			}
			if *duration != "" {
				if args[0] != "start" {
					return fmt.Errorf("--for requires start")
				}
				d, e := time.ParseDuration(*duration)
				if e != nil || d <= 0 || d > 24*time.Hour {
					return fmt.Errorf("duration must be positive and at most 24h")
				}
				payload["seconds"] = d.Seconds()
			}
			body, _ = json.Marshal(payload)
		}
		if args[0] == "top" {
			path = "/api/summary"
		}
		if args[0] == "workers" {
			path = "/api/workers"
		}
		if args[0] == "health" {
			path = "/health"
		}
		client := http.Client{Timeout: 8 * time.Second}
		sigCtx, stopSig := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stopSig()
		for {
			req, e := http.NewRequestWithContext(sigCtx, method, strings.TrimRight(*url, "/")+path, bytes.NewReader(body))
			if e != nil {
				return e
			}
			if body != nil {
				req.Header.Set("Content-Type", "application/json")
			}
			r, e := client.Do(req)
			if e != nil {
				if sigCtx.Err() != nil {
					return nil
				}
				return e
			}
			data, e := io.ReadAll(io.LimitReader(r.Body, 2*1024*1024))
			r.Body.Close()
			if e != nil {
				return e
			}
			if r.StatusCode != 200 {
				return fmt.Errorf("HTTP %d: %s", r.StatusCode, data)
			}
			var pretty bytes.Buffer
			if e = json.Indent(&pretty, data, "", "  "); e != nil {
				return e
			}
			fmt.Println(pretty.String())
			if (args[0] != "top" && args[0] != "workers") || *once {
				return nil
			}
			select {
			case <-sigCtx.Done():
				return nil
			case <-time.After(2 * time.Second):
			}
		}
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}
