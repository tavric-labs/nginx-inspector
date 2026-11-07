package collector

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

type NginxInfo struct {
	Binary        string   `json:"binary"`
	Version       string   `json:"version"`
	ConfigureArgs []string `json:"configure_args"`
	WithCompat    bool     `json:"with_compat"`
	ConfPath      string   `json:"conf_path"`
	PIDPath       string   `json:"pid_path"`
	ModulesPath   string   `json:"modules_path"`
	Prefix        string   `json:"prefix"`
}

type DoctorCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
	Fix    string `json:"fix,omitempty"`
}

type DoctorOptions struct {
	NginxBin     string
	NginxConf    string
	ControlFile  string
	NginxPIDFile string
	ProcRoot     string
	CollectorURL string
}

type DoctorReport struct {
	OK         bool              `json:"ok"`
	Privileges map[string]string `json:"privileges"`
	Nginx      *NginxInfo        `json:"nginx,omitempty"`
	Checks     []DoctorCheck     `json:"checks"`
	Commands   map[string]string `json:"commands"`
}

type SetupOptions struct {
	NginxBin      string
	NginxConf     string
	ControlFile   string
	DBFile        string
	ModuleSO      string
	ModuleSource  string
	NginxSource   string
	ModulesDir    string
	Generation    string
	CollectorUser string
	UDPPort       int
	MaxSample     int
	BuildModule   bool
	DryRun        bool
	SkipReload    bool
}

type SetupResult struct {
	Status         string            `json:"status"`
	DryRun         bool              `json:"dry_run"`
	NginxVersion   string            `json:"nginx_version,omitempty"`
	ModuleSymbol   string            `json:"module_symbol"`
	ModuleSO       string            `json:"module_so"`
	MarkerABI      bool              `json:"marker_abi"`
	NginxConf      string            `json:"nginx_conf"`
	ControlFile    string            `json:"control_file"`
	DBFile         string            `json:"db_file"`
	UDPPort        int               `json:"udp_port"`
	MaxSample      int               `json:"max_sample_permyriad"`
	Reloaded       bool              `json:"reloaded"`
	PrivilegesUsed []string          `json:"privileges_used"`
	Commands       map[string]string `json:"commands"`
}

var (
	moduleIdentPattern = regexp.MustCompile(`^ngx_http_inspector_[a-zA-Z0-9_]+_module$`)
	loadModulePattern  = regexp.MustCompile(`(?m)^\s*load_module\s+([^\s;]*ngx_http_inspector[^\s;]*\.so)\s*;\s*$`)
	directivePatterns  = map[string]*regexp.Regexp{
		"inspector":         regexp.MustCompile(`(?m)^(\s*)inspector\s+(?:on|off)\s*;`),
		"inspector_control": regexp.MustCompile(`(?m)^(\s*)inspector_control\s+[^;\n]+;`),
		"inspector_port":    regexp.MustCompile(`(?m)^(\s*)inspector_port\s+\d+\s*;`),
		"inspector_sample":  regexp.MustCompile(`(?m)^(\s*)inspector_sample\s+\d+\s*;`),
	}
)

func PrivilegeSummary() map[string]string {
	return map[string]string{
		"collector_serve":          "Unprivileged user; needs write access to --db directory (0700) and --control-file (0644); zero Linux capabilities required",
		"nginx_workers":            "Nginx worker user needs directory traversal (+x, 0755) and read access (0644) on --control-file before workers start or reload",
		"worker_os_sampling":       "Read access to --proc-root (/proc) and --nginx-pid-file; if /proc uses hidepid=2, run collector in nginx group or permitted proc gid",
		"nginx_install_and_reload": "Root (sudo) required only when copying module .so into system nginx modules path, editing system nginx.conf, and running nginx -t and nginx -s reload",
		"kernel_transport_trace":   "Root (sudo) + bpftrace, ss, and /sys/kernel/btf/vmlinux required only for opt-in kernel/socket transport tracing (nginx-inspector start --transport)",
	}
}

func splitConfigureArgs(raw string) []string {
	var args []string
	var cur strings.Builder
	var quote rune
	for _, r := range raw {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote = r
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			if cur.Len() > 0 {
				args = append(args, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		args = append(args, cur.String())
	}
	return args
}

func ParseNginxV(binary, output string) NginxInfo {
	info := NginxInfo{
		Binary:      binary,
		ConfPath:    "/etc/nginx/nginx.conf",
		PIDPath:     "/run/nginx.pid",
		ModulesPath: "/usr/lib/nginx/modules",
		Prefix:      "/etc/nginx",
	}
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "nginx version:") {
			parts := strings.SplitN(line, "nginx/", 2)
			if len(parts) == 2 {
				info.Version = strings.Fields(parts[1])[0]
			}
		}
		if strings.HasPrefix(line, "configure arguments:") {
			raw := strings.TrimSpace(strings.TrimPrefix(line, "configure arguments:"))
			info.ConfigureArgs = splitConfigureArgs(raw)
		}
	}
	for _, arg := range info.ConfigureArgs {
		switch {
		case arg == "--with-compat":
			info.WithCompat = true
		case strings.HasPrefix(arg, "--conf-path="):
			info.ConfPath = strings.TrimPrefix(arg, "--conf-path=")
		case strings.HasPrefix(arg, "--pid-path="):
			info.PIDPath = strings.TrimPrefix(arg, "--pid-path=")
		case strings.HasPrefix(arg, "--modules-path="):
			info.ModulesPath = strings.TrimPrefix(arg, "--modules-path=")
		case strings.HasPrefix(arg, "--prefix="):
			info.Prefix = strings.TrimPrefix(arg, "--prefix=")
		}
	}
	return info
}

func InspectNginx(nginxBin string) (NginxInfo, error) {
	if nginxBin == "" {
		nginxBin = "nginx"
	}
	path, err := exec.LookPath(nginxBin)
	if err != nil {
		return NginxInfo{Binary: nginxBin}, err
	}
	cmd := exec.Command(path, "-V")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return NginxInfo{Binary: path}, fmt.Errorf("nginx -V failed: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return ParseNginxV(path, string(out)), nil
}

func DiscoverNginxMasterPID(pidFile, nginxBin string) (int, error) {
	candidates := []string{}
	if pidFile != "" {
		candidates = append(candidates, pidFile)
	}
	if info, err := InspectNginx(nginxBin); err == nil && info.PIDPath != "" {
		candidates = append(candidates, info.PIDPath)
	}
	candidates = append(candidates, "/run/nginx.pid", "/var/run/nginx.pid", "/tmp/inspector-nginx.pid")
	for _, path := range candidates {
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
		if err == nil && pid > 0 {
			return pid, nil
		}
	}
	if runtime.GOOS == "linux" {
		view := readWorkerProcesses("/proc", "")
		for _, p := range view.Processes {
			if p.Role == "master" && p.PID > 0 {
				return p.PID, nil
			}
		}
	}
	return 0, fmt.Errorf("could not auto-detect nginx master PID; pass --master-pid")
}

func DiscoverNginxModuleSO(nginxConf, nginxBin string, masterPID int) (string, error) {
	if runtime.GOOS == "linux" && masterPID > 0 {
		v := readWorkerProcesses("/proc", "")
		var pids []int
		for _, p := range v.Processes {
			if p.Parent == masterPID && p.Role == "worker" {
				pids = append(pids, p.PID)
			}
		}
		pids = append(pids, masterPID)
		for _, pid := range pids {
			b, err := os.ReadFile(fmt.Sprintf("/proc/%d/maps", pid))
			if err != nil {
				continue
			}
			for _, line := range strings.Split(string(b), "\n") {
				fields := strings.Fields(line)
				if len(fields) >= 6 {
					p := fields[len(fields)-1]
					if strings.Contains(filepath.Base(p), "ngx_http_inspector") && strings.HasSuffix(p, ".so") {
						return p, nil
					}
				}
			}
		}
	}
	confPath := nginxConf
	if confPath == "" {
		if info, err := InspectNginx(nginxBin); err == nil && info.ConfPath != "" {
			confPath = info.ConfPath
		} else {
			confPath = "/etc/nginx/nginx.conf"
		}
	}
	if b, err := os.ReadFile(confPath); err == nil {
		if m := loadModulePattern.FindStringSubmatch(string(b)); len(m) == 2 {
			if abs, err := filepath.Abs(m[1]); err == nil {
				return abs, nil
			}
			return m[1], nil
		}
	}
	if def := discoverDefaultModuleSO(); def != "" {
		if abs, err := filepath.Abs(def); err == nil {
			return abs, nil
		}
		return def, nil
	}
	return "", fmt.Errorf("could not auto-detect loaded inspector module .so; pass --module")
}

func ResolveCollectorOwner(explicitUser string) (int, int, error) {
	if explicitUser != "" {
		u, err := user.Lookup(explicitUser)
		if err != nil {
			return -1, -1, fmt.Errorf("lookup user %q: %w", explicitUser, err)
		}
		uid, err := strconv.Atoi(u.Uid)
		if err != nil {
			return -1, -1, err
		}
		gid, err := strconv.Atoi(u.Gid)
		if err != nil {
			return -1, -1, err
		}
		return uid, gid, nil
	}
	if sudoUID := os.Getenv("SUDO_UID"); sudoUID != "" {
		uid, err1 := strconv.Atoi(sudoUID)
		gid, err2 := strconv.Atoi(os.Getenv("SUDO_GID"))
		if err1 == nil && err2 == nil && uid >= 0 && gid >= 0 {
			return uid, gid, nil
		}
	}
	return os.Geteuid(), os.Getegid(), nil
}

func CheckParentTraversal(path string) (string, bool) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return path, false
	}
	dir := filepath.Dir(abs)
	for {
		info, err := os.Stat(dir)
		if err == nil && runtime.GOOS != "windows" {
			if info.Mode().Perm()&0001 == 0 {
				return dir, false
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", true
}

func PrepareControlFile(controlPath string, uid, gid int) error {
	if controlPath == "" {
		return fmt.Errorf("control file path is required")
	}
	abs, err := filepath.Abs(controlPath)
	if err != nil {
		return err
	}
	dir := filepath.Dir(abs)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0755); err != nil {
		return err
	}
	if info, err := os.Lstat(abs); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("control path %s must be a regular file", abs)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	f, err := os.OpenFile(abs, os.O_RDWR|os.O_CREATE, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if st.Size() != 8 {
		if err := f.Truncate(8); err != nil {
			return err
		}
	}
	if err := f.Chmod(0644); err != nil {
		return err
	}
	if os.Geteuid() == 0 && uid >= 0 && gid >= 0 {
		_ = os.Chown(dir, uid, gid)
		if err := f.Chown(uid, gid); err != nil {
			return err
		}
	}
	return nil
}

func PrepareDBDirectory(dbPath string, uid, gid int) error {
	if dbPath == "" {
		return nil
	}
	abs, err := filepath.Abs(dbPath)
	if err != nil {
		return err
	}
	dir := filepath.Dir(abs)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	if os.Geteuid() == 0 && uid >= 0 && gid >= 0 {
		if err := os.Chown(dir, uid, gid); err != nil {
			return err
		}
		if _, err := os.Stat(abs); err == nil {
			_ = os.Chown(abs, uid, gid)
		}
	}
	return nil
}

func validateConfigPathValue(label, val string) error {
	if strings.TrimSpace(val) == "" {
		return fmt.Errorf("%s cannot be empty", label)
	}
	if strings.ContainsAny(val, ";\r\n\"'") {
		return fmt.Errorf("%s contains unsupported characters", label)
	}
	return nil
}

func UpdateNginxConfigContent(confText, moduleSOPath, controlFile string, udpPort, maxSample int) (string, error) {
	if err := validateConfigPathValue("module path", moduleSOPath); err != nil {
		return "", err
	}
	if err := validateConfigPathValue("control file", controlFile); err != nil {
		return "", err
	}
	if udpPort < 1024 || udpPort > 65535 {
		return "", fmt.Errorf("udp port must be 1024..65535")
	}
	if maxSample < 1 || maxSample > 10000 {
		return "", fmt.Errorf("max sample must be 1..10000")
	}

	loadLine := fmt.Sprintf("load_module %s;", moduleSOPath)
	updated := confText
	if loadModulePattern.MatchString(updated) {
		updated = loadModulePattern.ReplaceAllString(updated, loadLine)
	} else {
		updated = loadLine + "\n" + updated
	}

	values := map[string]string{
		"inspector":         "inspector on;",
		"inspector_control": fmt.Sprintf("inspector_control %s;", controlFile),
		"inspector_port":    fmt.Sprintf("inspector_port %d;", udpPort),
		"inspector_sample":  fmt.Sprintf("inspector_sample %d;", maxSample),
	}
	var missing []string
	for _, key := range []string{"inspector", "inspector_control", "inspector_port", "inspector_sample"} {
		re := directivePatterns[key]
		if re.MatchString(updated) {
			updated = re.ReplaceAllString(updated, "${1}"+values[key])
		} else {
			missing = append(missing, values[key])
		}
	}
	if len(missing) > 0 {
		httpOpen := regexp.MustCompile(`(?m)\bhttp\s*\{`)
		loc := httpOpen.FindStringIndex(updated)
		if loc == nil {
			return "", fmt.Errorf("nginx configuration does not contain an http { ... } block")
		}
		var block strings.Builder
		block.WriteString("\n")
		for _, line := range missing {
			block.WriteString("    ")
			block.WriteString(line)
			block.WriteString("\n")
		}
		insertAt := loc[1]
		updated = updated[:insertAt] + block.String() + updated[insertAt:]
	}
	return updated, nil
}

func ModuleGenerationName(moduleSrcDir, explicit string) (string, error) {
	if explicit != "" {
		if !strings.HasPrefix(explicit, "ngx_http_inspector_") {
			explicit = "ngx_http_inspector_" + explicit + "_module"
		}
		if !moduleIdentPattern.MatchString(explicit) {
			return "", fmt.Errorf("invalid module generation identifier %q", explicit)
		}
		return explicit, nil
	}
	h := sha256.New()
	for _, name := range []string{"ngx_http_inspector_module.c", "inspector_native_json.h", "config"} {
		if moduleSrcDir != "" {
			if b, err := os.ReadFile(filepath.Join(moduleSrcDir, name)); err == nil {
				h.Write(b)
			}
		}
	}
	fmt.Fprintf(h, ":%d", time.Now().UTC().Unix())
	sum := hex.EncodeToString(h.Sum(nil))[:10]
	return "ngx_http_inspector_g" + sum + "_module", nil
}

func FilterModuleConfigureArgs(args []string, absModuleDir string) []string {
	var out []string
	hasCompat := false
	for _, arg := range args {
		if strings.HasPrefix(arg, "--add-module=") || strings.HasPrefix(arg, "--add-dynamic-module=") {
			continue
		}
		if arg == "--with-compat" {
			hasCompat = true
		}
		out = append(out, arg)
	}
	if !hasCompat {
		out = append(out, "--with-compat")
	}
	out = append(out, "--add-dynamic-module="+absModuleDir)
	return out
}

func BuildNginxModule(nginxSrcDir, moduleSrcDir, moduleName string, info NginxInfo) (string, error) {
	if !moduleIdentPattern.MatchString(moduleName) {
		return "", fmt.Errorf("invalid module name %q", moduleName)
	}
	absModule, err := filepath.Abs(moduleSrcDir)
	if err != nil {
		return "", err
	}
	absNginxSrc, err := filepath.Abs(nginxSrcDir)
	if err != nil {
		return "", err
	}
	configurePath := filepath.Join(absNginxSrc, "configure")
	if _, err := os.Stat(configurePath); err != nil {
		return "", fmt.Errorf("nginx source configure script not found at %s", configurePath)
	}
	cfgArgs := FilterModuleConfigureArgs(info.ConfigureArgs, absModule)
	cfgCmd := exec.Command("./configure", cfgArgs...)
	cfgCmd.Dir = absNginxSrc
	cfgCmd.Env = append(os.Environ(), "NGINX_INSPECTOR_MODULE_NAME="+moduleName)
	if out, err := cfgCmd.CombinedOutput(); err != nil {
		// Fall back to minimal --with-compat if vendor flags reference missing build-host paths.
		fallback := exec.Command("./configure", "--with-compat", "--add-dynamic-module="+absModule)
		fallback.Dir = absNginxSrc
		fallback.Env = append(os.Environ(), "NGINX_INSPECTOR_MODULE_NAME="+moduleName)
		if out2, err2 := fallback.CombinedOutput(); err2 != nil {
			return "", fmt.Errorf("configure module failed: %w (%s / %s)", err, strings.TrimSpace(string(out)), strings.TrimSpace(string(out2)))
		}
	}
	makeCmd := exec.Command("make", "-f", "objs/Makefile", "modules", "-j2")
	makeCmd.Dir = absNginxSrc
	if out, err := makeCmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("make modules failed: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	built := filepath.Join(absNginxSrc, "objs", moduleName+".so")
	if _, err := os.Stat(built); err != nil {
		return "", fmt.Errorf("built module not found at %s", built)
	}
	return built, nil
}

func installImmutableSO(srcSO, destDir, fileName string) (string, error) {
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return "", err
	}
	destPath := filepath.Join(destDir, fileName)
	if filepath.Clean(srcSO) == filepath.Clean(destPath) {
		return destPath, nil
	}
	in, err := os.Open(srcSO)
	if err != nil {
		return "", err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(destDir, ".inspector-module-*.so.tmp")
	if err != nil {
		return "", err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Chmod(0644); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmpPath, destPath); err != nil {
		return "", err
	}
	return destPath, nil
}

func discoverDefaultModuleSO() string {
	for _, candidate := range []string{
		"build/transport/ngx_http_inspector_module.so",
		"build/ngx_http_inspector_module.so",
		"/opt/inspector/ngx_http_inspector_module.so",
		"/usr/lib/nginx/modules/ngx_http_inspector_module.so",
	} {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return ""
}

func discoverDefaultNginxSource() string {
	for _, candidate := range []string{
		"build/nginx-src",
		"/workspace/build/nginx-src",
		"/usr/share/nginx/src",
	} {
		if _, err := os.Stat(filepath.Join(candidate, "configure")); err == nil {
			return candidate
		}
	}
	return ""
}

func SetupOrReload(opts SetupOptions) (*SetupResult, error) {
	if opts.NginxBin == "" {
		opts.NginxBin = "nginx"
	}
	if opts.UDPPort == 0 {
		opts.UDPPort = 19876
	}
	if opts.MaxSample == 0 {
		opts.MaxSample = 10000
	}
	if opts.ControlFile == "" {
		opts.ControlFile = "/var/lib/nginx-inspector/control"
	}
	if opts.DBFile == "" {
		opts.DBFile = "/var/lib/inspector-data/events.sqlite3"
	}
	info, nginxErr := InspectNginx(opts.NginxBin)
	if opts.NginxConf == "" {
		if info.ConfPath != "" {
			opts.NginxConf = info.ConfPath
		} else {
			opts.NginxConf = "/etc/nginx/nginx.conf"
		}
	}
	if opts.ModulesDir == "" {
		if info.ModulesPath != "" {
			opts.ModulesDir = info.ModulesPath
		} else {
			opts.ModulesDir = "/usr/lib/nginx/modules"
		}
	}
	if opts.ModuleSource == "" {
		if _, err := os.Stat("module/ngx_http_inspector_module.c"); err == nil {
			opts.ModuleSource = "module"
		}
	}
	if opts.NginxSource == "" {
		opts.NginxSource = discoverDefaultNginxSource()
	}
	if opts.ModuleSO == "" && !opts.BuildModule {
		opts.ModuleSO = discoverDefaultModuleSO()
		if opts.ModuleSO == "" && opts.ModuleSource != "" && opts.NginxSource != "" {
			opts.BuildModule = true
		}
	}

	absControl, err := filepath.Abs(opts.ControlFile)
	if err != nil {
		return nil, err
	}
	absDB, err := filepath.Abs(opts.DBFile)
	if err != nil {
		return nil, err
	}
	absConf, err := filepath.Abs(opts.NginxConf)
	if err != nil {
		return nil, err
	}
	absModulesDir, err := filepath.Abs(opts.ModulesDir)
	if err != nil {
		return nil, err
	}

	modSymbol := "ngx_http_inspector_v2_module"
	destFileName := "ngx_http_inspector_module.so"
	if opts.BuildModule || opts.Generation != "" {
		modSymbol, err = ModuleGenerationName(opts.ModuleSource, opts.Generation)
		if err != nil {
			return nil, err
		}
		destFileName = modSymbol + ".so"
	} else if opts.ModuleSO != "" {
		base := filepath.Base(opts.ModuleSO)
		if strings.HasPrefix(base, "ngx_http_inspector_") && strings.HasSuffix(base, "_module.so") {
			destFileName = base
			modSymbol = strings.TrimSuffix(base, ".so")
		}
	}
	destSO := filepath.Join(absModulesDir, destFileName)

	pidFlag := ""
	if info.PIDPath != "" {
		pidFlag = fmt.Sprintf(" --proc-root /proc --nginx-pid-file %s", info.PIDPath)
	}
	res := &SetupResult{
		Status:       "planned",
		DryRun:       opts.DryRun,
		NginxVersion: info.Version,
		ModuleSymbol: modSymbol,
		ModuleSO:     destSO,
		NginxConf:    absConf,
		ControlFile:  absControl,
		DBFile:       absDB,
		UDPPort:      opts.UDPPort,
		MaxSample:    opts.MaxSample,
		PrivilegesUsed: []string{
			"control file 0644 + traversable 0755 parent directory for nginx workers",
			"collector DB directory 0700 owned by collector user",
			"root/sudo for module installation, nginx -t validation, and nginx -s reload",
		},
		Commands: map[string]string{
			"serve":               fmt.Sprintf("nginx-inspector serve --db %s --control-file %s%s", absDB, absControl, pidFlag),
			"trace_all_30s":       "nginx-inspector start --all --for 30s",
			"trace_transport_30s": fmt.Sprintf("nginx-inspector start --all --transport --for 30s --module %s", destSO),
			"status":              "nginx-inspector status",
			"doctor":              fmt.Sprintf("nginx-inspector doctor --control-file %s --nginx-conf %s", absControl, absConf),
		},
	}
	if opts.DryRun {
		return res, nil
	}
	if nginxErr != nil {
		return nil, nginxErr
	}

	uid, gid, err := ResolveCollectorOwner(opts.CollectorUser)
	if err != nil {
		return nil, err
	}
	if err := PrepareDBDirectory(absDB, uid, gid); err != nil {
		return nil, fmt.Errorf("prepare db directory: %w", err)
	}
	if err := PrepareControlFile(absControl, uid, gid); err != nil {
		return nil, fmt.Errorf("prepare control file: %w", err)
	}

	srcSO := opts.ModuleSO
	if opts.BuildModule {
		if opts.ModuleSource == "" || opts.NginxSource == "" {
			return nil, fmt.Errorf("building module requires --module-source and --nginx-source (or installed nginx-dev headers at /usr/share/nginx/src)")
		}
		srcSO, err = BuildNginxModule(opts.NginxSource, opts.ModuleSource, modSymbol, info)
		if err != nil {
			return nil, err
		}
	}
	if srcSO == "" {
		return nil, fmt.Errorf("no module .so found; supply --module-so or --build-module with --module-source and --nginx-source")
	}
	installedSO, err := installImmutableSO(srcSO, absModulesDir, destFileName)
	if err != nil {
		return nil, fmt.Errorf("install module .so: %w", err)
	}
	res.ModuleSO = installedSO
	res.MarkerABI = verifyMarkerABI(installedSO) == nil
	res.Commands["trace_transport_30s"] = fmt.Sprintf("nginx-inspector start --all --transport --for 30s --module %s", installedSO)

	origConf, err := os.ReadFile(absConf)
	if err != nil {
		return nil, fmt.Errorf("read nginx config %s: %w", absConf, err)
	}
	origMode := os.FileMode(0644)
	if st, err := os.Stat(absConf); err == nil {
		origMode = st.Mode().Perm()
	}
	updatedConf, err := UpdateNginxConfigContent(string(origConf), installedSO, absControl, opts.UDPPort, opts.MaxSample)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(absConf, []byte(updatedConf), origMode); err != nil {
		return nil, fmt.Errorf("write nginx config %s: %w", absConf, err)
	}

	testCmd := exec.Command(info.Binary, "-t", "-c", absConf)
	if out, err := testCmd.CombinedOutput(); err != nil {
		_ = os.WriteFile(absConf, origConf, origMode)
		return nil, fmt.Errorf("nginx -t validation failed (previous config restored): %w (%s)", err, strings.TrimSpace(string(out)))
	}

	if !opts.SkipReload {
		reloadCmd := exec.Command(info.Binary, "-s", "reload", "-c", absConf)
		if out, err := reloadCmd.CombinedOutput(); err != nil {
			_ = os.WriteFile(absConf, origConf, origMode)
			return nil, fmt.Errorf("nginx reload failed (previous config restored): %w (%s)", err, strings.TrimSpace(string(out)))
		}
		res.Reloaded = true
	}
	res.Status = "ok"
	return res, nil
}

func Doctor(opts DoctorOptions) DoctorReport {
	if opts.NginxBin == "" {
		opts.NginxBin = "nginx"
	}
	if opts.ControlFile == "" {
		if _, err := os.Stat("data/control"); err == nil {
			opts.ControlFile = "data/control"
		} else {
			opts.ControlFile = "/var/lib/nginx-inspector/control"
		}
	}
	if opts.ProcRoot == "" && runtime.GOOS == "linux" {
		opts.ProcRoot = "/proc"
	}
	if opts.CollectorURL == "" {
		opts.CollectorURL = "http://127.0.0.1:19877"
	}

	report := DoctorReport{
		OK:         true,
		Privileges: PrivilegeSummary(),
		Checks:     []DoctorCheck{},
		Commands:   map[string]string{},
	}
	add := func(name, status, detail, fix string) {
		if status == "error" {
			report.OK = false
		}
		report.Checks = append(report.Checks, DoctorCheck{Name: name, Status: status, Detail: detail, Fix: fix})
	}

	info, err := InspectNginx(opts.NginxBin)
	if err != nil {
		add("nginx_binary", "warn", fmt.Sprintf("nginx binary %q not inspected: %v", opts.NginxBin, err), "Install nginx or pass --nginx-bin /path/to/nginx")
	} else {
		report.Nginx = &info
		compatNote := "with --with-compat"
		if !info.WithCompat {
			compatNote = "without --with-compat (build module with full configure arguments)"
		}
		add("nginx_binary", "ok", fmt.Sprintf("nginx/%s (%s), conf=%s", info.Version, compatNote, info.ConfPath), "")
	}

	confPath := opts.NginxConf
	if confPath == "" && report.Nginx != nil {
		confPath = report.Nginx.ConfPath
	}
	pidPath := opts.NginxPIDFile
	if pidPath == "" && report.Nginx != nil {
		pidPath = report.Nginx.PIDPath
	}

	absControl, _ := filepath.Abs(opts.ControlFile)
	if st, err := os.Lstat(absControl); err != nil {
		add("control_file", "error", fmt.Sprintf("control file %s is missing (%v)", absControl, err), fmt.Sprintf("Run: nginx-inspector setup --control-file %s (or start collector before reloading nginx)", absControl))
	} else if !st.Mode().IsRegular() || st.Size() != 8 {
		add("control_file", "error", fmt.Sprintf("control file %s must be an 8-byte regular file (size=%d, mode=%s)", absControl, st.Size(), st.Mode()), fmt.Sprintf("Run: nginx-inspector setup --control-file %s", absControl))
	} else if runtime.GOOS != "windows" && st.Mode().Perm()&0004 == 0 {
		add("control_file", "error", fmt.Sprintf("control file %s mode %04o is not readable by nginx workers", absControl, st.Mode().Perm()), fmt.Sprintf("chmod 0644 %s", absControl))
	} else {
		detail := fmt.Sprintf("8-byte control file ready at %s (mode %04o)", absControl, st.Mode().Perm())
		if blocked, ok := CheckParentTraversal(absControl); !ok {
			detail += fmt.Sprintf("; note: ancestor %s lacks o+x if workers use a separate account", blocked)
		}
		add("control_file", "ok", detail, "")
	}

	var configuredSO string
	if confPath != "" {
		if raw, err := os.ReadFile(confPath); err != nil {
			add("nginx_config", "warn", fmt.Sprintf("cannot read %s: %v", confPath, err), fmt.Sprintf("Run with sudo or pass --nginx-conf: sudo nginx-inspector doctor --nginx-conf %s", confPath))
		} else {
			text := string(raw)
			if m := loadModulePattern.FindStringSubmatch(text); len(m) == 2 {
				configuredSO = m[1]
			}
			hasModule := configuredSO != ""
			hasEnabled := directivePatterns["inspector"].MatchString(text) && strings.Contains(directivePatterns["inspector"].FindString(text), "on")
			hasControl := directivePatterns["inspector_control"].MatchString(text)
			if hasModule && hasEnabled && hasControl {
				add("nginx_config", "ok", fmt.Sprintf("%s loads %s and enables inspector_control", confPath, configuredSO), "")
			} else {
				add("nginx_config", "error", fmt.Sprintf("%s is missing load_module, inspector on, or inspector_control", confPath), fmt.Sprintf("Run: nginx-inspector setup --nginx-conf %s --control-file %s", confPath, absControl))
			}
		}
	}

	if configuredSO != "" {
		if err := verifyMarkerABI(configuredSO); err == nil {
			add("module_marker_abi", "ok", fmt.Sprintf("%s exports ngx_inspector_request_begin/end markers for kernel transport tracing", configuredSO), "")
		} else if _, statErr := os.Stat(configuredSO); statErr == nil {
			add("module_marker_abi", "warn", fmt.Sprintf("%s is readable (%v)", configuredSO, err), "To enable kernel transport correlation, rebuild and reload: nginx-inspector reload --build-module")
		}
	}

	if runtime.GOOS == "linux" && opts.ProcRoot != "" {
		view := readWorkerProcesses(opts.ProcRoot, pidPath)
		if view.Available && len(view.Processes) > 0 {
			add("worker_sampling", "ok", fmt.Sprintf("visible nginx processes: %d (scoped=%v, master_pid=%d)", len(view.Processes), view.Scoped, view.MasterPID), "")
		} else if view.Available {
			add("worker_sampling", "warn", "proc filesystem is readable but no running nginx processes matched", "Verify nginx is running and check --nginx-pid-file")
		} else {
			add("worker_sampling", "warn", fmt.Sprintf("worker process sampling unavailable: %s", view.Error), fmt.Sprintf("Pass --proc-root /proc --nginx-pid-file %s to nginx-inspector serve", pidPath))
		}
	}

	client := http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(strings.TrimRight(opts.CollectorURL, "/") + "/health")
	if err != nil {
		add("collector_api", "warn", fmt.Sprintf("collector not reachable at %s (%v)", opts.CollectorURL, err), fmt.Sprintf("Start collector: nginx-inspector serve --control-file %s", absControl))
	} else {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		if resp.StatusCode == 200 {
			add("collector_api", "ok", fmt.Sprintf("collector healthy at %s (%s)", opts.CollectorURL, strings.TrimSpace(string(body))), "")
		} else {
			add("collector_api", "error", fmt.Sprintf("collector returned HTTP %d at %s", resp.StatusCode, opts.CollectorURL), "Check collector storage permissions and disk space")
		}
	}

	report.Commands["setup_or_reload"] = fmt.Sprintf("nginx-inspector reload --control-file %s", absControl)
	report.Commands["serve"] = fmt.Sprintf("nginx-inspector serve --db ./data/events.sqlite3 --control-file %s", absControl)
	report.Commands["trace_all_30s"] = "nginx-inspector start --all --for 30s"
	report.Commands["trace_transport_30s"] = "nginx-inspector start --all --transport --for 30s"
	return report
}
