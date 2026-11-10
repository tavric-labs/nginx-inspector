package collector

import (
	"bufio"
	"context"
	"crypto/rand"
	"debug/elf"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

type TraceOptions struct {
	MasterPID  int
	Module     string
	Duration   time.Duration
	Deadline   time.Time
	UDPPort    int
	streamOnly bool
}

var modulePathPattern = regexp.MustCompile(`^/[a-zA-Z0-9_./-]+$`)

func traceWorkers(master int) ([]int, map[int]string, error) {
	if master <= 0 {
		return nil, nil, fmt.Errorf("positive master PID required")
	}
	exe, e := os.Readlink(fmt.Sprintf("/proc/%d/exe", master))
	if e != nil || filepath.Base(exe) != "nginx" {
		return nil, nil, fmt.Errorf("target is not an accessible nginx master")
	}
	b, e := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", master))
	if e != nil || !strings.HasPrefix(string(b), "nginx: master process") {
		return nil, nil, fmt.Errorf("nginx master identity not verified")
	}
	v := readWorkerProcesses("/proc", "")
	var pids []int
	starts := map[int]string{}
	for _, p := range v.Processes {
		if p.PID == master {
			starts[master] = p.start
		}
		if p.Parent == master && (p.Role == "worker" || p.Role == "draining worker") {
			pids = append(pids, p.PID)
			starts[p.PID] = p.start
		}
	}
	if len(pids) == 0 || len(pids) > 32 || v.ScanLimited {
		return nil, nil, fmt.Errorf("cannot bound worker scope")
	}
	return pids, starts, nil
}
func traceListeners(master int, workers []int) ([]uint64, error) {
	// ss output is consumed locally; endpoint addresses are never exported.
	b, e := exec.Command("ss", "-H", "-ltnpe").Output()
	if e != nil {
		return nil, fmt.Errorf("cannot enumerate selected nginx listeners")
	}
	wanted := map[int]bool{master: true}
	for _, p := range workers {
		wanted[p] = true
	}
	pidRE := regexp.MustCompile(`pid=(\d+)`)
	inoRE := regexp.MustCompile(`ino:(\d+)`)
	var inodes []uint64
	seen := map[uint64]bool{}
	for _, line := range strings.Split(string(b), "\n") {
		selected := false
		for _, m := range pidRE.FindAllStringSubmatch(line, -1) {
			p, _ := strconv.Atoi(m[1])
			if wanted[p] {
				selected = true
			}
		}
		if !selected {
			continue
		}
		m := inoRE.FindStringSubmatch(line)
		if len(m) != 2 {
			continue
		}
		ino, e := strconv.ParseUint(m[1], 10, 64)
		if e == nil && ino > 0 && !seen[ino] {
			inodes = append(inodes, ino)
			seen[ino] = true
		}
	}
	if len(inodes) == 0 || len(inodes) > 64 {
		return nil, fmt.Errorf("cannot identify bounded listener inode set")
	}
	return inodes, nil
}
func RunTrace(ctx context.Context, o TraceOptions, out, diagnostics io.Writer) error {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		return fmt.Errorf("this initial tracer supports Linux amd64 only")
	}
	if o.Duration%time.Second != 0 || o.Duration < time.Second || o.Duration > 2*time.Hour || o.UDPPort < 1 || o.UDPPort > 65535 || !modulePathPattern.MatchString(o.Module) {
		return fmt.Errorf("invalid trace options (duration 1s..2h, absolute module path)")
	}
	if os.Geteuid() != 0 {
		return fmt.Errorf("kernel tracing requires separately authorized host privileges")
	}
	pids, starts, e := traceWorkers(o.MasterPID)
	if e != nil {
		return e
	}
	selected := []int{}
	selectedStarts := map[int]string{o.MasterPID: starts[o.MasterPID]}
	for _, pid := range pids {
		f, err := os.Open(fmt.Sprintf("/proc/%d/maps", pid))
		if err != nil {
			continue
		}
		data, err := io.ReadAll(io.LimitReader(f, 4<<20))
		f.Close()
		if err != nil {
			continue
		}
		mapped := false
		for _, line := range strings.Split(string(data), "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 6 && fields[len(fields)-1] == o.Module {
				mapped = true
				break
			}
		}
		if mapped {
			selected = append(selected, pid)
			selectedStarts[pid] = starts[pid]
		}
	}
	if len(selected) == 0 {
		return fmt.Errorf("no selected worker maps the specified marker-enabled module")
	}
	pids, starts = selected, selectedStarts
	inodes, e := traceListeners(o.MasterPID, pids)
	if e != nil {
		return e
	}
	// Confirm the module exports the stable ABI before loading any probes.
	if e := verifyMarkerABI(o.Module); e != nil {
		return e
	}
	sessionBytes := make([]byte, 16)
	if _, e = rand.Read(sessionBytes); e != nil {
		return e
	}
	session := hex.EncodeToString(sessionBytes)
	script := traceProgram(o, pids, inodes, session)
	ctx, cancel := context.WithTimeout(ctx, o.Duration+60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bpftrace", "-q", "-e", script)
	cmd.Env = append(os.Environ(), "BPFTRACE_MAX_MAP_KEYS=4096")
	cmd.Stderr = diagnostics
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = 3 * time.Second
	stdout, e := cmd.StdoutPipe()
	if e != nil {
		return e
	}
	udp, e := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: o.UDPPort})
	if e != nil {
		return e
	}
	defer udp.Close()
	if e = cmd.Start(); e != nil {
		return e
	}
	// Stop if the approved process generation changes; do not silently trace a
	// reused PID or follow new workers outside the reviewed scope.
	go func() {
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				_, current, e := traceWorkers(o.MasterPID)
				if e != nil {
					cancel()
					return
				}
				for pid, start := range starts {
					if current[pid] != start {
						cancel()
						return
					}
				}
			}
		}
	}()
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 4096), 4096)
	received, sent, invalid, dropped := 0, 0, 0, 0
	output := traceOutput{writer: out, remaining: 64 << 20, streamOnly: o.streamOnly}
	var outputErr error
	for scanner.Scan() {
		line := scanner.Bytes()
		if string(line) == "TRACE_READY" {
			fmt.Fprintln(diagnostics, "TRACE_READY")
			continue
		}
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		t, e := ValidateTransport(line)
		if e != nil {
			invalid++
			continue
		}
		if t.Session != session || t.Master != o.MasterPID {
			invalid++
			continue
		}
		received++
		if e := output.record(line); e != nil {
			outputErr = e
			cancel()
			break
		}
		udp.SetWriteDeadline(time.Now().Add(10 * time.Millisecond))
		if _, e = udp.Write(line); e != nil {
			dropped++
		} else {
			sent++
		}
	}
	scanErr := scanner.Err()
	if scanErr != nil {
		cancel()
	}
	err := cmd.Wait()
	fmt.Fprintf(diagnostics, "transport summary: decoded=%d sent=%d invalid=%d udp_dropped=%d; BPF output loss/map exhaustion may additionally limit coverage\n", received, sent, invalid, dropped)
	if outputErr != nil {
		return outputErr
	}
	if scanErr != nil {
		return scanErr
	}
	if err != nil {
		return fmt.Errorf("bpftrace failed or target generation changed: %w", err)
	}
	if invalid > 0 {
		return fmt.Errorf("rejected %d malformed tracer records", invalid)
	}
	return nil
}

func traceProgram(o TraceOptions, pids []int, inodes []uint64, session string) string {
	var targets []string
	for _, pid := range pids {
		targets = append(targets, fmt.Sprintf("pid == %d", pid))
	}
	scope := "(" + strings.Join(targets, " || ") + ")"
	var listeners []string
	for _, ino := range inodes {
		listeners = append(listeners, fmt.Sprintf("$ino == %d", ino))
	}
	listen := strings.Join(listeners, " || ")
	// No packet contents, addresses, user buffers or kernel pointers are output.
	prefix := fmt.Sprintf(`{\"schema\":1,\"kind\":\"transport\",\"session\":\"%s\",\"master_pid\":%d,`, session, o.MasterPID)
	emit := func(event, extra, args string) string {
		return fmt.Sprintf(`printf("%s\"host_pid\":%%d,\"fd\":%%d,\"generation\":\"%%llu\",\"at_ns\":\"%%llu\",\"event\":\"%s\"%s}\n", pid,$fd,@gen[pid,$fd],nsecs%s);`, prefix, event, extra, args)
	}
	var b strings.Builder
	b.WriteString(`BEGIN {` + socketTypeSeeds() + `printf("TRACE_READY\n");}` + "\n")

	// Listener socket inode is verified against the selected master's ss ownership.
	fmt.Fprintf(&b, `kprobe:inet_csk_reqsk_queue_hash_add { $sk=(struct sock*)arg0; $so=$sk->sk_socket; if($so!=0){$f=$so->file;if($f!=0){$ino=$f->f_inode->i_ino;if(%s){@syn[(uint64)arg1]=nsecs;}}}}
kprobe:inet_csk_complete_hashdance { $sk=(struct sock*)arg0;$so=$sk->sk_socket;if($so!=0){$f=$so->file;if($f!=0){$ino=$f->f_inode->i_ino;if(%s){@handoff_req[tid,cpu]=(uint64)arg2;@handoff_syn[tid,cpu]=@syn[(uint64)arg2];@nginx_candidate[(uint64)arg1]=(uint64)1;if(!@sk_epoch[(uint64)arg1]){@sk_epoch[(uint64)arg1]=nsecs;}@queued[(uint64)arg1]=nsecs;@qsrc[(uint64)arg1]=(uint64)2;@synchild[(uint64)arg1]=@syn[(uint64)arg2];}}}}
kretprobe:inet_csk_complete_hashdance {delete(@handoff_req[tid,cpu]);delete(@handoff_syn[tid,cpu]);}
kprobe:inet_csk_reqsk_queue_add { $sk=(struct sock*)arg0;$so=$sk->sk_socket;if($so!=0){$f=$so->file;if($f!=0){$ino=$f->f_inode->i_ino;if(%s){@queued[(uint64)arg2]=nsecs;@qsrc[(uint64)arg2]=(uint64)1;@synchild[(uint64)arg2]=(uint64)0;if(@handoff_req[tid,cpu]==(uint64)arg1){@synchild[(uint64)arg2]=@handoff_syn[tid,cpu];}delete(@syn[(uint64)arg1]);}}}}
kprobe:inet_csk_reqsk_queue_drop {delete(@syn[(uint64)arg1]);}
kprobe:sk_free,kprobe:__sk_free {delete(@qsrc[(uint64)arg0]);delete(@queued[(uint64)arg0]);delete(@synchild[(uint64)arg0]);if(@skpid[(uint64)arg0]||@nginx_candidate[(uint64)arg0]){%s}delete(@skpid[(uint64)arg0]);delete(@skfd[(uint64)arg0]);delete(@skgen[(uint64)arg0]);delete(@sk_epoch[(uint64)arg0]);delete(@nginx_candidate[(uint64)arg0]);}
kprobe:inet_csk_accept /%s/ { @accepting[tid]=1; }
kretprobe:inet_csk_accept /%s && @accepting[tid]/ {if(retval!=0){@acceptedsk[tid]=(uint64)retval;@aq[tid]=@queued[(uint64)retval];@aqsrc[tid]=@qsrc[(uint64)retval];delete(@qsrc[(uint64)retval]);@as[tid]=@synchild[(uint64)retval];delete(@queued[(uint64)retval]);delete(@synchild[(uint64)retval]);}delete(@accepting[tid]);}
`, listen, listen, listen, bufferSocketCleanup(), scope, scope)
	for _, name := range []string{"accept4", "accept"} {
		fmt.Fprintf(&b, `tracepoint:syscalls:sys_exit_%s /%s/ { if(args->ret>=0){$fd=(int64)args->ret;@gen[pid,$fd]=nsecs;$sk=@acceptedsk[tid];if($sk){%s}%s}delete(@acceptedsk[tid]);delete(@aq[tid]);delete(@aqsrc[tid]);delete(@as[tid]);}
`, name, scope, socketBinding(), emit("accept", `,\"queued_ns\":\"%llu\",\"syn_ns\":\"%llu\",\"queue_source\":%u`, `,@aq[tid],@as[tid],@aqsrc[tid]`))
	}
	for _, marker := range []string{"begin", "end"} {
		fmt.Fprintf(&b, `uprobe:%s:ngx_inspector_request_%s /%s/ { $fd=(int64)arg4;if($fd>=0 && $fd<16777216){if(!@gen[pid,$fd]){@gen[pid,$fd]=nsecs;}%s}}
`, o.Module, marker, scope, emit("request_"+marker, `,\"request_id\":\"%llu-%llu-%llu-%llu\",\"request_number\":%llu`, `,arg0,arg1,arg2,arg3,arg5`))
	}
	b.WriteString(fmt.Sprintf(`tracepoint:syscalls:sys_enter_epoll_ctl /%s/ {if(args->op==1||args->op==3){@ctl_fd[tid]=(int64)args->fd;@ctl_ep[tid]=(int64)args->epfd;@ctl_data[tid]=*(uint64*)uptr((uint64)args->event+4);@ctl_flags[tid]=*(uint32*)uptr((uint64)args->event);@ctl[tid]=1;}}
tracepoint:syscalls:sys_exit_epoll_ctl /%s && @ctl[tid]/ {if(args->ret==0){$fd=@ctl_fd[tid];@datafd[pid,@ctl_ep[tid],@ctl_data[tid]]=$fd+1;@datagen[pid,@ctl_ep[tid],@ctl_data[tid]]=@gen[pid,$fd];if(@gen[pid,$fd]){%s}}delete(@ctl[tid]);delete(@ctl_fd[tid]);delete(@ctl_ep[tid]);delete(@ctl_data[tid]);delete(@ctl_flags[tid]);}
`, scope, scope, emit("register", `,\"flags\":%u`, `,@ctl_flags[tid]`)))
	// x86_64 packed epoll events are 12 bytes; at most 32 events are inspected per
	// syscall. Excess returned events remain outside this bounded observation.
	for _, name := range []string{"epoll_wait", "epoll_pwait"} {
		fmt.Fprintf(&b, `tracepoint:syscalls:sys_enter_%s /%s/ {@ep_ptr[tid]=(uint64)args->events;@ep_fd[tid]=(int64)args->epfd;}
tracepoint:syscalls:sys_exit_%s /%s/ {$i=0;unroll(32){if($i<args->ret){$ptr=@ep_ptr[tid]+12*$i;$data=*(uint64*)uptr($ptr+4);$f=@datafd[pid,@ep_fd[tid],$data];if($f){$fd=$f-1;if(@gen[pid,$fd] && @datagen[pid,@ep_fd[tid],$data]==@gen[pid,$fd]){%s}}}$i=$i+1;}delete(@ep_ptr[tid]);delete(@ep_fd[tid]);}
`, name, scope, name, scope, emit("ready", `,\"flags\":%u`, `,*(uint32*)uptr($ptr)`))
	}
	for _, name := range []string{"read", "readv", "recvfrom", "recvmsg"} {
		fmt.Fprintf(&b, `tracepoint:syscalls:sys_enter_%s /%s/ {if(@gen[pid,(int64)args->fd]){@read_fd[tid]=(int64)args->fd;@read_at[tid]=nsecs;}}
tracepoint:syscalls:sys_exit_%s /%s && @read_at[tid]/ {$fd=@read_fd[tid];if(args->ret>=0){%s}else{%s}delete(@read_fd[tid]);delete(@read_at[tid]);}
`, name, scope, name, scope, emit("read", fmt.Sprintf(`,\"operation\":\"%s\",\"duration_ns\":\"%%llu\",\"bytes\":%%lld`, name), `,nsecs-@read_at[tid],args->ret`), emit("read_error", fmt.Sprintf(`,\"operation\":\"%s\",\"duration_ns\":\"%%llu\",\"errno\":%%d`, name), `,nsecs-@read_at[tid],-(int64)args->ret`))
	}
	for _, name := range []string{"write", "writev", "sendto", "sendmsg", "sendfile"} {
		probe, fd := name, "fd"
		if name == "sendfile" {
			probe = "sendfile*"
			fd = "out_fd"
		}
		fmt.Fprintf(&b, `tracepoint:syscalls:sys_enter_%s /%s/ {if(@gen[pid,(int64)args->%s]){@write_fd[tid]=(int64)args->%s;@write_at[tid]=nsecs;}}
tracepoint:syscalls:sys_exit_%s /%s && @write_at[tid]/ {$fd=@write_fd[tid];if(args->ret>=0){%s}else{%s}delete(@write_fd[tid]);delete(@write_at[tid]);}
`, probe, scope, fd, fd, probe, scope, emit("write", fmt.Sprintf(`,\"operation\":\"%s\",\"duration_ns\":\"%%llu\",\"bytes\":%%lld`, name), `,nsecs-@write_at[tid],args->ret`), emit("write_error", fmt.Sprintf(`,\"operation\":\"%s\",\"duration_ns\":\"%%llu\",\"errno\":%%d`, name), `,nsecs-@write_at[tid],-(int64)args->ret`))
	}
	b.WriteString(socketTraceProgram(prefix, scope))
	fmt.Fprintf(&b, `tracepoint:syscalls:sys_enter_close /%s/ {$fd=(int64)args->fd;if(@gen[pid,$fd]){%s delete(@fdsk[pid,$fd]);delete(@gen[pid,$fd]);}}
interval:s:%d { exit(); }
END { clear(@syn);clear(@queued);clear(@synchild);clear(@gen);clear(@datafd);clear(@datagen);clear(@handoff_req);clear(@handoff_syn);clear(@aq);clear(@aqsrc);clear(@qsrc);clear(@as);clear(@accepting);clear(@ctl);clear(@ctl_fd);clear(@ctl_ep);clear(@ctl_data);clear(@ctl_flags);clear(@ep_ptr);clear(@ep_fd);clear(@read_fd);clear(@read_at);clear(@write_at);clear(@write_fd);clear(@fdsk);clear(@skpid);clear(@skfd);clear(@skgen);clear(@acceptedsk);clear(@msgsk);clear(@msgfd);clear(@msggen);clear(@rxdepth);clear(@rxsk);clear(@rxat);clear(@rxgen); %s }
`, scope, emit("close", "", ""), int(o.Duration.Seconds()), receiveCPUClear()+bufferClear())
	return b.String()
}

// Read exported symbols directly: production hosts need no binutils installation.
func verifyMarkerABI(path string) error {
	f, err := elf.Open(path)
	if err != nil {
		return fmt.Errorf("cannot read marker module ELF: %w", err)
	}
	defer f.Close()
	symbols, err := f.DynamicSymbols()
	if err != nil {
		return fmt.Errorf("cannot read marker module symbols: %w", err)
	}
	found := map[string]bool{}
	for _, sym := range symbols {
		if sym.Section != elf.SHN_UNDEF && elf.ST_TYPE(sym.Info) == elf.STT_FUNC && (elf.ST_BIND(sym.Info) == elf.STB_GLOBAL || elf.ST_BIND(sym.Info) == elf.STB_WEAK) {
			found[sym.Name] = true
		}
	}
	if !found["ngx_inspector_request_begin"] || !found["ngx_inspector_request_end"] {
		return fmt.Errorf("selected module has no request-correlation marker ABI")
	}
	return nil
}
