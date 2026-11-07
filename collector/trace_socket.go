package collector

import "fmt"

// This reads socket accounting, never skb data, user buffers, or endpoints.
// The caller holds the socket reference while tcp_*msg is executing.
func socketSnapshot(prefix, point string) string {
	return fmt.Sprintf(`$ss=(struct sock*)$sk;$tt=(struct tcp_sock*)$sk;printf("%s\"host_pid\":%%d,\"fd\":%%d,\"generation\":\"%%llu\",\"at_ns\":\"%%llu\",\"event\":\"socket\",\"socket\":{\"point\":\"%s\",\"state\":%%u,\"receive_memory_bytes\":%%d,\"send_queued_memory_bytes\":%%d,\"unread_sequence_units\":%%u,\"srtt_us\":%%u,\"cwnd_segments\":%%u,\"packets_out\":%%u,\"total_retransmits\":%%u,\"send_window_bytes\":%%u,\"receive_window_bytes\":%%u}}\n",pid,$fd,@gen[pid,$fd],nsecs,$ss->__sk_common.skc_state,$ss->sk_backlog.rmem_alloc.counter,$ss->sk_wmem_queued,(uint32)($tt->rcv_nxt-$tt->copied_seq),$tt->srtt_us>>3,$tt->snd_cwnd,$tt->packets_out,$tt->total_retrans,$tt->snd_wnd,$tt->rcv_wnd);`, prefix, point)
}
func socketBinding() string {
	return `@fdsk[pid,$fd]=$sk;@skpid[$sk]=(uint64)pid;@skfd[$sk]=$fd;@skgen[$sk]=@gen[pid,$fd];`
}
func socketTraceProgram(prefix, scope string) string {
	var b string
	for _, v := range []struct{ name, scopeMap, fdMap, point string }{{"tcp_recvmsg", "read_at", "read_fd", "recvmsg_return"}, {"tcp_sendmsg", "write_at", "write_fd", "sendmsg_return"}} {
		b += fmt.Sprintf(`kprobe:%s /%s && @%s[tid]/ {$fd=@%s[tid];$sk=(uint64)arg0;%s @msgsk[tid]=$sk;@msgfd[tid]=$fd;@msggen[tid]=@gen[pid,$fd];}
kretprobe:%s /%s && @msgsk[tid]/ {$fd=@msgfd[tid];$sk=@msgsk[tid];if(@gen[pid,$fd]==@msggen[tid]){%s}delete(@msgsk[tid]);delete(@msgfd[tid]);delete(@msggen[tid]);}
`, v.name, scope, v.scopeMap, v.fdMap, socketBinding(), v.name, scope, socketSnapshot(prefix, v.point))
	}
	// Receive callbacks may execute in softirq context, outside the owning worker.
	// Track nesting only while a selected socket's call is active. Every return
	// pops its own depth; an unselected nested call cannot consume an outer frame.
	b += fmt.Sprintf(`kprobe:tcp_rcv_established {$sk=(uint64)arg0;$d=(uint64)@rxdepth[tid,cpu];if($d>0||@skpid[$sk]){$d=$d+1;@rxdepth[tid,cpu]=$d;if($d<=8&&@skpid[$sk]){@rxsk[tid,cpu,$d]=$sk;@rxat[tid,cpu,$d]=nsecs;@rxgen[tid,cpu,$d]=@skgen[$sk];}}}
kretprobe:tcp_rcv_established {$d=(uint64)@rxdepth[tid,cpu];if($d>0){if($d<=8&&@rxat[tid,cpu,$d]){$sk=@rxsk[tid,cpu,$d];$owner=@skpid[$sk];$fd=@skfd[$sk];$g=@rxgen[tid,cpu,$d];if($owner&&$g&&@skgen[$sk]==$g){printf("%s\"host_pid\":%%d,\"fd\":%%d,\"generation\":\"%%llu\",\"at_ns\":\"%%llu\",\"event\":\"network_rx\",\"duration_ns\":\"%%llu\",\"cpu\":%%u}\n",$owner,$fd,$g,nsecs,nsecs-@rxat[tid,cpu,$d],cpu);}delete(@rxsk[tid,cpu,$d]);delete(@rxat[tid,cpu,$d]);delete(@rxgen[tid,cpu,$d]);}if($d==1){delete(@rxdepth[tid,cpu]);}else{@rxdepth[tid,cpu]=$d-1;}}}
`, prefix)
	// Historical socket identity survives descriptor close until sk_free. This
	// retains shutdown transitions without joining a reused FD to the old socket.
	for _, ev := range []struct{ probe, event, extra, args string }{
		{"sock:inet_sock_set_state", "tcp_state", `,\"from_state\":%u,\"to_state\":%u`, `,args->oldstate,args->newstate`},
		{"tcp:tcp_retransmit_skb", "tcp_retransmit", "", ""},
	} {
		b += fmt.Sprintf(`tracepoint:%s {$sk=(uint64)args->skaddr;$owner=@skpid[$sk];$fd=@skfd[$sk];$g=@skgen[$sk];if($owner&&$g){printf("%s\"host_pid\":%%d,\"fd\":%%d,\"generation\":\"%%llu\",\"at_ns\":\"%%llu\",\"event\":\"%s\"%s}\n",$owner,$fd,$g,nsecs%s);}}
`, ev.probe, prefix, ev.event, ev.extra, ev.args)
	}
	return b
}
