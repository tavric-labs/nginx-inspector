package collector

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

type LiveRequest struct {
	ID            string `json:"id"`
	Method        string `json:"method"`
	Path          string `json:"path"`
	Phase         string `json:"phase"`
	Elapsed       int64  `json:"elapsed_ms"`
	IO            uint32 `json:"io"`
	Status        int    `json:"status"`
	ResponseBytes int64  `json:"response_bytes"`
}
type ClientConnections struct {
	Active  int64 `json:"active"`
	Reading int64 `json:"reading"`
	Writing int64 `json:"writing"`
	Waiting int64 `json:"waiting"`
}

type WorkerTraffic struct {
	SlotsUsed     int64              `json:"connection_slots_used"`
	SlotsCapacity int64              `json:"connection_slots_capacity"`
	Connections   *ClientConnections `json:"client_connections"`
	Schema        int                `json:"schema"`
	Kind          string             `json:"kind"`
	PID           int                `json:"worker_pid"`
	Master        int                `json:"master_pid"`
	At            int64              `json:"at_ms"`
	Inflight      int64              `json:"sampled_inflight"`
	Omitted       int64              `json:"omitted"`
	Requests      []LiveRequest      `json:"requests"`
	received      time.Time
}

func ValidateWorker(raw []byte) (WorkerTraffic, error) {
	var w WorkerTraffic
	if len(raw) > 8192 {
		return w, fmt.Errorf("worker payload too large")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if e := d.Decode(&w); e != nil {
		return w, e
	}
	if d.Decode(new(any)) != io.EOF {
		return w, fmt.Errorf("trailing JSON")
	}
	delta := w.At - time.Now().UnixMilli()
	if w.SlotsUsed < 0 || w.SlotsCapacity < 0 || w.SlotsUsed > w.SlotsCapacity || w.Schema != 1 || w.Kind != "worker" || w.PID <= 0 || w.Master <= 0 || w.Inflight < 0 || w.Omitted < 0 || w.Inflight != int64(len(w.Requests))+w.Omitted || len(w.Requests) > 8 || delta < -10000 || delta > 10000 {
		return w, fmt.Errorf("invalid worker snapshot")
	}
	if w.Connections != nil && (w.Connections.Active < 0 || w.Connections.Reading < 0 || w.Connections.Writing < 0 || w.Connections.Waiting < 0) {
		return w, fmt.Errorf("invalid client counters")
	}
	for i := range w.Requests {
		r := &w.Requests[i]
		if !idPattern.MatchString(r.ID) || len(r.Path) > 256 || len(r.Method) > 32 || !contains(spec.Trace, r.Phase) || r.Elapsed < 0 || r.ResponseBytes < 0 || (r.Status != 0 && (r.Status < 100 || r.Status > 599)) {
			return w, fmt.Errorf("invalid live request")
		}
		r.Path = strings.SplitN(r.Path, "?", 2)[0]
	}
	w.received = time.Now()
	return w, nil
}

type WorkerProcess struct {
	PID               int                `json:"pid"`
	Parent            int                `json:"parent_pid"`
	Role              string             `json:"role"`
	State             string             `json:"state"`
	Threads           int64              `json:"threads"`
	RSS               *int64             `json:"rss_bytes"`
	Anonymous         *int64             `json:"anonymous_bytes"`
	File              *int64             `json:"file_bytes"`
	Shared            *int64             `json:"shared_bytes"`
	Virtual           int64              `json:"virtual_bytes"`
	CPU               *float64           `json:"cpu_percent"`
	UserCPU           *float64           `json:"user_cpu_percent"`
	SystemCPU         *float64           `json:"system_cpu_percent"`
	UserSeconds       *float64           `json:"user_cpu_seconds"`
	SystemSeconds     *float64           `json:"system_cpu_seconds"`
	Data              *int64             `json:"data_bytes"`
	MajorFaults       int64              `json:"major_faults"`
	MinorFaults       int64              `json:"minor_faults"`
	VoluntarySwitches *int64             `json:"voluntary_switches"`
	Preemptions       *int64             `json:"involuntary_switches"`
	VoluntaryRate     *float64           `json:"voluntary_switches_per_second"`
	PreemptionRate    *float64           `json:"involuntary_switches_per_second"`
	Scheduling        *ProcessScheduling `json:"scheduling"`
	Traffic           *WorkerTraffic     `json:"traffic"`
	ticks             int64
	userTicks         int64
	systemTicks       int64
	StartTicks        string `json:"start_ticks"`
	start             string
}

// Linux scheduler dispatches and context switches are not event-loop wakeups.
type ProcessScheduling struct {
	RuntimeNS           int64    `json:"runtime_ns"`
	RunqueueNS          int64    `json:"runqueue_ns"`
	Slices              int64    `json:"slices"`
	RunqueueMSPerSecond *float64 `json:"runqueue_ms_per_second"`
	SlicesPerSecond     *float64 `json:"slices_per_second"`
	CPUUSPerSlice       *float64 `json:"cpu_us_per_slice"`
}

func counterRate(current, previous *int64, seconds float64) *float64 {
	if current == nil || previous == nil || *current < *previous || seconds <= 0 {
		return nil
	}
	v := float64(*current-*previous) / seconds
	return &v
}

func processDeltas(p *WorkerProcess, old WorkerProcess, seconds, clockTicks float64) {
	if p.start != old.start || seconds <= 0 {
		return
	}
	if p.userTicks >= old.userTicks && p.systemTicks >= old.systemTicks && clockTicks > 0 {
		u := 100 * float64(p.userTicks-old.userTicks) / clockTicks / seconds
		s := 100 * float64(p.systemTicks-old.systemTicks) / clockTicks / seconds
		total := u + s
		p.CPU, p.UserCPU, p.SystemCPU = &total, &u, &s
	}
	p.VoluntaryRate = counterRate(p.VoluntarySwitches, old.VoluntarySwitches, seconds)
	p.PreemptionRate = counterRate(p.Preemptions, old.Preemptions, seconds)
	if n, o := p.Scheduling, old.Scheduling; n != nil && o != nil && n.RuntimeNS >= o.RuntimeNS && n.RunqueueNS >= o.RunqueueNS && n.Slices >= o.Slices {
		wait := float64(n.RunqueueNS-o.RunqueueNS) / 1e6 / seconds
		slices := float64(n.Slices-o.Slices) / seconds
		n.RunqueueMSPerSecond, n.SlicesPerSecond = &wait, &slices
		if n.Slices > o.Slices {
			cpu := float64(n.RuntimeNS-o.RuntimeNS) / 1e3 / float64(n.Slices-o.Slices)
			n.CPUUSPerSlice = &cpu
		}
	}
}

type WorkerView struct {
	MasterPID     int                `json:"master_pid"`
	Available     bool               `json:"available"`
	Error         string             `json:"error,omitempty"`
	At            int64              `json:"sampled_ms"`
	ScanLimited   bool               `json:"scan_limited"`
	Scoped        bool               `json:"scoped"`
	Processes     []WorkerProcess    `json:"processes"`
	LiveWorkers   []WorkerTraffic    `json:"live_workers"`
	CaptureActive bool               `json:"capture_active"`
	Diagnostics   *WorkerDiagnostics `json:"diagnostics,omitempty"`
}
type workerMonitor struct {
	mu            sync.Mutex
	root, pidfile string
	view          WorkerView
	previous      map[int]WorkerProcess
	previousAt    time.Time
	traffic       map[int]WorkerTraffic
	clockTicks    float64
}

func newWorkerMonitor(root, pidfile string) *workerMonitor {
	return &workerMonitor{root: root, pidfile: pidfile, previous: map[int]WorkerProcess{}, traffic: map[int]WorkerTraffic{}, clockTicks: processClockTicks()}
}
func (w *workerMonitor) observe(v WorkerTraffic) bool {
	if !w.matchesMaster(v.Master) {
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.traffic) >= 256 {
		for pid, v := range w.traffic {
			if time.Since(v.received) > 5*time.Second {
				delete(w.traffic, pid)
			}
		}
	}
	if len(w.traffic) < 256 {
		w.traffic[v.PID] = v
		return true
	}
	return false
}
func (w *workerMonitor) clear() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.traffic = map[int]WorkerTraffic{}
}

func (w *workerMonitor) snapshot(active bool) WorkerView {
	w.mu.Lock()
	defer w.mu.Unlock()
	v := w.view
	v.Processes = append([]WorkerProcess{}, v.Processes...)
	v.LiveWorkers = []WorkerTraffic{}
	v.CaptureActive = active
	if active {
		for _, t := range w.traffic {
			if time.Since(t.received) <= 4*time.Second {
				allowed := !v.Scoped
				for _, p := range v.Processes {
					if p.PID == t.PID {
						allowed = true
						break
					}
				}
				if !allowed {
					continue
				}
				v.LiveWorkers = append(v.LiveWorkers, t)
			}
		}
		for i := range v.Processes {
			if t, ok := w.traffic[v.Processes[i].PID]; ok && time.Since(t.received) <= 4*time.Second {
				copy := t
				v.Processes[i].Traffic = &copy
			}
		}
	}
	return v
}
func (w *workerMonitor) sample() {
	view := readWorkerProcesses(w.root, w.pidfile)
	now := time.Now()
	w.mu.Lock()
	defer w.mu.Unlock()
	for pid, t := range w.traffic {
		if time.Since(t.received) > 4*time.Second {
			delete(w.traffic, pid)
		}
	}
	next := map[int]WorkerProcess{}
	for i := range view.Processes {
		p := &view.Processes[i]
		if w.clockTicks > 0 {
			u, s := float64(p.userTicks)/w.clockTicks, float64(p.systemTicks)/w.clockTicks
			p.UserSeconds, p.SystemSeconds = &u, &s
		}
		if old, ok := w.previous[p.PID]; ok && !w.previousAt.IsZero() {
			processDeltas(p, old, now.Sub(w.previousAt).Seconds(), w.clockTicks)
		}
		next[p.PID] = *p
	}
	w.previous = next
	w.previousAt = now
	view.At = now.UnixMilli()
	w.view = view
}
func (w *workerMonitor) run(ctx context.Context) {
	w.sample()
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.sample()
		}
	}
}

func (w *workerMonitor) matchesMaster(claim int) bool {
	if w.pidfile == "" {
		return true
	}
	w.mu.Lock()
	cached := w.view.MasterPID
	for _, p := range w.view.Processes {
		if (p.PID == claim || p.Parent == claim) && (p.Role == "master" || p.Role == "worker" || p.Role == "draining worker") {
			w.mu.Unlock()
			return true
		}
	}
	w.mu.Unlock()
	if claim > 0 && claim == cached {
		return true
	}
	f, e := os.Open(w.pidfile)
	if e != nil {
		return false
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, 32))
	if e != nil {
		return false
	}
	pid, e := strconv.Atoi(strings.TrimSpace(string(b)))
	if e != nil || pid <= 0 {
		return false
	}
	w.mu.Lock()
	w.view.MasterPID = pid
	w.view.Scoped = true
	w.mu.Unlock()
	return claim == pid
}
func (w *workerMonitor) accepts(e Event) bool {
	if w.pidfile == "" {
		return true
	}
	if native, ok := e["native"].(map[string]any); ok {
		nginx, _ := native["nginx"].(map[string]any)
		master, ok := integer(nginx["master_pid"])
		return ok && w.matchesMaster(int(master))
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	id, _ := e["id"].(string)
	pid, err := strconv.Atoi(strings.SplitN(id, "-", 2)[0])
	if err != nil {
		return false
	}
	for _, p := range w.view.Processes {
		if p.PID == pid && (p.Role == "worker" || p.Role == "draining worker") {
			return true
		}
	}
	return false
}
