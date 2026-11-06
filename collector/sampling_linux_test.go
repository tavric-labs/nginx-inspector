//go:build linux

package collector

import (
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestPackedLeaseDefaultsAndSamplingBounds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control")
	c, e := NewControl(path)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	state, e := c.Start(nil)
	if e != nil || state.Sample != 100 {
		t.Fatal(state, e)
	}
	raw, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	word := binary.NativeEndian.Uint64(raw)
	if word>>48 != 100 || word&0x0000ffffffffffff <= monotonicMS() {
		t.Fatal("incorrect packed deadline", word)
	}
	state, e = c.StartSample(nil, 10000)
	if e != nil || state.Sample != 10000 {
		t.Fatal(state, e)
	}
	for _, sample := range []int{0, -1, 10001} {
		if _, e = c.StartSample(nil, sample); e == nil {
			t.Fatal("accepted invalid sample", sample)
		}
	}
	c.Stop()
	raw, _ = os.ReadFile(path)
	if binary.NativeEndian.Uint64(raw) != 0 {
		t.Fatal("stop did not clear full lease")
	}
}
func TestConfiguredMasterRejectsForeignNativeEvents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nginx.pid")
	os.WriteFile(path, []byte("42\n"), 0600)
	m := newWorkerMonitor("/proc", path)
	event := Event{"native": map[string]any{"nginx": map[string]any{"master_pid": json.Number("42")}}}
	if !m.accepts(event) {
		t.Fatal("current master not recognized before OS sample")
	}
	event["native"].(map[string]any)["nginx"].(map[string]any)["master_pid"] = json.Number("43")
	if m.accepts(event) {
		t.Fatal("foreign nginx master accepted")
	}
}
