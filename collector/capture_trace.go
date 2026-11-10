package collector

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"time"
)

type traceReadyWriter struct {
	io.Writer
	ready atomic.Bool
}

func (w *traceReadyWriter) Write(p []byte) (int, error) {
	if strings.Contains(string(p), "TRACE_READY") {
		w.ready.Store(true)
	}
	return w.Writer.Write(p)
}

func RunCaptureTrace(ctx context.Context, base string, options TraceOptions, sample int, out, diagnostics io.Writer) error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("transport-enabled start requires separately authorized host privileges")
	}
	if !options.Deadline.IsZero() {
		options.Duration = time.Until(options.Deadline).Truncate(time.Second)
	}
	if options.Duration < time.Second || options.Duration > 2*time.Hour || options.Duration%time.Second != 0 {
		return fmt.Errorf("transport-enabled start requires --for 1s..2h in whole seconds")
	}
	if options.MasterPID <= 0 || !modulePathPattern.MatchString(options.Module) || options.UDPPort < 1 || options.UDPPort > 65535 || sample < 1 || sample > 10000 {
		return fmt.Errorf("invalid transport start options")
	}
	if err := verifyMarkerABI(options.Module); err != nil {
		return err
	}
	options.streamOnly = out == io.Discard
	return runCaptureTrace(ctx, base, options, sample, out, diagnostics, RunTrace)
}
func runCaptureTrace(ctx context.Context, base string, options TraceOptions, sample int, out, diagnostics io.Writer, trace func(context.Context, TraceOptions, io.Writer, io.Writer) error) error {
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "http" || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" {
		return fmt.Errorf("transport controls require a loopback HTTP collector URL")
	}
	client := &http.Client{Timeout: 2 * time.Second}
	request := func(path string, payload any, result any) error {
		var body io.Reader
		method := "GET"
		if payload != nil {
			b, _ := json.Marshal(payload)
			body = bytes.NewReader(b)
			method = "POST"
		}
		req, e := http.NewRequest(method, strings.TrimRight(base, "/")+path, body)
		if e != nil {
			return e
		}
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		r, e := client.Do(req)
		if e != nil {
			return e
		}
		defer r.Body.Close()
		if r.StatusCode != 200 {
			return fmt.Errorf("collector HTTP %d for %s", r.StatusCode, path)
		}
		if result == nil {
			_, e = io.Copy(io.Discard, io.LimitReader(r.Body, 4096))
			return e
		}
		return json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(result)
	}
	var current TraceSession
	if err := request("/api/transport/session", nil, &current); err != nil {
		return err
	}
	if current.Active {
		return fmt.Errorf("another transport session is active; stop it before starting again")
	}
	deadline := options.Deadline
	if deadline.IsZero() {
		deadline = time.Now().Add(options.Duration)
	}
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	expires := deadline.UnixMilli()
	if err := request("/api/capture/start", map[string]any{"seconds": time.Until(deadline).Seconds(), "sample_permyriad": sample}, nil); err != nil {
		return err
	}
	// Every exit stops both modes. No successful HTTP-only session is left behind
	// if BPF compilation, output, target generation, or the collector fails.
	defer request("/api/capture/stop", map[string]any{}, nil)
	defer request("/api/transport/session", map[string]any{"active": false, "ready": false, "expires_ms": int64(0), "master_pid": options.MasterPID}, nil)
	writer := &traceReadyWriter{Writer: diagnostics}
	heartbeat := func() error {
		return request("/api/transport/session", map[string]any{"active": true, "ready": writer.ready.Load(), "expires_ms": expires, "master_pid": options.MasterPID}, nil)
	}
	if err := heartbeat(); err != nil {
		return err
	}
	fmt.Fprintln(diagnostics, "Unified HTTP/transport session started; waiting for TRACE_READY; both stop at the requested deadline.")
	done := make(chan error, 1)
	go func() {
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				done <- nil
				return
			case <-tick.C:
				var capture CaptureState
				if e := request("/api/capture", nil, &capture); e != nil {
					cancel()
					done <- e
					return
				}
				if !capture.Active {
					cancel()
					done <- nil
					return
				}
				if e := heartbeat(); e != nil {
					cancel()
					done <- e
					return
				}
			}
		}
	}()
	traceErr := trace(ctx, options, out, writer)
	cancelled := ctx.Err() != nil
	cancel()
	monitorErr := <-done
	if monitorErr != nil {
		return monitorErr
	}
	if cancelled {
		return nil
	}
	return traceErr
}
