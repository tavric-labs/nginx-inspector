//go:build linux

package collector

import "testing"

func TestSchedulingAndStatusParsers(t *testing.T) {
	p := WorkerProcess{}
	parseMemory("VmData: 4096 kB\nvoluntary_ctxt_switches: 20\nnonvoluntary_ctxt_switches: 3\nVmRSS: 12 invalid\n", &p)
	if p.Data == nil || *p.Data != 4194304 || p.VoluntarySwitches == nil || *p.VoluntarySwitches != 20 || *p.Preemptions != 3 || p.RSS != nil {
		t.Fatalf("wrong status units: %+v", p)
	}
	if s := parseScheduling("1000000 2000000 12\n"); s == nil || s.RuntimeNS != 1000000 || s.RunqueueNS != 2000000 || s.Slices != 12 {
		t.Fatal(s)
	}
	for _, bad := range []string{"0 0 0", "-1 0 1", "n/a 2 3", "1 2", "1 2 3 4"} {
		if parseScheduling(bad) != nil {
			t.Fatal("accepted", bad)
		}
	}
}
