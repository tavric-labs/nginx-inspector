package collector

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// Operator-provided historical measurements. Never merged into live counters.
// The collector has no root capabilities and never executes diagnostic commands.
type WorkerDiagnostics struct {
	Schema           int                      `json:"schema"`
	StartedMS        int64                    `json:"started_ms"`
	EndedMS          int64                    `json:"ended_ms"`
	MasterPID        int                      `json:"master_pid"`
	MasterStartTicks string                   `json:"master_start_ticks"`
	Processes        []DiagnosticProcess      `json:"processes"`
	Sockets          []DiagnosticSocketSample `json:"socket_samples"`
	Trace            DiagnosticTrace          `json:"trace"`
	ProfileStatus    string                   `json:"profile_status"`
	Profile          []DiagnosticSymbol       `json:"profile"`
}
type DiagnosticProcess struct {
	PID        int               `json:"pid"`
	StartTicks string            `json:"start_ticks"`
	FDs        map[string]int64  `json:"fd_counts"`
	Epoll      []DiagnosticEpoll `json:"epoll"`
	PSS        *int64            `json:"pss_bytes"`
	Private    *int64            `json:"private_bytes"`
	Shared     *int64            `json:"shared_bytes"`
	ReadCalls  *int64            `json:"read_calls"`
	WriteCalls *int64            `json:"write_calls"`
}
type DiagnosticEpoll struct {
	FD     int      `json:"epoll_fd"`
	Target int      `json:"target_fd"`
	Flags  []string `json:"flags"`
}
type DiagnosticSocketSample struct {
	AtMS      int64                `json:"at_ms"`
	Workers   []DiagnosticSockets  `json:"workers"`
	Listeners []DiagnosticListener `json:"listeners"`
}
type DiagnosticListener struct {
	Inode int64 `json:"inode"`
	PIDs  []int `json:"pids"`
}
type DiagnosticSockets struct {
	PID       int   `json:"pid"`
	Clients   int64 `json:"client_sockets"`
	Upstreams int64 `json:"upstream_sockets"`
}
type DiagnosticTrace struct {
	Status          string             `json:"status"`
	Seconds         float64            `json:"seconds"`
	Calls           []DiagnosticCalls  `json:"calls"`
	WallHistogram   []DiagnosticBucket `json:"epoll_wall_us"`
	EventsHistogram []DiagnosticBucket `json:"returned_events"`
}
type DiagnosticCalls struct {
	PID   int    `json:"pid"`
	Name  string `json:"name"`
	Count int64  `json:"count"`
}
type DiagnosticBucket struct {
	Range string `json:"range"`
	Count int64  `json:"count"`
}
type DiagnosticSymbol struct {
	Percent float64 `json:"percent"`
	DSO     string  `json:"dso"`
	Symbol  string  `json:"symbol"`
}

func loadWorkerDiagnostics(path string) (*WorkerDiagnostics, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("cannot open configured snapshot")
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 262145))
	if err != nil || len(b) > 262144 {
		return nil, fmt.Errorf("snapshot exceeds 256 KiB or cannot be read")
	}
	var d WorkerDiagnostics
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&d); err != nil {
		return nil, fmt.Errorf("invalid snapshot JSON")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return nil, fmt.Errorf("trailing snapshot JSON")
	}
	bad := func() (*WorkerDiagnostics, error) { return nil, fmt.Errorf("invalid snapshot fields or bounds") }
	safe := func(s string, limit int) bool { return len(s) <= limit && !strings.ContainsAny(s, "\x00\r\n") }
	digits := func(s string) bool {
		if len(s) == 0 || len(s) > 24 {
			return false
		}
		for _, c := range s {
			if c < '0' || c > '9' {
				return false
			}
		}
		return true
	}
	if d.Schema != 1 || d.MasterPID <= 0 || !digits(d.MasterStartTicks) || d.StartedMS <= 0 || d.EndedMS < d.StartedMS || d.EndedMS > time.Now().Add(time.Minute).UnixMilli() || d.EndedMS-d.StartedMS > 3600000 || len(d.Processes) > 64 || len(d.Sockets) > 32 || len(d.Profile) > 100 || !safe(d.ProfileStatus, 200) {
		return bad()
	}
	known := map[int]bool{}
	for _, p := range d.Processes {
		if p.PID <= 0 || (p.PID == d.MasterPID && p.StartTicks != d.MasterStartTicks) || known[p.PID] || !digits(p.StartTicks) || len(p.FDs) > 6 || len(p.Epoll) > 8192 {
			return bad()
		}
		known[p.PID] = true
		for k, v := range p.FDs {
			if !contains([]string{"socket", "pipe", "epoll", "eventfd", "anonymous", "file"}, k) || v < 0 || v > 1000000 {
				return bad()
			}
		}
		for _, v := range []*int64{p.PSS, p.Private, p.Shared, p.ReadCalls, p.WriteCalls} {
			if v != nil && *v < 0 {
				return bad()
			}
		}
		for _, e := range p.Epoll {
			if e.FD < 0 || e.Target < 0 || len(e.Flags) > 7 {
				return bad()
			}
			for _, flag := range e.Flags {
				if !contains([]string{"IN", "OUT", "ERR", "HUP", "RDHUP", "EXCLUSIVE", "ET"}, flag) {
					return bad()
				}
			}
		}
	}
	if !known[d.MasterPID] {
		return bad()
	}
	for _, s := range d.Sockets {
		if s.AtMS < d.StartedMS || s.AtMS > d.EndedMS || len(s.Workers) > 64 || len(s.Listeners) > 64 {
			return bad()
		}
		for _, listener := range s.Listeners {
			if listener.Inode <= 0 || len(listener.PIDs) > 64 {
				return bad()
			}
			for _, pid := range listener.PIDs {
				if !known[pid] {
					return bad()
				}
			}
		}
		seen := map[int]bool{}
		for _, w := range s.Workers {
			if !known[w.PID] || seen[w.PID] || w.Clients < 0 || w.Upstreams < 0 {
				return bad()
			}
			seen[w.PID] = true
		}
	}
	if !safe(d.Trace.Status, 200) || d.Trace.Seconds <= 0 || d.Trace.Seconds > 300 || len(d.Trace.Calls) > 1024 || len(d.Trace.WallHistogram) > 128 || len(d.Trace.EventsHistogram) > 128 {
		return bad()
	}
	for _, c := range d.Trace.Calls {
		if !known[c.PID] || c.Count < 0 || !contains([]string{"epoll_wait", "accept4", "epoll_ctl", "sendto", "recvfrom", "writev", "read", "write"}, c.Name) {
			return bad()
		}
	}
	for _, b := range append(append([]DiagnosticBucket{}, d.Trace.WallHistogram...), d.Trace.EventsHistogram...) {
		if !safe(b.Range, 40) || b.Count < 0 {
			return bad()
		}
	}
	for _, p := range d.Profile {
		if p.Percent < 0 || p.Percent > 100 || !safe(p.DSO, 160) || !safe(p.Symbol, 256) {
			return bad()
		}
	}
	// Keep the API's collection shapes stable even when optional JSON arrays are absent.
	if d.Processes == nil {
		d.Processes = []DiagnosticProcess{}
	}
	if d.Sockets == nil {
		d.Sockets = []DiagnosticSocketSample{}
	}
	if d.Profile == nil {
		d.Profile = []DiagnosticSymbol{}
	}
	if d.Trace.Calls == nil {
		d.Trace.Calls = []DiagnosticCalls{}
	}
	if d.Trace.WallHistogram == nil {
		d.Trace.WallHistogram = []DiagnosticBucket{}
	}
	if d.Trace.EventsHistogram == nil {
		d.Trace.EventsHistogram = []DiagnosticBucket{}
	}
	for i := range d.Processes {
		p := &d.Processes[i]
		if p.FDs == nil {
			p.FDs = map[string]int64{}
		}
		if p.Epoll == nil {
			p.Epoll = []DiagnosticEpoll{}
		}
		for j := range p.Epoll {
			if p.Epoll[j].Flags == nil {
				p.Epoll[j].Flags = []string{}
			}
		}
	}
	for i := range d.Sockets {
		s := &d.Sockets[i]
		if s.Workers == nil {
			s.Workers = []DiagnosticSockets{}
		}
		if s.Listeners == nil {
			s.Listeners = []DiagnosticListener{}
		}
		for j := range s.Listeners {
			if s.Listeners[j].PIDs == nil {
				s.Listeners[j].PIDs = []int{}
			}
		}
	}
	return &d, nil
}
