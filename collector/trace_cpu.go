package collector

import "fmt"

// An observed on-CPU estimate for the outermost scoped TCP receive callback.
// It excludes intervals bracketed by scheduler and interrupt tracepoints. It
// is not whole-network-stack CPU accounting or exclusive per-request CPU.
func receiveCPUEntry() string {
	return `if(!@rx_cur_depth[cpu]){@rx_cur_tid[cpu]=(uint64)tid;@rx_cpu_acc[cpu]=(uint64)0;@rx_off_acc[cpu]=(uint64)0;@rx_irq_acc[cpu]=(uint64)0;@rx_irq_depth[cpu]=(uint64)0;@rx_off_at[cpu]=(uint64)0;@rx_irq_at[cpu]=(uint64)0;@rx_account[tid,cpu,$d]=(uint64)1;@rx_cur_depth[cpu]=$d;@rx_cpu_at[cpu]=nsecs;}`
}
func receiveCPUProbes() string {
	return `tracepoint:irq:irq_handler_entry,tracepoint:irq:softirq_entry,tracepoint:irq_vectors:*_entry /@rx_cur_depth[cpu] && @rx_cur_tid[cpu]==tid/ {$now=nsecs;if(!@rx_irq_depth[cpu]){if(@rx_cpu_at[cpu]){@rx_cpu_acc[cpu]=@rx_cpu_acc[cpu]+$now-@rx_cpu_at[cpu];@rx_cpu_at[cpu]=(uint64)0;}@rx_irq_at[cpu]=$now;}@rx_irq_depth[cpu]=@rx_irq_depth[cpu]+1;}
tracepoint:irq:irq_handler_exit,tracepoint:irq:softirq_exit,tracepoint:irq_vectors:*_exit /@rx_cur_depth[cpu] && @rx_cur_tid[cpu]==tid && @rx_irq_depth[cpu]/ {$now=nsecs;@rx_irq_depth[cpu]=@rx_irq_depth[cpu]-1;if(!@rx_irq_depth[cpu]){@rx_irq_acc[cpu]=@rx_irq_acc[cpu]+$now-@rx_irq_at[cpu];@rx_irq_at[cpu]=(uint64)0;if(!@rx_off_at[cpu]){@rx_cpu_at[cpu]=$now;}}}
tracepoint:sched:sched_switch /@rx_cur_depth[cpu]/ {$now=nsecs;if((uint64)args->prev_pid==@rx_cur_tid[cpu]&&!@rx_off_at[cpu]){if(@rx_cpu_at[cpu]){@rx_cpu_acc[cpu]=@rx_cpu_acc[cpu]+$now-@rx_cpu_at[cpu];@rx_cpu_at[cpu]=(uint64)0;}@rx_off_at[cpu]=$now;}if((uint64)args->next_pid==@rx_cur_tid[cpu]&&@rx_off_at[cpu]){@rx_off_acc[cpu]=@rx_off_acc[cpu]+$now-@rx_off_at[cpu];@rx_off_at[cpu]=(uint64)0;if(!@rx_irq_depth[cpu]){@rx_cpu_at[cpu]=$now;}}}
`
}
func receiveCPUResult(prefix, event, function string) string {
	common := fmt.Sprintf(`%s\"host_pid\":%%d,\"fd\":%%d,\"generation\":\"%%llu\",\"at_ns\":\"%%llu\",\"event\":\"%s\",\"kernel_function\":\"%s\",\"duration_ns\":\"%%llu\",\"cpu\":%%u`, prefix, event, function)
	basic := fmt.Sprintf(`printf("%s}\n",$owner,$fd,$g,$end,$end-@rxat[tid,cpu,$d],cpu);`, common)
	return fmt.Sprintf(`if(@rx_account[tid,cpu,$d]){if(@rx_cpu_at[cpu]){@rx_cpu_acc[cpu]=@rx_cpu_acc[cpu]+$end-@rx_cpu_at[cpu];} $total=$end-@rxat[tid,cpu,$d];$used=@rx_cpu_acc[cpu]+@rx_off_acc[cpu]+@rx_irq_acc[cpu];if(!@rx_irq_depth[cpu]&&!@rx_off_at[cpu]&&$used<=$total){printf("%s,\"on_cpu_ns\":\"%%llu\",\"off_cpu_ns\":\"%%llu\",\"irq_handler_ns\":\"%%llu\"}\n",$owner,$fd,$g,$end,$total,cpu,@rx_cpu_acc[cpu],@rx_off_acc[cpu],@rx_irq_acc[cpu]);}else{%s}delete(@rx_cur_depth[cpu]);delete(@rx_cur_tid[cpu]);delete(@rx_cpu_at[cpu]);delete(@rx_cpu_acc[cpu]);delete(@rx_off_acc[cpu]);delete(@rx_irq_acc[cpu]);delete(@rx_irq_depth[cpu]);delete(@rx_off_at[cpu]);delete(@rx_irq_at[cpu]);delete(@rx_account[tid,cpu,$d]);}else{%s}`, common, basic, basic)
}
func receiveCPUClear() string {
	return `clear(@rx_cur_depth);clear(@rx_cur_tid);clear(@rx_cpu_at);clear(@rx_cpu_acc);clear(@rx_off_acc);clear(@rx_irq_acc);clear(@rx_irq_depth);clear(@rx_off_at);clear(@rx_irq_at);clear(@rx_account);clear(@recv_cpu);`
}

func kernelReadCPUEntry() string {
	return `@recv_cpu[tid]=(uint64)cpu;$d=(uint64)65535;@rxat[tid,cpu,$d]=@recv_enter[tid];` + receiveCPUEntry()
}
func kernelReadCPUResult(prefix string) string {
	basic := fmt.Sprintf(`printf("%s\"host_pid\":%%d,\"fd\":%%d,\"generation\":\"%%llu\",\"at_ns\":\"%%llu\",\"event\":\"kernel_read\",\"kernel_function\":\"tcp_recvmsg\",\"duration_ns\":\"%%llu\",\"cpu\":%%u}\n",pid,$fd,@gen[pid,$fd],$read_end,$read_end-@recv_enter[tid],cpu);`, prefix)
	clear := `delete(@rx_cur_depth[$origin]);delete(@rx_cur_tid[$origin]);delete(@rx_cpu_at[$origin]);delete(@rx_cpu_acc[$origin]);delete(@rx_off_acc[$origin]);delete(@rx_irq_acc[$origin]);delete(@rx_irq_depth[$origin]);delete(@rx_off_at[$origin]);delete(@rx_irq_at[$origin]);`
	return `$origin=@recv_cpu[tid];$d=(uint64)65535;$owner=(uint64)pid;$g=@gen[pid,$fd];$end=$read_end;if($origin==(uint64)cpu){` + receiveCPUResult(prefix, "kernel_read", "tcp_recvmsg") + `}else{` + basic + `if(@rx_cur_tid[$origin]==tid&&@rx_cur_depth[$origin]==$d){` + clear + `}}delete(@rx_account[tid,$origin,$d]);delete(@rxat[tid,$origin,$d]);delete(@recv_cpu[tid]);`
}

func receiveCPUAbandon() string {
	return `if(@rx_account[tid,cpu,$d]){delete(@rx_cur_depth[cpu]);delete(@rx_cur_tid[cpu]);delete(@rx_cpu_at[cpu]);delete(@rx_cpu_acc[cpu]);delete(@rx_off_acc[cpu]);delete(@rx_irq_acc[cpu]);delete(@rx_irq_depth[cpu]);delete(@rx_off_at[cpu]);delete(@rx_irq_at[cpu]);delete(@rx_account[tid,cpu,$d]);}`
}

func kernelReadCPUAbandon() string {
	return `if(@recv_enter[tid]){$origin=@recv_cpu[tid];$tag=(uint64)65535;if(@rx_cur_tid[$origin]==tid&&@rx_cur_depth[$origin]==$tag&&@rx_account[tid,$origin,$tag]){delete(@rx_cur_depth[$origin]);delete(@rx_cur_tid[$origin]);delete(@rx_cpu_at[$origin]);delete(@rx_cpu_acc[$origin]);delete(@rx_off_acc[$origin]);delete(@rx_irq_acc[$origin]);delete(@rx_irq_depth[$origin]);delete(@rx_off_at[$origin]);delete(@rx_irq_at[$origin]);}delete(@rx_account[tid,$origin,$tag]);delete(@rxat[tid,$origin,$tag]);delete(@recv_cpu[tid]);delete(@recv_enter[tid]);delete(@recv_copied[tid]);}`
}
