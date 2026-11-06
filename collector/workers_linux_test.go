package collector

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestWorkerTelemetryBoundsAndPrivacy(t *testing.T) {
	w := WorkerTraffic{Schema: 1, Kind: "worker", PID: 42, Master: 1, At: time.Now().UnixMilli(), Inflight: 1, Requests: []LiveRequest{{ID: "42-1-2", Method: "GET", Path: "/slow?token=secret", Phase: "precontent", Elapsed: 1}}}
	v, e := ValidateWorker(encoded(w))
	if e != nil || strings.Contains(string(encoded(v)), "secret") {
		t.Fatal(v, e)
	}
	raw := strings.Replace(string(encoded(w)), `"method":"GET"`, `"authorization":"secret","method":"GET"`, 1)
	if _, e := ValidateWorker([]byte(raw)); e == nil {
		t.Fatal("unknown worker field accepted")
	}
	w.Inflight = 9
	w.Requests = make([]LiveRequest, 9)
	if _, e := ValidateWorker(encoded(w)); e == nil {
		t.Fatal("live request cap not enforced")
	}
}
func TestWorkerViewClearsLiveRequestsWhenOff(t *testing.T) {
	w := newWorkerMonitor("", "")
	w.observe(WorkerTraffic{PID: 42, received: time.Now(), Requests: []LiveRequest{{Path: "/inflight"}}})
	if len(w.snapshot(true).LiveWorkers) != 1 {
		t.Fatal("missing active telemetry")
	}
	if len(w.snapshot(false).LiveWorkers) != 0 {
		t.Fatal("retained live telemetry after stop")
	}
}
func TestWorkerProcScopeAndMemory(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux proc")
	}
	root := t.TempDir()
	write := func(pid int, parent int, role string) {
		d := filepath.Join(root, fmt.Sprint(pid))
		os.MkdirAll(d, 0700)
		os.WriteFile(filepath.Join(d, "comm"), []byte("nginx\n"), 0600)
		f := make([]string, 22)
		for i := range f {
			f[i] = "0"
		}
		f[0] = "S"
		f[1] = fmt.Sprint(parent)
		f[11] = "20"
		f[12] = "10"
		f[17] = "1"
		f[19] = "12345"
		f[20] = "8192"
		os.WriteFile(filepath.Join(d, "stat"), []byte(fmt.Sprintf("%d (nginx) %s", pid, strings.Join(f, " "))), 0600)
		os.WriteFile(filepath.Join(d, "status"), []byte("VmRSS:\t1024 kB\nRssAnon:\t512 kB\nRssFile:\t512 kB\n"), 0600)
		os.WriteFile(filepath.Join(d, "cmdline"), []byte("nginx: "+role+"\x00private-argument-not-exported"), 0600)
	}
	write(10, 1, "master process")
	write(11, 10, "worker process")
	write(99, 98, "worker process")
	pidfile := filepath.Join(root, "nginx.pid")
	os.WriteFile(pidfile, []byte("10\n"), 0600)
	v := readWorkerProcesses(root, pidfile)
	if !v.Available || !v.Scoped || len(v.Processes) != 2 {
		t.Fatal(v)
	}
	if v.Processes[1].Role != "worker" || v.Processes[1].RSS == nil || *v.Processes[1].RSS != 1048576 {
		t.Fatal(v.Processes[1])
	}
	if strings.Contains(string(encoded(v)), "private-argument") {
		t.Fatal("exported command line")
	}
}
