package collector

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestMarkerABIExports(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux ELF shared object")
	}
	cc, err := exec.LookPath("cc")
	if err != nil {
		t.Skip("C compiler unavailable")
	}
	for _, tc := range []struct {
		name, source string
		valid        bool
	}{
		{"markers", "void ngx_inspector_request_begin(void) {} void ngx_inspector_request_end(void) {}", true},
		{"undefined", "extern void ngx_inspector_request_begin(void); void ngx_inspector_request_end(void) { ngx_inspector_request_begin(); }", false},
		{"variables", "int ngx_inspector_request_begin; int ngx_inspector_request_end;", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			src := filepath.Join(dir, "marker.c")
			module := filepath.Join(dir, "marker.so")
			if err := os.WriteFile(src, []byte(tc.source), 0600); err != nil {
				t.Fatal(err)
			}
			if out, err := exec.Command(cc, "-shared", "-fPIC", "-o", module, src).CombinedOutput(); err != nil {
				t.Fatalf("compile: %s: %v", out, err)
			}
			if err := verifyMarkerABI(module); (err == nil) != tc.valid {
				t.Fatalf("valid=%v: %v", tc.valid, err)
			}
		})
	}
}
