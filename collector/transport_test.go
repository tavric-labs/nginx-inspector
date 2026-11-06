package collector

import (
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func traceFixture(event, id, gen, at string) TransportRecord {
	return TransportRecord{Schema: 1, Kind: "transport", Session: strings.Repeat("a", 32), Master: 10, HostPID: 11, FD: 9, Generation: gen, At: at, Event: event, RequestID: id}
}
func TestTransportClosedContract(t *testing.T) {
	good := traceFixture("request_begin", "11-1-2-3", "100", "200")
	good.RequestNumber = 1
	if _, e := ValidateTransport(encoded(good)); e != nil {
		t.Fatal(e)
	}
	for _, raw := range [][]byte{[]byte(strings.Replace(string(encoded(good)), `"kind":"transport"`, `"kind":"transport","body":"secret"`, 1)), append(encoded(good), []byte(` {}`)...), make([]byte, 1537)} {
		if _, e := ValidateTransport(raw); e == nil {
			t.Fatal("accepted private, trailing or oversized payload")
		}
	}
	for _, mutate := range []func(*TransportRecord){func(v *TransportRecord) { v.Generation = "201" }, func(v *TransportRecord) { v.Event = "packet_payload" }, func(v *TransportRecord) { v.Session = "../../secret" }, func(v *TransportRecord) { v.At = "NaN" }, func(v *TransportRecord) { v.Event = "read" }} {
		v := good
		mutate(&v)
		if _, e := ValidateTransport(encoded(v)); e == nil {
			t.Fatal("invalid record accepted", v)
		}
	}
}
func TestTransportGenerationSessionAndSharedConnection(t *testing.T) {
	s, e := NewStore(filepath.Join(t.TempDir(), "trace.sqlite3"), 4, 100)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	begin := traceFixture("request_begin", "11-1-7-1000", "100", "200")
	begin.RequestNumber = 1
	second := traceFixture("request_begin", "11-2-7-1001", "100", "300")
	second.RequestNumber = 2
	reusedFD := traceFixture("accept", "", "400", "400")
	// Reordered UDP delivery: later generation and other markers can arrive first.
	if e = s.insertTransport([]TransportRecord{reusedFD, second, traceFixture("accept", "", "100", "100"), begin}); e != nil {
		t.Fatal(e)
	}
	got, e := s.Transport(begin.RequestID)
	if e != nil || got.Status != "correlated_connection" || !got.Shared || got.Begin == nil || len(got.Records) != 3 {
		t.Fatal(got, e)
	}
	for _, record := range got.Records {
		if record.Generation != "100" {
			t.Fatal("FD reuse crossed connection generations")
		}
	}
	conflict := begin
	conflict.Session = strings.Repeat("b", 32)
	if e = s.insertTransport([]TransportRecord{conflict}); e != nil {
		t.Fatal(e)
	}
	got, e = s.Transport(begin.RequestID)
	if e != nil || got.Status != "ambiguous" || len(got.Records) != 0 {
		t.Fatal("ambiguous session was guessed", got, e)
	}
	got, e = s.Transport("missing")
	if e != nil || got.Status != "not_captured" {
		t.Fatal(got, e)
	}
}
func TestTransportRetentionAndTruncatedMarkerAnchor(t *testing.T) {
	s, e := NewStore(filepath.Join(t.TempDir(), "trace.sqlite3"), 1, 100)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	begin := traceFixture("request_begin", "11-1-7-1000", "100", "1000")
	begin.RequestNumber = 1
	batch := []TransportRecord{}
	for i := 0; i < 270; i++ {
		batch = append(batch, traceFixture("read", "", "100", fmt.Sprint(101+i)))
	}
	batch = append(batch, begin)
	if e = s.insertTransport(batch); e != nil {
		t.Fatal(e)
	}
	v, e := s.Transport(begin.RequestID)
	if e != nil || !v.Truncated || v.Begin == nil || v.Begin.RequestID != begin.RequestID || len(v.Records) != 256 {
		t.Fatal(v, e)
	}
	s.db.Exec("UPDATE transport_records SET created_ms=?", time.Now().Add(-2*time.Hour).UnixMilli())
	if e = s.insertTransport([]TransportRecord{traceFixture("read", "", "2000", "2000")}); e != nil {
		t.Fatal(e)
	}
	v, e = s.Transport(begin.RequestID)
	if e != nil || v.Status != "not_captured" {
		t.Fatal(v, e)
	}
}
func TestTraceProgramUsesStableMarkersAndNoPayloadReads(t *testing.T) {
	script := traceProgram(TraceOptions{MasterPID: 10, Module: "/tmp/inspector.so", Duration: time.Minute}, []int{11, 12}, []uint64{123}, strings.Repeat("a", 32))
	for _, want := range []string{"ngx_inspector_request_begin", "ngx_inspector_request_end", "pid == 11", "pid == 12", "$ino == 123", "interval:s:60", `printf("{\"schema\":1`, "@datagen", "inet_csk_reqsk_queue_drop", "TRACE_READY"} {
		if !strings.Contains(script, want) {
			t.Fatal("missing", want)
		}
	}
	for _, bad := range []string{"args->buf", "skb->data", "comm==", "system(", "\n+kprobe", `\\\"schema`} {
		if strings.Contains(script, bad) {
			t.Fatal("unsafe or malformed trace", bad)
		}
	}
}

func TestReplayEmbeddingPolicy(t *testing.T) {
	s := &Server{}
	for _, path := range []string{"/", "/journey", "/replay/detail", "/replay/detail?embed=1"} {
		request := httptest.NewRequest("GET", "http://127.0.0.1:19877"+path, nil)
		response := httptest.NewRecorder()
		s.handler(false, 19877).ServeHTTP(response, request)
		if response.Code != 200 {
			t.Fatal(path, response.Code)
		}
		policy := response.Header().Get("Content-Security-Policy")
		want := "frame-ancestors 'none'"
		if strings.Contains(path, "embed=1") {
			want = "frame-ancestors 'self'"
		}
		if !strings.Contains(policy, want) || !strings.Contains(policy, "frame-src 'self'") {
			t.Fatal(path, policy)
		}
	}
}
