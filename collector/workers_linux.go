//go:build linux

package collector

import (
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

func boundedFile(path string, limit int64) ([]byte, error) {
	f, e := os.Open(path)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, limit))
}
func processClockTicks() float64 {
	b, e := boundedFile("/proc/self/auxv", 4096)
	if e != nil {
		return 0
	}
	word := strconv.IntSize / 8
	for len(b) >= 2*word {
		var key, value uint64
		if word == 8 {
			key, value = binary.NativeEndian.Uint64(b[:8]), binary.NativeEndian.Uint64(b[8:16])
		} else {
			key, value = uint64(binary.NativeEndian.Uint32(b[:4])), uint64(binary.NativeEndian.Uint32(b[4:8]))
		}
		if key == 17 {
			return float64(value)
		}
		b = b[2*word:]
	}
	return 0
}
func parseProcessStat(raw string) (WorkerProcess, bool) {
	var p WorkerProcess
	i := strings.LastIndex(raw, ") ")
	open := strings.Index(raw, "(")
	if open < 0 || i <= open {
		return p, false
	}
	head := strings.Fields(raw[:strings.Index(raw, "(")])
	if len(head) != 1 {
		return p, false
	}
	var e error
	p.PID, e = strconv.Atoi(head[0])
	if e != nil {
		return p, false
	}
	f := strings.Fields(raw[i+2:])
	if len(f) < 22 {
		return p, false
	}
	n := func(i int) int64 { v, _ := strconv.ParseInt(f[i], 10, 64); return v }
	p.Parent = int(n(1))
	p.State = f[0]
	p.userTicks, p.systemTicks = n(11), n(12)
	p.ticks = p.userTicks + p.systemTicks
	p.MinorFaults, p.MajorFaults = n(7), n(9)
	p.Threads = n(17)
	p.start = f[19]
	p.StartTicks = p.start
	p.Virtual = n(20)
	return p, true
}
func parseMemory(raw string, p *WorkerProcess) {
	values := map[string]**int64{"VmRSS": &p.RSS, "RssAnon": &p.Anonymous, "RssFile": &p.File, "RssShmem": &p.Shared, "VmData": &p.Data, "voluntary_ctxt_switches": &p.VoluntarySwitches, "nonvoluntary_ctxt_switches": &p.Preemptions}
	for _, line := range strings.Split(raw, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		target, ok := values[strings.TrimSuffix(f[0], ":")]
		if !ok {
			continue
		}
		n, e := strconv.ParseInt(f[1], 10, 64)
		if e == nil && n >= 0 {
			if strings.HasPrefix(f[0], "Vm") || strings.HasPrefix(f[0], "Rss") {
				if len(f) != 3 || f[2] != "kB" || n > (1<<63-1)/1024 {
					continue
				}
				n *= 1024
			}
			*target = &n
		}
	}
}

func parseScheduling(raw string) *ProcessScheduling {
	f := strings.Fields(raw)
	if len(f) != 3 {
		return nil
	}
	n := make([]int64, 3)
	for i := range n {
		v, err := strconv.ParseInt(f[i], 10, 64)
		if err != nil || v < 0 {
			return nil
		}
		n[i] = v
	}
	// A zero schedstat triple can also mean scheduler statistics are disabled.
	if n[0] == 0 && n[1] == 0 && n[2] == 0 {
		return nil
	}
	return &ProcessScheduling{RuntimeNS: n[0], RunqueueNS: n[1], Slices: n[2]}
}
func readWorkerProcesses(root, pidfile string) WorkerView {
	v := WorkerView{Processes: []WorkerProcess{}}
	if root == "" {
		v.Error = "OS process sampling is not configured"
		return v
	}
	scope := 0
	if pidfile != "" {
		b, e := boundedFile(pidfile, 32)
		if e != nil {
			v.Error = "nginx PID file is unavailable"
			return v
		}
		scope, e = strconv.Atoi(strings.TrimSpace(string(b)))
		if e != nil || scope <= 0 {
			v.Error = "invalid nginx PID file"
			return v
		}
		v.Scoped = true
		v.MasterPID = scope
	}
	dir, e := os.Open(root)
	if e != nil {
		v.Error = "proc filesystem is unavailable"
		return v
	}
	defer dir.Close()
	v.Available = true
	scanned := 0
	for scanned < 8192 && len(v.Processes) < 64 {
		entries, e := dir.ReadDir(256)
		if e != nil && e != io.EOF {
			v.Error = "proc scan incomplete"
			break
		}
		for _, d := range entries {
			scanned++
			pid, e := strconv.Atoi(d.Name())
			if e != nil || pid <= 0 {
				continue
			}
			base := filepath.Join(root, d.Name())
			comm, e := boundedFile(filepath.Join(base, "comm"), 32)
			if e != nil || strings.TrimSpace(string(comm)) != "nginx" {
				continue
			}
			raw, e := boundedFile(filepath.Join(base, "stat"), 4096)
			if e != nil {
				continue
			}
			p, ok := parseProcessStat(string(raw))
			if !ok || (scope != 0 && p.PID != scope && p.Parent != scope) {
				continue
			}
			cmd, e := boundedFile(filepath.Join(base, "cmdline"), 128)
			if e != nil {
				p.Role = "nginx"
			} else {
				title := string(cmd)
				switch {
				case strings.HasPrefix(title, "nginx: worker process is shutting down"):
					p.Role = "draining worker"
				case strings.HasPrefix(title, "nginx: worker process"):
					p.Role = "worker"
				case strings.HasPrefix(title, "nginx: master process"):
					p.Role = "master"
				case strings.HasPrefix(title, "nginx: cache manager"):
					p.Role = "cache manager"
				case strings.HasPrefix(title, "nginx: cache loader"):
					p.Role = "cache loader"
				default:
					p.Role = "nginx"
				}
			}
			if b, e := boundedFile(filepath.Join(base, "status"), 16384); e == nil {
				parseMemory(string(b), &p)
			}
			if b, e := boundedFile(filepath.Join(base, "schedstat"), 256); e == nil {
				p.Scheduling = parseScheduling(string(b))
			}
			v.Processes = append(v.Processes, p)
			if len(v.Processes) == 64 {
				break
			}
		}
		if e == io.EOF {
			break
		}
	}
	v.ScanLimited = scanned >= 8192 || len(v.Processes) >= 64
	sort.Slice(v.Processes, func(i, j int) bool { return v.Processes[i].PID < v.Processes[j].PID })
	return v
}
