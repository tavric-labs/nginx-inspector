package collector

import "fmt"

// Only in-order data that becomes contiguous during this receive callback is
// eligible. No packet contents, sequence numbers, or kernel addresses are output.
func bufferArrivalEntry() string {
	return `$tp=(struct tcp_sock*)$sk;@rx_seq[tid,cpu,$d]=(uint64)$tp->rcv_nxt;@rx_copied[tid,cpu,$d]=(uint64)$tp->copied_seq;@rx_ooo[tid,cpu,$d]=(uint64)$tp->out_of_order_queue.rb_node;@rx_state[tid,cpu,$d]=(uint64)((struct sock*)$sk)->__sk_common.skc_state;`
}
func bufferArrivalReturn() string {
	return `$tp=(struct tcp_sock*)$sk;$first=(uint32)@rx_seq[tid,cpu,$d];$last=$tp->rcv_nxt;$span=(uint32)($last-$first);if(!@rx_ooo[tid,cpu,$d]&&!$tp->out_of_order_queue.rb_node&&@rx_state[tid,cpu,$d]==1&&((struct sock*)$sk)->__sk_common.skc_state==1&&$span>0&&$span<1073741824&&$tp->copied_seq==(uint32)@rx_copied[tid,cpu,$d]){$slot=(uint64)@q_cursor[$sk]%(uint64)8;if(@q_enter[$sk,$slot]){@q_overruns[$sk]=@q_overruns[$sk]+1;}@q_first[$sk,$slot]=(uint64)$first;@q_last[$sk,$slot]=(uint64)$last;@q_enter[$sk,$slot]=@rxat[tid,cpu,$d];@q_visible[$sk,$slot]=$end;@q_gen[$sk,$slot]=$g;@q_cursor[$sk]=$slot+1;}`
}
func bufferReadEntry() string {
	return `@recv_copied[tid]=(uint64)((struct tcp_sock*)$sk)->copied_seq;@recv_enter[tid]=nsecs;`
}
func bufferReadReturn(prefix string) string {
	return fmt.Sprintf(`$tp=(struct tcp_sock*)$sk;$before=(uint32)@recv_copied[tid];$after=$tp->copied_seq;$consumed=(uint32)($after-$before);$matched=(uint64)0;$count=(uint64)0;$low=(uint64)0;$high=(uint64)0;if($consumed>0&&$consumed<1073741824){$i=(uint64)0;unroll(8){$slot=$i;if(@q_enter[$sk,$slot]&&@q_gen[$sk,$slot]==@gen[pid,$fd]){$rs=(int64)(int32)((uint32)@q_first[$sk,$slot]-$before);$re=(int64)(int32)((uint32)@q_last[$sk,$slot]-$before);$lo=$rs>0?$rs:(int64)0;$hi=$re<(int64)$consumed?$re:(int64)$consumed;if($hi>$lo){$a=@q_enter[$sk,$slot];$v=@q_visible[$sk,$slot];$min=@recv_enter[tid]>$v?@recv_enter[tid]-$v:(uint64)0;$max=$read_end-$a;if(!$count||$min<$low){$low=$min;}if($max>$high){$high=$max;}$matched=$matched+(uint64)($hi-$lo);$count=$count+1;}if($re<=(int64)$consumed){delete(@q_enter[$sk,$slot]);delete(@q_first[$sk,$slot]);delete(@q_last[$sk,$slot]);delete(@q_visible[$sk,$slot]);delete(@q_gen[$sk,$slot]);}else{if($rs<(int64)$consumed){@q_first[$sk,$slot]=(uint64)$after;}}}$i=$i+1;}}if($matched){printf("%s\"host_pid\":%%d,\"fd\":%%d,\"generation\":\"%%llu\",\"at_ns\":\"%%llu\",\"event\":\"buffer_residence\",\"bytes\":%%llu,\"residence_min_ns\":\"%%llu\",\"residence_max_ns\":\"%%llu\",\"matched_ranges\":%%llu,\"queue_overruns\":%%llu}\n",pid,$fd,@gen[pid,$fd],$read_end,$matched,$low,$high,$count,@q_overruns[$sk]);}delete(@recv_copied[tid]);delete(@recv_enter[tid]);`, prefix)
}
func bufferFrameCleanup() string {
	return `delete(@rx_seq[tid,cpu,$d]);delete(@rx_copied[tid,cpu,$d]);delete(@rx_ooo[tid,cpu,$d]);delete(@rx_state[tid,cpu,$d]);`
}
func bufferSocketCleanup() string {
	return `$j=(uint64)0;unroll(8){delete(@q_first[(uint64)arg0,$j]);delete(@q_last[(uint64)arg0,$j]);delete(@q_enter[(uint64)arg0,$j]);delete(@q_visible[(uint64)arg0,$j]);delete(@q_gen[(uint64)arg0,$j]);$j=$j+1;}delete(@q_cursor[(uint64)arg0]);delete(@q_overruns[(uint64)arg0]);`
}
func bufferClear() string {
	return `clear(@rx_seq);clear(@rx_copied);clear(@rx_ooo);clear(@rx_state);clear(@q_first);clear(@q_last);clear(@q_enter);clear(@q_visible);clear(@q_gen);clear(@q_cursor);clear(@q_overruns);clear(@recv_copied);clear(@recv_enter);`
}
