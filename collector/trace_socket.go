package collector

import "fmt"

// This reads socket accounting, never skb data, user buffers, or endpoints.
// The caller holds the socket reference while tcp_*msg is executing.
func socketSnapshot(prefix, point string) string {
	return fmt.Sprintf(`$ss=(struct sock*)$sk;$tt=(struct tcp_sock*)$sk;printf("%s\"host_pid\":%%d,\"fd\":%%d,\"generation\":\"%%llu\",\"at_ns\":\"%%llu\",\"event\":\"socket\",\"socket\":{\"point\":\"%s\",\"state\":%%u,\"receive_memory_bytes\":%%d,\"send_queued_memory_bytes\":%%d,\"unread_sequence_units\":%%u,\"srtt_us\":%%u,\"cwnd_segments\":%%u,\"packets_out\":%%u,\"total_retransmits\":%%u,\"send_window_bytes\":%%u,\"receive_window_bytes\":%%u}}\n",pid,$fd,@gen[pid,$fd],nsecs,$ss->__sk_common.skc_state,$ss->sk_backlog.rmem_alloc.counter,$ss->sk_wmem_queued,(uint32)($tt->rcv_nxt-$tt->copied_seq),$tt->srtt_us>>3,$tt->snd_cwnd,$tt->packets_out,$tt->total_retrans,$tt->snd_wnd,$tt->rcv_wnd);`, prefix, point)
}
func socketBinding() string {
	return `@nginx_candidate[$sk]=(uint64)1;if(!@sk_epoch[$sk]){@sk_epoch[$sk]=nsecs;}@fdsk[pid,$fd]=$sk;@skpid[$sk]=(uint64)pid;@skfd[$sk]=$fd;@skgen[$sk]=@gen[pid,$fd];`
}
func socketTraceProgram(prefix, scope string) string {
	var b string
	for _, v := range []struct{ name, scopeMap, fdMap, point string }{{"tcp_recvmsg", "read_at", "read_fd", "recvmsg_return"}, {"tcp_sendmsg", "write_at", "write_fd", "sendmsg_return"}} {
		entry, beforeExit, exit, cleanup := "", "", "", ""
		if v.name == "tcp_recvmsg" {
			entry = bufferReadEntry() + kernelReadCPUEntry()
			beforeExit = kernelReadCPUResult(prefix)
			exit = bufferReadReturn(prefix)
			cleanup = kernelReadCPUAbandon()
		}
		b += fmt.Sprintf(`kprobe:%s /%s && @%s[tid]/ {$fd=@%s[tid];$sk=(uint64)arg0;%s @msgsk[tid]=$sk;@msgfd[tid]=$fd;@msggen[tid]=@gen[pid,$fd];%s}
kretprobe:%s /%s && @msgsk[tid]/ {$read_end=nsecs;$fd=@msgfd[tid];$sk=@msgsk[tid];if(@gen[pid,$fd]==@msggen[tid]){%s %s %s}%s delete(@msgsk[tid]);delete(@msgfd[tid]);delete(@msggen[tid]);}
`, v.name, scope, v.scopeMap, v.fdMap, socketBinding(), entry, v.name, scope, beforeExit, socketSnapshot(prefix, v.point), exit, cleanup)
	}
	for _, function := range []string{"tcp_rcv_established", "tcp_rcv_state_process"} {
		b += fmt.Sprintf(`kprobe:%s {$sk=(uint64)arg0;$d=(uint64)@rxdepth[tid,cpu];if($d>0||@nginx_candidate[$sk]){$d=$d+1;@rxdepth[tid,cpu]=$d;if($d<=8&&@nginx_candidate[$sk]){@rxsk[tid,cpu,$d]=$sk;@rxat[tid,cpu,$d]=nsecs;@rxgen[tid,cpu,$d]=@sk_epoch[$sk];%s if(@skpid[$sk]){%s}}}}
kretprobe:%s {$end=nsecs;$d=(uint64)@rxdepth[tid,cpu];if($d>0){if($d<=8&&@rxat[tid,cpu,$d]){$sk=@rxsk[tid,cpu,$d];$owner=@skpid[$sk];$fd=@skfd[$sk];$g=@skgen[$sk];$epoch=@rxgen[tid,cpu,$d];if($epoch&&@sk_epoch[$sk]==$epoch){%s if($owner&&$g&&$end>=$g){%s}}%s delete(@rxsk[tid,cpu,$d]);delete(@rxat[tid,cpu,$d]);delete(@rxgen[tid,cpu,$d]);%s}if($d==1){delete(@rxdepth[tid,cpu]);}else{@rxdepth[tid,cpu]=$d-1;}}}
`, function, bufferArrivalEntry(), receiveCPUEntry(), function, bufferArrivalReturn(), receiveCPUResult(prefix, "network_rx", function), receiveCPUAbandon(), bufferFrameCleanup())
	}
	b += receiveCPUProbes()
	for _, ev := range []struct{ probe, event, extra, args string }{
		{"sock:inet_sock_set_state", "tcp_state", `,\"from_state\":%u,\"to_state\":%u`, `,args->oldstate,args->newstate`},
		{"tcp:tcp_retransmit_skb", "tcp_retransmit", "", ""},
	} {
		b += fmt.Sprintf(`tracepoint:%s {$sk=(uint64)args->skaddr;$owner=@skpid[$sk];$fd=@skfd[$sk];$g=@skgen[$sk];if($owner&&$g){printf("%s\"host_pid\":%%d,\"fd\":%%d,\"generation\":\"%%llu\",\"at_ns\":\"%%llu\",\"event\":\"%s\"%s}\n",$owner,$fd,$g,nsecs%s);}}
`, ev.probe, prefix, ev.event, ev.extra, ev.args)
	}
	return b
}

// bpftrace 0.20 cannot infer a self-referential map's value type. Seed and
// immediately remove impossible keys before readiness; no real socket/CPU key
// is overwritten and no dummy observations are emitted.
func socketTypeSeeds() string {
	s := `@rxdepth[(uint64)0,(uint64)65536]=(uint64)0;delete(@rxdepth[(uint64)0,(uint64)65536]);`
	for _, name := range []string{"q_cursor", "q_overruns"} {
		s += fmt.Sprintf(`@%s[(uint64)0]=(uint64)0;delete(@%s[(uint64)0]);`, name, name)
	}
	for _, name := range []string{"rx_cur_depth", "rx_cur_tid", "rx_cpu_at", "rx_cpu_acc", "rx_off_acc", "rx_irq_acc", "rx_irq_depth", "rx_off_at", "rx_irq_at"} {
		s += fmt.Sprintf(`@%s[(uint64)65536]=(uint64)0;delete(@%s[(uint64)65536]);`, name, name)
	}
	for _, name := range []string{"rx_seq", "rx_copied", "rx_ooo", "rx_state", "rxsk", "rxat", "rxgen", "rx_account"} {
		s += fmt.Sprintf(`@%s[(uint64)0,(uint64)65536,(uint64)0]=(uint64)0;delete(@%s[(uint64)0,(uint64)65536,(uint64)0]);`, name, name)
	}
	for _, name := range []string{"q_slots"} {
		s += fmt.Sprintf(`@%s[(uint64)0,(uint64)0]=(uint64)0;delete(@%s[(uint64)0,(uint64)0]);`, name, name)
	}
	for _, name := range []string{"recv_copied", "recv_enter", "recv_cpu", "sk_epoch", "nginx_candidate"} {
		s += fmt.Sprintf(`@%s[(uint64)0]=(uint64)0;delete(@%s[(uint64)0]);`, name, name)
	}
	s += `@q_ranges[(uint64)0,(uint64)0]=((uint64)0,(uint64)0,(uint64)0,(uint64)0,(uint64)0);delete(@q_ranges[(uint64)0,(uint64)0]);`
	return s
}
