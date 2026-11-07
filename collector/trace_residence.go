package collector

import "fmt"

// Only in-order data that becomes contiguous during this receive callback is
// eligible. No packet contents, sequence numbers, or kernel addresses are output.
func bufferArrivalEntry() string {
	return `$tp=(struct tcp_sock*)$sk;@rx_seq[tid,cpu,$d]=(uint64)$tp->rcv_nxt;@rx_copied[tid,cpu,$d]=(uint64)$tp->copied_seq;@rx_ooo[tid,cpu,$d]=(uint64)$tp->out_of_order_queue.rb_node;@rx_state[tid,cpu,$d]=(uint64)((struct sock*)$sk)->__sk_common.skc_state;`
}
func bufferArrivalReturn() string {
	return `$tp=(struct tcp_sock*)$sk;$first=(uint32)@rx_seq[tid,cpu,$d];$last=$tp->rcv_nxt;$span=(uint32)($last-$first);if(!@rx_ooo[tid,cpu,$d]&&(uint64)$tp->out_of_order_queue.rb_node==0&&@rx_state[tid,cpu,$d]==1&&((struct sock*)$sk)->__sk_common.skc_state==1&&$span>0&&$span<1073741824&&$tp->copied_seq==(uint32)@rx_copied[tid,cpu,$d]){$id=(uint64)@q_cursor[$sk]+1;$slot=$id%(uint64)8;$old=@q_slots[$sk,$slot];$oldrange=@q_ranges[$sk,$old];if($old&&$oldrange.4==$g){@q_overruns[$sk]=@q_overruns[$sk]+1;}if($old){delete(@q_ranges[$sk,$old]);}@q_ranges[$sk,$id]=((uint64)$first,(uint64)$last,@rxat[tid,cpu,$d],$end,$g);@q_slots[$sk,$slot]=$id;@q_cursor[$sk]=$id;}`
}
func bufferReadEntry() string {
	return `@recv_copied[tid]=(uint64)((struct tcp_sock*)$sk)->copied_seq;@recv_enter[tid]=nsecs;`
}

// Each range value is immutable and published through its ring slot only after
// construction. A reader keeps one coherent tuple even if another CPU replaces
// the slot. Partial reads need no range mutation: overlap uses copied_seq.
func bufferReadReturn(prefix string) string {
	return fmt.Sprintf(`$tp=(struct tcp_sock*)$sk;$before=(uint32)@recv_copied[tid];$after=$tp->copied_seq;$consumed=(uint32)($after-$before);$matched=(uint64)0;$count=(uint64)0;$low=(uint64)0;$high=(uint64)0;if($consumed>0&&$consumed<1073741824){$i=(uint64)0;unroll(8){$slot=$i;$id=@q_slots[$sk,$slot];$range=@q_ranges[$sk,$id];if($id&&$range.2&&$range.3&&$range.4==@gen[pid,$fd]){$rs=(int64)(int32)((uint32)$range.0-$before);$re=(int64)(int32)((uint32)$range.1-$before);$lo=$rs>0?$rs:(int64)0;$hi=$re<(int64)$consumed?$re:(int64)$consumed;if($hi>$lo){$a=$range.2;$v=$range.3;$min=@recv_enter[tid]>$v?@recv_enter[tid]-$v:(uint64)0;$max=$read_end-$a;if(!$count||$min<$low){$low=$min;}if($max>$high){$high=$max;}$matched=$matched+(uint64)($hi-$lo);$count=$count+1;}if($re<=(int64)$consumed){delete(@q_ranges[$sk,$id]);}}$i=$i+1;}}if($matched){printf("%s\"host_pid\":%%d,\"fd\":%%d,\"generation\":\"%%llu\",\"at_ns\":\"%%llu\",\"event\":\"buffer_residence\",\"bytes\":%%llu,\"residence_min_ns\":\"%%llu\",\"residence_max_ns\":\"%%llu\",\"matched_ranges\":%%llu,\"queue_overruns\":%%llu}\n",pid,$fd,@gen[pid,$fd],$read_end,$matched,$low,$high,$count,@q_overruns[$sk]);}delete(@recv_copied[tid]);delete(@recv_enter[tid]);`, prefix)
}
func bufferFrameCleanup() string {
	return `delete(@rx_seq[tid,cpu,$d]);delete(@rx_copied[tid,cpu,$d]);delete(@rx_ooo[tid,cpu,$d]);delete(@rx_state[tid,cpu,$d]);`
}
func bufferSocketCleanup() string {
	return `$j=(uint64)0;unroll(8){$id=@q_slots[(uint64)arg0,$j];if($id){delete(@q_ranges[(uint64)arg0,$id]);}delete(@q_slots[(uint64)arg0,$j]);$j=$j+1;}delete(@q_cursor[(uint64)arg0]);delete(@q_overruns[(uint64)arg0]);`
}
func bufferClear() string {
	return `clear(@rx_seq);clear(@rx_copied);clear(@rx_ooo);clear(@rx_state);clear(@q_slots);clear(@q_ranges);clear(@q_cursor);clear(@q_overruns);clear(@recv_copied);clear(@recv_enter);`
}
