package collector

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestParseNginxVAndFilterConfigureArgs(t *testing.T) {
	raw := "nginx version: nginx/1.24.0 (Ubuntu)\n" +
		"built by gcc 13.2.0 (Ubuntu 13.2.0-23ubuntu4)\n" +
		"configure arguments: --with-cc-opt='-g -O2 -ffile-prefix-map=/build/nginx=.' --prefix=/usr/share/nginx --conf-path=/etc/nginx/nginx.conf --pid-path=/run/nginx.pid --modules-path=/usr/lib/nginx/modules --with-compat --with-http_ssl_module --add-dynamic-module=/build/other/module\n"
	info := ParseNginxV("/usr/sbin/nginx", raw)
	if info.Version != "1.24.0" || !info.WithCompat || info.ConfPath != "/etc/nginx/nginx.conf" || info.PIDPath != "/run/nginx.pid" || info.ModulesPath != "/usr/lib/nginx/modules" {
		t.Fatalf("unexpected parsed info: %+v", info)
	}
	filtered := FilterModuleConfigureArgs(info.ConfigureArgs, "/opt/src/module")
	joined := strings.Join(filtered, " ")
	if strings.Contains(joined, "/build/other/module") {
		t.Fatalf("did not strip third-party module: %v", filtered)
	}
	if !strings.Contains(joined, "--add-dynamic-module=/opt/src/module") || !strings.Contains(joined, "--with-compat") {
		t.Fatalf("missing expected module configure flags: %v", filtered)
	}
}

func TestUpdateNginxConfigContentFreshAndExisting(t *testing.T) {
	fresh := "worker_processes auto;\nevents { worker_connections 1024; }\nhttp {\n    include mime.types;\n    server {\n        listen 8080;\n    }\n}\n"
	updated, err := UpdateNginxConfigContent(fresh, "/usr/lib/nginx/modules/ngx_http_inspector_g1_module.so", "/var/lib/nginx-inspector/control", 19876, 10000)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"load_module /usr/lib/nginx/modules/ngx_http_inspector_g1_module.so;",
		"inspector on;",
		"inspector_control /var/lib/nginx-inspector/control;",
		"inspector_port 19876;",
		"inspector_sample 10000;",
	} {
		if !strings.Contains(updated, want) {
			t.Fatalf("missing %q in:\n%s", want, updated)
		}
	}

	reloaded, err := UpdateNginxConfigContent(updated, "/usr/lib/nginx/modules/ngx_http_inspector_g2_module.so", "/var/lib/nginx-inspector/control", 19876, 10000)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(reloaded, "load_module") != 1 || !strings.Contains(reloaded, "ngx_http_inspector_g2_module.so") {
		t.Fatalf("expected single updated load_module line:\n%s", reloaded)
	}
	if strings.Count(reloaded, "inspector_control") != 1 {
		t.Fatalf("expected single inspector_control line:\n%s", reloaded)
	}

	if _, err := UpdateNginxConfigContent(fresh, "/bad;path.so", "/var/lib/nginx-inspector/control", 19876, 10000); err == nil {
		t.Fatal("accepted semicolon in module path")
	}
}

func TestSetupReloadRollbackAndDoctor(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell script mock")
	}
	base := t.TempDir()
	_ = os.Chmod(base, 0755)
	fakeBin := filepath.Join(base, "fake-nginx")
	confPath := filepath.Join(base, "nginx.conf")
	controlPath := filepath.Join(base, "control", "control")
	dbPath := filepath.Join(base, "data", "events.sqlite3")
	modulesDir := filepath.Join(base, "modules")
	srcSO := filepath.Join(base, "source.so")

	if err := os.WriteFile(srcSO, []byte("ELF-mock-so"), 0644); err != nil {
		t.Fatal(err)
	}
	initialConf := "worker_processes 1;\nevents { worker_connections 64; }\nhttp {\n    server { listen 18080; }\n}\n"
	if err := os.WriteFile(confPath, []byte(initialConf), 0644); err != nil {
		t.Fatal(err)
	}

	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"-V\" ]; then\n" +
		"  echo \"nginx version: nginx/1.24.0\" >&2\n" +
		"  echo \"configure arguments: --with-compat --conf-path=" + confPath + " --modules-path=" + modulesDir + "\" >&2\n" +
		"  exit 0\n" +
		"fi\n" +
		"if [ \"$1\" = \"-t\" ]; then\n" +
		"  if [ \"${FAIL_NGINX_T:-0}\" = \"1\" ]; then\n" +
		"    echo \"emerg: mock validation failure\" >&2\n" +
		"    exit 1\n" +
		"  fi\n" +
		"  exit 0\n" +
		"fi\n" +
		"if [ \"$1\" = \"-s\" ] && [ \"$2\" = \"reload\" ]; then\n" +
		"  exit 0\n" +
		"fi\n" +
		"exit 0\n"
	if err := os.WriteFile(fakeBin, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}

	res, err := SetupOrReload(SetupOptions{
		NginxBin:    fakeBin,
		NginxConf:   confPath,
		ControlFile: controlPath,
		DBFile:      dbPath,
		ModuleSO:    srcSO,
		ModulesDir:  modulesDir,
		Generation:  "r20261107",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Reloaded || res.ModuleSymbol != "ngx_http_inspector_r20261107_module" {
		t.Fatalf("unexpected setup result: %+v", res)
	}
	st, err := os.Stat(controlPath)
	if err != nil || st.Size() != 8 {
		t.Fatalf("control file not 8 bytes: %v %v", st, err)
	}

	doc := Doctor(DoctorOptions{
		NginxBin:    fakeBin,
		NginxConf:   confPath,
		ControlFile: controlPath,
	})
	if !doc.OK {
		t.Fatalf("expected doctor OK, got: %+v", doc)
	}

	// Verify automatic config rollback if nginx -t fails on a subsequent update.
	goodConfBytes, _ := os.ReadFile(confPath)
	t.Setenv("FAIL_NGINX_T", "1")
	_, err = SetupOrReload(SetupOptions{
		NginxBin:    fakeBin,
		NginxConf:   confPath,
		ControlFile: controlPath,
		DBFile:      dbPath,
		ModuleSO:    srcSO,
		ModulesDir:  modulesDir,
		Generation:  "r20261108",
	})
	if err == nil {
		t.Fatal("expected error when nginx -t fails")
	}
	afterRollback, _ := os.ReadFile(confPath)
	if string(afterRollback) != string(goodConfBytes) {
		t.Fatalf("config was not rolled back after nginx -t failure:\n%s", string(afterRollback))
	}
}
