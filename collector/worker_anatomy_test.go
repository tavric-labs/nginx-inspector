package collector

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProcessDeltaIdentityResetAndMissingCounters(t *testing.T) {
	i := func(v int64) *int64 { return &v }
	old := WorkerProcess{start: "42", userTicks: 20, systemTicks: 10, VoluntarySwitches: i(10), Preemptions: i(5), Scheduling: &ProcessScheduling{RuntimeNS: 1000000, RunqueueNS: 2000000, Slices: 2}}
	p := WorkerProcess{start: "42", userTicks: 24, systemTicks: 12, VoluntarySwitches: i(14), Preemptions: i(7), Scheduling: &ProcessScheduling{RuntimeNS: 3000000, RunqueueNS: 6000000, Slices: 6}}
	processDeltas(&p, old, 2, 100)
	if p.CPU == nil || *p.CPU != 3 || *p.UserCPU != 2 || *p.SystemCPU != 1 || *p.VoluntaryRate != 2 || *p.PreemptionRate != 1 || *p.Scheduling.SlicesPerSecond != 2 || *p.Scheduling.CPUUSPerSlice != 500 || *p.Scheduling.RunqueueMSPerSecond != 2 {
		t.Fatalf("wrong deltas: %+v", p)
	}
	reset := WorkerProcess{start: "43", userTicks: 30, systemTicks: 20}
	processDeltas(&reset, old, 2, 100)
	if reset.CPU != nil || reset.VoluntaryRate != nil {
		t.Fatal("PID reuse exported a false delta")
	}
	reset = WorkerProcess{start: "42", userTicks: 1, systemTicks: 1, VoluntarySwitches: i(1), Scheduling: &ProcessScheduling{}}
	processDeltas(&reset, old, 2, 100)
	if reset.CPU != nil || reset.VoluntaryRate != nil || reset.Scheduling.CPUUSPerSlice != nil {
		t.Fatal("regressed counters exported deltas")
	}
	p = WorkerProcess{start: "42", userTicks: 24, systemTicks: 12}
	processDeltas(&p, old, 2, 0)
	if p.CPU != nil || p.VoluntaryRate != nil || p.Scheduling != nil {
		t.Fatal("missing data was fabricated")
	}
}

func diagnosticFixture() WorkerDiagnostics {
	now := time.Now().UnixMilli()
	return WorkerDiagnostics{Schema: 1, StartedMS: now - 120000, EndedMS: now, MasterPID: 10, MasterStartTicks: "42", Processes: []DiagnosticProcess{{PID: 10, StartTicks: "42"}, {PID: 11, StartTicks: "43", FDs: map[string]int64{"socket": 4}, Epoll: []DiagnosticEpoll{{FD: 7, Target: 8, Flags: []string{"IN", "EXCLUSIVE"}}}}}, Sockets: []DiagnosticSocketSample{{AtMS: now - 1000, Workers: []DiagnosticSockets{{PID: 11, Clients: 2, Upstreams: 1}}, Listeners: []DiagnosticListener{{Inode: 123, PIDs: []int{10, 11}}, {Inode: 124, PIDs: []int{10, 11}}}}}, Trace: DiagnosticTrace{Seconds: 60, Status: "complete", Calls: []DiagnosticCalls{{PID: 11, Name: "epoll_wait", Count: 60}}}}
}
func TestDiagnosticsRejectsUnknownPrivateFieldsAndOversize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "diagnostics.json")
	write := func(b []byte) {
		t.Helper()
		if e := os.WriteFile(path, b, 0600); e != nil {
			t.Fatal(e)
		}
	}
	fixture := diagnosticFixture()
	write(encoded(fixture))
	got, e := loadWorkerDiagnostics(path)
	if e != nil || len(got.Sockets[0].Listeners) != 2 || strings.Contains(string(encoded(got)), `"profile":null`) || strings.Contains(string(encoded(got)), `"fd_counts":null`) {
		t.Fatal(got, e)
	}
	for _, raw := range [][]byte{[]byte(strings.Replace(string(encoded(fixture)), `"schema":1`, `"authorization":"private","schema":1`, 1)), append(encoded(fixture), []byte(` {}`)...), make([]byte, 262145)} {
		write(raw)
		if _, e := loadWorkerDiagnostics(path); e == nil {
			t.Fatal("accepted unknown, trailing or oversized JSON")
		}
	}
	fixture = diagnosticFixture()
	fixture.Trace.Calls[0].PID = 999
	write(encoded(fixture))
	if _, e := loadWorkerDiagnostics(path); e == nil {
		t.Fatal("accepted unscoped trace PID")
	}
	fixture = diagnosticFixture()
	fixture.Processes[1].Epoll[0].Flags = []string{"unknown"}
	write(encoded(fixture))
	if _, e := loadWorkerDiagnostics(path); e == nil {
		t.Fatal("accepted unknown epoll flag")
	}
	fixture = diagnosticFixture()
	fixture.MasterStartTicks = "999"
	write(encoded(fixture))
	if _, e := loadWorkerDiagnostics(path); e == nil {
		t.Fatal("accepted master identity mismatch")
	}
	fixture = diagnosticFixture()
	fixture.EndedMS = time.Now().Add(time.Hour).UnixMilli()
	write(encoded(fixture))
	if _, e := loadWorkerDiagnostics(path); e == nil {
		t.Fatal("accepted future snapshot")
	}
}

func TestInvalidDiagnosticsStartupReturnsError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.json")
	if e := os.WriteFile(path, []byte(`{"schema":999}`), 0600); e != nil {
		t.Fatal(e)
	}
	if s, e := NewServer(Config{WorkerDiagnosticsFile: path}); e == nil || s != nil {
		t.Fatal("invalid configured snapshot did not fail startup")
	}
}
