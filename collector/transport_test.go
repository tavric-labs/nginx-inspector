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

func TestDeepConnectionMetadata(t *testing.T) {
	cpu := 0
	cases := []TransportRecord{
		{Event: "read_error", Operation: "recvfrom", Errno: 11, Duration: "10"},
		{Event: "write", Operation: "writev", Bytes: 1024, Duration: "20"},
		{Event: "write_error", Operation: "sendfile", Errno: 11, Duration: "30"},
		{Event: "network_rx", CPU: &cpu, Duration: "40"},
		{Event: "socket", Socket: &SocketObservation{Point: "recvmsg_return", State: 1, ReceiveMemory: 0, SendQueuedMemory: 0, CongestionWindow: 10}},
		{Event: "tcp_state", FromState: 1, ToState: 8},
		{Event: "tcp_retransmit"},
	}
	for _, v := range cases {
		base := traceFixture(v.Event, "", "100", "200")
		base.Operation = v.Operation
		base.Errno = v.Errno
		base.Duration = v.Duration
		base.Bytes = v.Bytes
		base.CPU = v.CPU
		base.Socket = v.Socket
		base.FromState = v.FromState
		base.ToState = v.ToState
		if _, e := ValidateTransport(encoded(base)); e != nil {
			t.Fatalf("%s: %v", v.Event, e)
		}
		base.RequestID = "11-1-2-3"
		base.RequestNumber = 1
		if _, e := ValidateTransport(encoded(base)); e == nil {
			t.Fatal("assigned shared kernel work to a request", v.Event)
		}
	}
	invalid := []TransportRecord{
		{Event: "write", Operation: "recvfrom", Duration: "1"},
		{Event: "read_error", Operation: "read", Duration: "1"},
		{Event: "network_rx", Duration: "1"},
		{Event: "socket", Socket: &SocketObservation{Point: "payload"}},
		{Event: "socket", Socket: &SocketObservation{Point: "sendmsg_return", ReceiveMemory: 1 << 45}},
		{Event: "tcp_state", FromState: 33, ToState: 1},
	}
	for _, v := range invalid {
		base := traceFixture(v.Event, "", "100", "200")
		base.Operation = v.Operation
		base.Errno = v.Errno
		base.Duration = v.Duration
		base.Socket = v.Socket
		base.FromState = v.FromState
		base.ToState = v.ToState
		if _, e := ValidateTransport(encoded(base)); e == nil {
			t.Fatal("invalid deep metadata accepted", v)
		}
	}
	script := traceProgram(TraceOptions{MasterPID: 10, Module: "/tmp/inspector.so", Duration: time.Minute}, []int{11}, []uint64{123}, strings.Repeat("a", 32))
	for _, want := range []string{"tcp_rcv_established", "tcp_recvmsg", "tcp_sendmsg", "sys_enter_sendfile*", "read_error", "write_error", "sk_backlog.rmem_alloc.counter", "tcp:tcp_retransmit_skb", "sock:inet_sock_set_state", "clear(@rxgen)", "delete(@skpid[(uint64)arg0])"} {
		if !strings.Contains(script, want) {
			t.Fatal("missing deep probe or cleanup", want)
		}
	}
	for _, bad := range []string{"args->buf", "args->iov", "skb->data", "%p", "daddr", "saddr"} {
		if strings.Contains(script, bad) {
			t.Fatal("private packet/buffer/endpoint access", bad)
		}
	}
}

func TestReceiveCPUAndResidenceBounds(t *testing.T) {
	cpu := 0
	v := traceFixture("network_rx", "", "100", "200")
	v.CPU = &cpu
	v.Duration = "50"
	v.OnCPU = "30"
	v.OffCPU = "10"
	v.IRQ = "5"
	if _, e := ValidateTransport(encoded(v)); e != nil {
		t.Fatal(e)
	}
	read := v
	read.Event = "kernel_read"
	read.KernelFunction = "tcp_recvmsg"
	if _, e := ValidateTransport(encoded(read)); e != nil {
		t.Fatal(e)
	}
	early := traceFixture("buffer_residence", "", "250", "300")
	early.Bytes = 16
	early.MatchedRanges = 1
	early.ResidenceMin = "0"
	early.ResidenceMax = "100"
	if _, e := ValidateTransport(encoded(early)); e != nil {
		t.Fatal("pre-accept queue time must remain valid", e)
	}
	v.IRQ = "20"
	if _, e := ValidateTransport(encoded(v)); e == nil {
		t.Fatal("CPU components exceeded elapsed callback")
	}
	v.IRQ = ""
	if _, e := ValidateTransport(encoded(v)); e == nil {
		t.Fatal("accepted incomplete CPU accounting")
	}
	q := traceFixture("buffer_residence", "", "100", "300")
	q.Bytes = 64
	q.MatchedRanges = 2
	q.ResidenceMin = "0"
	q.ResidenceMax = "100"
	if _, e := ValidateTransport(encoded(q)); e != nil {
		t.Fatal(e)
	}
	for _, mutate := range []func(*TransportRecord){func(v *TransportRecord) { v.ResidenceMin = "101" }, func(v *TransportRecord) { v.ResidenceMax = "301" }, func(v *TransportRecord) { v.MatchedRanges = 9 }, func(v *TransportRecord) { v.Bytes = 0 }, func(v *TransportRecord) { v.ResidenceMax = "-1" }, func(v *TransportRecord) { v.RequestID = "11-1-2-3"; v.RequestNumber = 1 }} {
		bad := q
		mutate(&bad)
		if _, e := ValidateTransport(encoded(bad)); e == nil {
			t.Fatal("invalid residence accepted", bad)
		}
	}
	script := traceProgram(TraceOptions{MasterPID: 10, Module: "/tmp/inspector.so", Duration: time.Minute}, []int{11}, []uint64{123}, strings.Repeat("a", 32))
	for _, want := range []string{"tracepoint:sched:sched_switch", "tracepoint:irq_vectors:*_entry", "on_cpu_ns", "residence_min_ns", "out_of_order_queue.rb_node", "@rxdepth[(uint64)0,(uint64)65536]=(uint64)0", "clear(@q_ranges)", "clear(@rx_cpu_acc)"} {
		if !strings.Contains(script, want) {
			t.Fatal("missing accounting boundary or bounded cleanup", want)
		}
	}
	for _, bad := range []string{"skb->data", "skb->cb", "args->buf", "%p", "\\\"seq\\\":"} {
		if strings.Contains(script, bad) {
			t.Fatal("payload or raw sequence exported", bad)
		}
	}
}

func TestTransportKeepsEvidenceNearSelectedRequest(t *testing.T) {
	s, e := NewStore(filepath.Join(t.TempDir(), "trace.db"), 4, 1000)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	batch := []TransportRecord{traceFixture("accept", "", "100", "100")}
	for i := 0; i < 500; i++ {
		batch = append(batch, traceFixture("read", "", "100", fmt.Sprint(200+i)))
	}
	begin := traceFixture("request_begin", "11-5-7-1000", "100", "1000")
	begin.RequestNumber = 5
	batch = append(batch, begin)
	snapshot := traceFixture("socket", "", "100", "1001")
	snapshot.Socket = &SocketObservation{Point: "recvmsg_return", State: 1, ReceiveMemory: 1234}
	batch = append(batch, snapshot)
	end := begin
	end.Event = "request_end"
	end.At = "1010"
	batch = append(batch, end)
	for i := 0; i < 500; i++ {
		batch = append(batch, traceFixture("read", "", "100", fmt.Sprint(1100+i)))
	}
	if e = s.insertTransport(batch); e != nil {
		t.Fatal(e)
	}
	v, e := s.Transport(begin.RequestID)
	if e != nil || !v.Truncated || len(v.Records) > 256 || v.Begin == nil || v.End == nil {
		t.Fatal(v, e)
	}
	setup, near := false, false
	for _, r := range v.Records {
		setup = setup || r.Event == "accept"
		near = near || r.Socket != nil && r.Socket.ReceiveMemory == 1234
	}
	if !setup || !near {
		t.Fatal("lost setup or selected-request socket evidence", setup, near)
	}
}
