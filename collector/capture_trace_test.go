package collector

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func traceControlFixture(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("native capture lease")
	}
	c, e := NewControl(filepath.Join(t.TempDir(), "control"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(c.Close)
	s := &Server{Control: c, workers: newWorkerMonitor("", "")}
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, p, _ := net.SplitHostPort(r.Host)
		port, _ := strconv.Atoi(p)
		s.handler(false, port).ServeHTTP(w, r)
	}))
	t.Cleanup(h.Close)
	return s, h
}
func TestUnifiedCaptureStopsBoth(t *testing.T) {
	for _, mode := range []string{"stop", "expiry", "failure"} {
		t.Run(mode, func(t *testing.T) {
			s, h := traceControlFixture(t)
			options := TraceOptions{MasterPID: 10, Duration: 10 * time.Second}
			if mode == "expiry" {
				options.Duration = time.Second
			}
			armed := make(chan struct{})
			done := make(chan error, 1)
			go func() {
				done <- runCaptureTrace(context.Background(), h.URL, options, 10000, io.Discard, io.Discard, func(ctx context.Context, _ TraceOptions, _ io.Writer, d io.Writer) error {
					fmt.Fprintln(d, "TRACE_READY")
					close(armed)
					if mode == "failure" {
						return fmt.Errorf("probe failed")
					}
					<-ctx.Done()
					return ctx.Err()
				})
			}()
			select {
			case <-armed:
			case <-time.After(3 * time.Second):
				t.Fatal("not armed")
			}
			if mode == "stop" {
				s.Control.Stop()
			}
			select {
			case e := <-done:
				if mode == "failure" && (e == nil || !strings.Contains(e.Error(), "probe failed")) {
					t.Fatal(e)
				}
				if mode != "failure" && e != nil {
					t.Fatal(e)
				}
			case <-time.After(4 * time.Second):
				t.Fatal("tracer did not stop")
			}
			if s.Control.Status().Active || s.traceSessionStatus(time.Now()).Active {
				t.Fatal("left capture active")
			}
		})
	}
}
func TestTransportLeaseExpiryAndPrivacy(t *testing.T) {
	s, h := traceControlFixture(t)
	seconds := 30.0
	s.Control.StartSample(&seconds, 10000)
	payload := fmt.Sprintf(`{"active":true,"ready":true,"expires_ms":%d,"master_pid":10}`, time.Now().Add(20*time.Second).UnixMilli())
	v, e := s.updateTraceSession([]byte(payload))
	if e != nil || !v.Active || !v.Ready {
		t.Fatal(v, e)
	}
	if s.traceSessionStatus(time.Now().Add(16 * time.Second)).Active {
		t.Fatal("stale lease remained active")
	}
	if _, e = s.updateTraceSession([]byte(strings.TrimSuffix(payload, "}") + `,"body":"private"}`)); e == nil {
		t.Fatal("accepted unknown metadata")
	}
	u, _ := url.Parse(h.URL)
	port, _ := strconv.Atoi(u.Port())
	r := httptest.NewRequest("POST", h.URL+"/api/transport/session", strings.NewReader(payload))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.handler(true, port).ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("private viewer accepted trace lease", w.Code)
	}
}
func TestRecordedJourneysOutliveRecentList(t *testing.T) {
	s, e := NewStore(filepath.Join(t.TempDir(), "events.db"), 4, 1000)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	batch := []Event{}
	for i := 0; i < 130; i++ {
		batch = append(batch, Event{"id": fmt.Sprintf("11-%d-2-3", i), "started_ms": time.Now().UnixMilli(), "path": "/test", "status": 200, "duration_ms": 1, "client": "local"})
	}
	if e = s.Insert(batch); e != nil {
		t.Fatal(e)
	}
	marker := traceFixture("request_begin", "11-0-2-3", "100", "200")
	marker.RequestNumber = 1
	if e = s.insertTransport([]TransportRecord{marker}); e != nil {
		t.Fatal(e)
	}
	recent, _ := s.Recent()
	if strings.Contains(string(recent[0]), `"11-0-2-3"`) {
		t.Fatal("fixture was not old")
	}
	recorded, e := s.RecordedJourneys()
	if e != nil || len(recorded) != 1 || !strings.Contains(string(recorded[0]), "11-0-2-3") {
		t.Fatal(recorded, e)
	}
}
