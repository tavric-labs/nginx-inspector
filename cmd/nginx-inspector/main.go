package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/tavric-labs/nginx-inspector/collector"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
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
func printJSON(v any) error {
	var pretty bytes.Buffer
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if err := json.Indent(&pretty, b, "", "  "); err != nil {
		return err
	}
	fmt.Println(pretty.String())
	return nil
}
func run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: nginx-inspector serve|setup|reload|doctor|start [--all] [--transport] [--for 10m]|stop|status|top|workers|health|version")
	}
	if args[0] == "version" || args[0] == "--version" {
		fmt.Printf("nginx-inspector %s (%s) %s/%s\n", version, commit, runtime.GOOS, runtime.GOARCH)
		return nil
	}
	f := flag.NewFlagSet(args[0], flag.ContinueOnError)
	switch args[0] {
	case "trace":
		o := collector.TraceOptions{}
		f.IntVar(&o.MasterPID, "master-pid", 0, "verified host nginx master PID")
		f.StringVar(&o.Module, "module", "", "absolute path to marker-enabled nginx module")
		f.DurationVar(&o.Duration, "for", 60*time.Second, "bounded trace duration, 1s..2h")
		f.IntVar(&o.UDPPort, "udp-port", 19876, "loopback collector transport port")
		nginxBin := f.String("nginx-bin", "nginx", "nginx binary for auto-detecting master PID and module")
		nginxConf := f.String("nginx-conf", "", "nginx.conf path for auto-detecting module")
		nginxPID := f.String("nginx-pid-file", "", "nginx master PID file")
		useSudo := f.Bool("sudo", false, "request sudo automatically when not running as root")
		if e := f.Parse(args[1:]); e != nil {
			return e
		}
		if *useSudo && os.Geteuid() != 0 && os.Getenv("NGINX_INSPECTOR_SUDO_ESCALATED") == "" {
			return escalateWithSudo(args, "Kernel transport tracing requires root privileges; requesting sudo")
		}
		if o.MasterPID == 0 {
			if pid, err := collector.DiscoverNginxMasterPID(*nginxPID, *nginxBin); err == nil {
				o.MasterPID = pid
			}
		}
		if o.Module == "" {
			if mod, err := collector.DiscoverNginxModuleSO(*nginxConf, *nginxBin, o.MasterPID); err == nil {
				o.Module = mod
			}
		}
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		return collector.RunTrace(ctx, o, os.Stdout, os.Stderr)
	case "serve":
		c := collector.Config{}
		f.StringVar(&c.DB, "db", "inspector.sqlite3", "SQLite history file")
		f.StringVar(&c.ControlFile, "control-file", "", "shared nginx lease file")
		f.StringVar(&c.ProcRoot, "proc-root", "", "optional Linux proc filesystem for worker CPU/RSS sampling")
		f.StringVar(&c.NginxPIDFile, "nginx-pid-file", "", "scope worker process sampling to this master PID file")
		f.StringVar(&c.WorkerDiagnosticsFile, "worker-diagnostics", "", "optional sanitized, dated worker diagnostics JSON; read once at startup")
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
		if e := os.MkdirAll(filepath.Dir(c.DB), 0700); e != nil {
			return e
		}
		controlMode := os.FileMode(0700)
		if filepath.Clean(filepath.Dir(c.ControlFile)) != filepath.Clean(filepath.Dir(c.DB)) {
			controlMode = 0755
		}
		if e := os.MkdirAll(filepath.Dir(c.ControlFile), controlMode); e != nil {
			return e
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
	case "setup", "reload":
		opts := collector.SetupOptions{}
		f.StringVar(&opts.NginxBin, "nginx-bin", "nginx", "nginx binary")
		f.StringVar(&opts.NginxConf, "nginx-conf", "", "nginx.conf path (defaults to nginx -V conf-path)")
		f.StringVar(&opts.ControlFile, "control-file", "/var/lib/nginx-inspector/control", "shared 8-byte control file path")
		f.StringVar(&opts.DBFile, "db", "/var/lib/inspector-data/events.sqlite3", "collector SQLite database path")
		f.StringVar(&opts.ModuleSO, "module-so", "", "prebuilt ngx_http_inspector module .so path")
		f.StringVar(&opts.ModuleSource, "module-source", "", "path to C module source directory")
		f.StringVar(&opts.NginxSource, "nginx-source", "", "path to matching nginx source tree")
		f.StringVar(&opts.ModulesDir, "modules-dir", "", "directory for installed versioned .so")
		f.StringVar(&opts.Generation, "generation", "", "optional module symbol generation name")
		f.StringVar(&opts.CollectorUser, "collector-user", "", "unprivileged user that runs nginx-inspector serve")
		f.IntVar(&opts.UDPPort, "udp-port", 19876, "loopback UDP event port")
		f.IntVar(&opts.MaxSample, "max-sample", 10000, "nginx inspector_sample upper bound (1..10000)")
		f.BoolVar(&opts.BuildModule, "build-module", false, "compile a fresh module generation from source before reload")
		f.BoolVar(&opts.DryRun, "dry-run", false, "show planned changes and required privileges without modifying host")
		f.BoolVar(&opts.SkipReload, "skip-reload", false, "validate with nginx -t without reloading nginx")
		useSudo := f.Bool("sudo", true, "request sudo automatically when host paths require root")
		traceAll := f.Bool("all", false, "immediately start a 100% full capture session after reload")
		sample := f.Float64("sample", 0, "immediately start a capture session at this percentage (0.01..100) after reload")
		duration := f.String("for", "", "duration for post-reload capture session, e.g. 30s or 10m")
		url := f.String("url", "http://127.0.0.1:19877", "collector URL for post-reload capture")
		if e := f.Parse(args[1:]); e != nil {
			return e
		}
		res, err := collector.SetupOrReload(opts)
		if err != nil {
			if !opts.DryRun && *useSudo && os.Geteuid() != 0 && os.Getenv("NGINX_INSPECTOR_SUDO_ESCALATED") == "" && isPermissionError(err) {
				return escalateWithSudo(args, "Host privileges required to install nginx module, validate nginx -t, and reload nginx; requesting sudo")
			}
			return err
		}
		if !opts.DryRun && (*traceAll || *sample > 0 || *duration != "") {
			pct := *sample
			if *traceAll || pct == 0 {
				pct = 100
			}
			if err := startCaptureSession(*url, pct, *duration); err != nil {
				res.Commands["capture_notice"] = fmt.Sprintf("collector capture not started automatically (%v); run: %s", err, res.Commands["trace_all_30s"])
			} else {
				res.Commands["capture_started"] = fmt.Sprintf("active at %.2f%% sample", pct)
			}
		}
		return printJSON(res)
	case "doctor":
		opts := collector.DoctorOptions{}
		f.StringVar(&opts.NginxBin, "nginx-bin", "nginx", "nginx binary")
		f.StringVar(&opts.NginxConf, "nginx-conf", "", "nginx.conf path")
		f.StringVar(&opts.ControlFile, "control-file", "", "shared control file path")
		f.StringVar(&opts.NginxPIDFile, "nginx-pid-file", "", "nginx master PID file")
		f.StringVar(&opts.ProcRoot, "proc-root", "", "Linux proc filesystem root")
		f.StringVar(&opts.CollectorURL, "url", "http://127.0.0.1:19877", "collector URL")
		if e := f.Parse(args[1:]); e != nil {
			return e
		}
		return printJSON(collector.Doctor(opts))
	case "start", "stop", "status", "top", "health", "workers":
		url := f.String("url", "http://127.0.0.1:19877", "collector URL")
		sample := f.Float64("sample", 1, "capture percentage, 0.01..100; default 1")
		traceAll := f.Bool("all", false, "capture 100% of eligible requests (alias for --sample 100)")
		duration := f.String("for", "", "capture duration, e.g. 10m or 1h")
		once := f.Bool("once", false, "print once")
		until := f.String("until", "", "absolute RFC3339 stop time for transport-enabled start")
		transport := f.Bool("transport", false, "start HTTP and kernel tracing together (host privileges required)")
		master := f.Int("master-pid", 0, "host nginx master for transport-enabled start")
		module := f.String("module", "", "marker-enabled module for transport-enabled start")
		nginxBin := f.String("nginx-bin", "nginx", "nginx binary for auto-detecting master PID and module")
		nginxConf := f.String("nginx-conf", "", "nginx.conf path for auto-detecting module")
		nginxPID := f.String("nginx-pid-file", "", "nginx master PID file")
		useSudo := f.Bool("sudo", false, "request sudo automatically for --transport when not root")
		udp := f.Int("udp-port", 19876, "loopback transport ingestion port")
		if e := f.Parse(args[1:]); e != nil {
			return e
		}
		sampleSet := false
		f.Visit(func(fl *flag.Flag) {
			if fl.Name == "sample" {
				sampleSet = true
			}
		})
		if *traceAll {
			if args[0] != "start" {
				return fmt.Errorf("--all requires start")
			}
			if sampleSet && *sample != 100 {
				return fmt.Errorf("cannot combine --all with a different --sample value")
			}
			*sample = 100
		}
		if *transport {
			if args[0] != "start" {
				return fmt.Errorf("--transport requires start")
			}
			if *useSudo && os.Geteuid() != 0 && os.Getenv("NGINX_INSPECTOR_SUDO_ESCALATED") == "" {
				return escalateWithSudo(args, "Correlated HTTP + kernel transport tracing requires root privileges; requesting sudo")
			}
			var d time.Duration
			var deadline time.Time
			var e error
			if *until != "" {
				if *duration != "" {
					return fmt.Errorf("choose --for or --until")
				}
				deadline, e = time.Parse(time.RFC3339Nano, *until)
				if e != nil {
					return fmt.Errorf("--until requires an RFC3339 time")
				}
				d = time.Until(deadline).Truncate(time.Second)
			} else {
				d, e = time.ParseDuration(*duration)
				if e != nil {
					return fmt.Errorf("--transport requires --for or --until")
				}
			}

			if math.IsNaN(*sample) || math.IsInf(*sample, 0) || *sample < .01 || *sample > 100 {
				return fmt.Errorf("sample must be 0.01..100 percent")
			}
			if *master == 0 {
				if pid, err := collector.DiscoverNginxMasterPID(*nginxPID, *nginxBin); err == nil {
					*master = pid
				}
			}
			if *module == "" {
				if mod, err := collector.DiscoverNginxModuleSO(*nginxConf, *nginxBin, *master); err == nil {
					*module = mod
				}
			}
			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer cancel()
			return collector.RunCaptureTrace(ctx, *url, collector.TraceOptions{MasterPID: *master, Module: *module, Duration: d, Deadline: deadline, UDPPort: *udp}, int(math.Round(*sample*100)), io.Discard, os.Stderr)
		}
		if *until != "" {
			return fmt.Errorf("--until requires start --transport")
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
			if args[0] == "status" {
				var httpState any
				if e = json.Unmarshal(data, &httpState); e != nil {
					return e
				}
				tr, e := client.Get(strings.TrimRight(*url, "/") + "/api/transport/session")
				if e != nil {
					return e
				}
				var transportState any
				e = json.NewDecoder(io.LimitReader(tr.Body, 4096)).Decode(&transportState)
				tr.Body.Close()
				if e != nil {
					return e
				}
				if tr.StatusCode != 200 {
					return fmt.Errorf("transport status HTTP %d", tr.StatusCode)
				}
				data, _ = json.Marshal(map[string]any{"http": httpState, "transport": transportState})
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

func isPermissionError(err error) bool {
	if errors.Is(err, os.ErrPermission) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "permission denied") || strings.Contains(msg, "operation not permitted")
}

func escalateWithSudo(args []string, reason string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	sudoPath, err := exec.LookPath("sudo")
	if err != nil {
		return fmt.Errorf("root privileges required and sudo was not found: %w", err)
	}
	fmt.Fprintln(os.Stderr, reason)
	sudoArgs := append([]string{"NGINX_INSPECTOR_SUDO_ESCALATED=1", exe}, args...)
	cmd := exec.Command(sudoPath, sudoArgs...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func startCaptureSession(baseURL string, samplePct float64, duration string) error {
	if math.IsNaN(samplePct) || math.IsInf(samplePct, 0) || samplePct < .01 || samplePct > 100 {
		return fmt.Errorf("sample must be 0.01..100 percent")
	}
	payload := map[string]any{"sample_permyriad": int(math.Round(samplePct * 100))}
	if duration != "" {
		d, err := time.ParseDuration(duration)
		if err != nil || d <= 0 || d > 24*time.Hour {
			return fmt.Errorf("duration must be positive and at most 24h")
		}
		payload["seconds"] = d.Seconds()
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	client := http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequest("POST", strings.TrimRight(baseURL, "/")+"/api/capture/start", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return nil
}
