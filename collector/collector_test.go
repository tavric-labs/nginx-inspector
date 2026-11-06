package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func fixture() map[string]any {
	return map[string]any{"schema": 1, "id": "12-34-56", "started_ms": time.Now().UnixMilli(), "duration_ms": 3, "client": "127.0.0.1", "method": "POST", "path": "/example?secret=discard", "status": 200, "request_bytes": 5, "response_body_bytes": 10, "wire_bytes": "14", "connection_requests": 1, "rewrite_visits": 1, "worker_sent": 0, "worker_dropped": 0, "phases_ms": map[string]any{}, "request_headers": map[string]any{}, "response_headers": map[string]any{}, "upstream": map[string]any{}, "tls": map[string]any{}}
}
func encoded(v any) []byte { b, _ := json.Marshal(v); return b }
func TestValidationPrivacyAndTypes(t *testing.T) {
	v := fixture()
	event, e := Validate(encoded(v))
	if e != nil || event["path"] != "/example" {
		t.Fatal(event, e)
	}
	for _, key := range []string{"authorization", "body", "cookie"} {
		v := fixture()
		v[key] = "secret"
		if _, e := Validate(encoded(v)); e == nil {
			t.Fatal("accepted", key)
		}
	}
	for _, bad := range []any{true, -1, 1.5, "3"} {
		v := fixture()
		v["duration_ms"] = bad
		if _, e := Validate(encoded(v)); e == nil {
			t.Fatal("accepted invalid number", bad)
		}
	}
	if _, e := Validate(append(encoded(v), []byte(" {}")...)); e == nil {
		t.Fatal("accepted trailing JSON")
	}
	if _, e := Validate(make([]byte, MaxEventBytes+1)); e == nil {
		t.Fatal("accepted oversize")
	}
}
func nativeFixture() map[string]any {
	v := fixture()
	v["schema"] = 2
	n := map[string]any{}
	for k := range spec.Scalars {
		n[k] = map[string]any{}
	}
	for k := range spec.Arrays {
		n[k] = []any{}
	}
	n["trace"] = []any{map[string]any{"event": "log", "at_ms": 1, "subrequest": 0}}
	v["native"] = n
	return v
}
func TestNativeClosedContract(t *testing.T) {
	v := nativeFixture()
	if _, e := Validate(encoded(v)); e != nil {
		t.Fatal(e)
	}
	for section := range spec.Scalars {
		v := nativeFixture()
		n := v["native"].(map[string]any)
		n[section].(map[string]any)["authorization"] = "secret"
		if _, e := Validate(encoded(v)); e == nil {
			t.Fatal("unknown native field", section)
		}
	}
	for name, s := range spec.Arrays {
		v := nativeFixture()
		a := make([]any, s.Cap+1)
		for i := range a {
			a[i] = map[string]any{}
		}
		v["native"].(map[string]any)[name] = a
		if _, e := Validate(encoded(v)); e == nil {
			t.Fatal("accepted cap overflow", name)
		}
	}
	v = nativeFixture()
	v["native"].(map[string]any)["routing"] = map[string]any{"original_path": "/start?token=secret"}
	event, e := Validate(encoded(v))
	if e != nil || strings.Contains(string(encoded(event)), "secret") {
		t.Fatal(event, e)
	}
}
func TestSQLiteRetentionAndLegacyHistory(t *testing.T) {
	p := filepath.Join(t.TempDir(), "events.db")
	s, e := NewStore(p, 24, 2)
	if e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 3; i++ {
		v := fixture()
		v["id"] = fmt.Sprintf("1-%d", i)
		event, e := Validate(encoded(v))
		if e != nil {
			t.Fatal(e)
		}
		if e = s.Insert([]Event{event, event}); e != nil {
			t.Fatal(e)
		}
	}
	rows, e := s.Recent()
	if e != nil || len(rows) != 2 {
		t.Fatal(len(rows), e)
	}
	summary, e := s.Summary()
	if e != nil || summary["retained"] != 2 {
		t.Fatal(summary, e)
	}
	s.Close()
	s, e = NewStore(p, 24, 2)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if _, e = s.Get("1-2"); e != nil {
		t.Fatal(e)
	}
}
func TestControlExpiryAndOffOnRestart(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux native lease")
	}
	p := filepath.Join(t.TempDir(), "control")
	c, e := NewControl(p)
	if e != nil {
		t.Fatal(e)
	}
	if c.Status().Active {
		t.Fatal("default on")
	}
	n := .05
	if _, e = c.Start(&n); e != nil {
		t.Fatal(e)
	}
	time.Sleep(70 * time.Millisecond)
	if c.Status().Active {
		t.Fatal("expiry failed")
	}
	c.Start(nil)
	c.Close()
	c, e = NewControl(p)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	if c.Status().Active {
		t.Fatal("restart enabled capture")
	}
}
func TestReadOnlyListenerAndControlBoundary(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux native lease")
	}
	c, e := NewControl(filepath.Join(t.TempDir(), "control"))
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	s := &Server{Control: c, workers: newWorkerMonitor("", "")}
	for _, tc := range []struct {
		readonly           bool
		host, origin, body string
		code               int
	}{{true, "127.0.0.1:19877", "", "{}", 403}, {false, "evil.example", "", "{}", 403}, {false, "127.0.0.1:19877", "http://evil.example", "{}", 403}, {false, "127.0.0.1:19877", "", "{\"seconds\":true}", 400}, {false, "127.0.0.1:19877", "", "{\"seconds\":0}", 400}, {false, "127.0.0.1:19877", "", "{}", 200}} {
		r := httptest.NewRequest("POST", "http://127.0.0.1:19877/api/capture/start", strings.NewReader(tc.body))
		r.Host = tc.host
		r.Header.Set("Origin", tc.origin)
		r.Header.Set("Content-Type", "application/json; charset=utf-8")
		w := httptest.NewRecorder()
		s.handler(tc.readonly, 19877).ServeHTTP(w, r)
		if w.Code != tc.code {
			t.Fatal(tc, w.Code, w.Body.String())
		}
	}
}
func TestUDPToHistoryAndShutdown(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux native lease")
	}
	d := t.TempDir()
	s, e := NewServer(Config{DB: filepath.Join(d, "db"), ControlFile: filepath.Join(d, "control"), RetentionHours: 24, MaxEvents: 100, HTTPPort: 0, UDPPort: 0})
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	defer func() {
		cancel()
		if e := <-done; e != nil {
			t.Error(e)
		}
	}()
	s.Control.Start(nil)
	conn, e := net.DialUDP("udp4", nil, s.udp.LocalAddr().(*net.UDPAddr))
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	conn.Write(encoded(fixture()))
	url := "http://" + s.listeners[0].Addr().String()
	for i := 0; i < 100; i++ {
		r, e := http.Get(url + "/api/events")
		if e != nil {
			t.Fatal(e)
		}
		b, _ := io.ReadAll(r.Body)
		r.Body.Close()
		if strings.Contains(string(b), "12-34-56") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("event not persisted")
}
