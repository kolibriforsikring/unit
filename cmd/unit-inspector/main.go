// This program exists as an executable demonstration of what `unit`
// configures for you.
//
// It is intentionally verbose and introspective, and is meant to be
// read as much as it is meant to be run.
//
// By inspecting its output and source code, you can learn how systemd
// provides:
//   - socket activation (zero-downtime restarts)
//   - dynamic users
//   - credential injection
//   - readiness notification
//   - watchdog supervision
//
// No systemd-specific Go libraries are used — everything relies on
// standard library behavior and environment variables.
package main

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Info represents the sandbox status and system information.
type Info struct {
	AppName         string
	GoVersion       string
	Architecture    string
	PID             int
	UID             int
	Username        string
	SocketActive    bool
	ClientIP        string
	ForwardedFor    string
	SecretStatus    string
	EnvFoo          string
	OutboundNetwork string
	WriteTmp        string
	WriteEtc        string
	ReadRoot        string
	StateDir        string // path from $STATE_DIRECTORY, empty if not set
	WriteState      string // result of writing a file to the state directory
	LogsDir         string // path from $LOGS_DIRECTORY, empty if not set
	WriteLog        string // result of writing a log file to the logs directory
	Resources       ResourceInfo
}

// ResourceInfo holds cgroup-reported resource limits and current usage.
type ResourceInfo struct {
	MemoryMax     string
	MemoryHigh    string
	MemoryCurrent string
	CPUMax        string
	TasksMax      string
	TasksCurrent  string
}

// gatherResourceInfo reads resource limits and current usage from the cgroup v2
// filesystem. Returns an empty struct if cgroups are not available (e.g. local dev).
func gatherResourceInfo() ResourceInfo {
	cgroupPath := resolveCgroupPath()

	read := func(file string) string {
		data, err := os.ReadFile(filepath.Join(cgroupPath, file))
		if err != nil {
			return "unavailable"
		}
		return strings.TrimSpace(string(data))
	}

	info := ResourceInfo{
		MemoryMax:     formatBytes(read("memory.max")),
		MemoryHigh:    formatBytes(read("memory.high")),
		MemoryCurrent: formatBytes(read("memory.current")),
		CPUMax:        formatCPU(read("cpu.max")),
		TasksMax:      read("pids.max"),
		TasksCurrent:  read("pids.current"),
	}
	return info
}

// resolveCgroupPath finds this process's cgroup v2 directory under /sys/fs/cgroup.
func resolveCgroupPath() string {
	data, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return "/sys/fs/cgroup"
	}
	// cgroups v2: single line "0::/system.slice/unit-inspector.service"
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		parts := strings.SplitN(line, ":", 3)
		if len(parts) == 3 && parts[0] == "0" {
			return filepath.Join("/sys/fs/cgroup", parts[2])
		}
	}
	return "/sys/fs/cgroup"
}

// formatBytes converts a raw cgroup byte value (or "max") to a human-readable string.
func formatBytes(raw string) string {
	if raw == "max" || raw == "unavailable" {
		return raw
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return raw
	}
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// formatCPU converts a cpu.max value ("quota period" or "max period") to a percent string.
// e.g. "50000 100000" → "50%", "max 100000" → "unlimited"
func formatCPU(raw string) string {
	if raw == "unavailable" {
		return raw
	}
	parts := strings.Fields(raw)
	if len(parts) != 2 {
		return raw
	}
	if parts[0] == "max" {
		return "unlimited"
	}
	quota, err1 := strconv.ParseFloat(parts[0], 64)
	period, err2 := strconv.ParseFloat(parts[1], 64)
	if err1 != nil || err2 != nil || period == 0 {
		return raw
	}
	return fmt.Sprintf("%.0f%%", (quota/period)*100)
}

func gatherInfo(r *http.Request) Info {
	info := Info{
		AppName:      "unit-inspector",
		GoVersion:    runtime.Version(),
		Architecture: fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH),
		PID:          os.Getpid(),
		UID:          os.Getuid(),
		SocketActive: os.Getenv("LISTEN_PID") != "",
		ClientIP:     r.RemoteAddr,
	}

	if u, err := user.Current(); err == nil {
		info.Username = u.Username
	} else {
		info.Username = "unknown"
	}

	if realIP := r.Header.Get("X-Real-IP"); realIP != "" {
		info.ClientIP = realIP
	}
	info.ForwardedFor = r.Header.Get("X-Forwarded-For")

	// Check Secrets
	if secret, ok := ReadSecret("DB_PASSWORD"); ok {
		info.SecretStatus = fmt.Sprintf("Injected (%d bytes, via systemd)", len(secret))
	} else {
		info.SecretStatus = "Missing (Check unit.toml secrets script)"
	}

	// Check static env
	info.EnvFoo = os.Getenv("FOO")

	// Check Outbound Network (TCP connect to Cloudflare DNS)
	conn, err := net.DialTimeout("tcp", "1.1.1.1:53", 2*time.Second)
	if err != nil {
		info.OutboundNetwork = fmt.Sprintf("Blocked (err: %v)", err)
	} else {
		info.OutboundNetwork = "Allowed"
		conn.Close()
	}

	// Check Filesystem Writes (/tmp)
	tmpPath := filepath.Join(os.TempDir(), "unit-sandbox-test.txt")
	if err := os.WriteFile(tmpPath, []byte("test"), 0600); err != nil {
		info.WriteTmp = fmt.Sprintf("Blocked (err: %v)", err)
	} else {
		info.WriteTmp = fmt.Sprintf("Allowed (to %s)", os.TempDir())
		os.Remove(tmpPath)
	}

	// Check Filesystem Writes (/etc)
	etcPath := "/etc/unit-sandbox-test.txt"
	if err := os.WriteFile(etcPath, []byte("test"), 0600); err != nil {
		info.WriteEtc = fmt.Sprintf("Blocked (err: %v)", err)
	} else {
		info.WriteEtc = "Allowed ⚠️ (Sandbox weak or running as root)"
		os.Remove(etcPath)
	}

	// Check Filesystem Reads (/root)
	if _, err := os.ReadDir("/root"); err != nil {
		info.ReadRoot = fmt.Sprintf("Blocked (err: %v)", err)
	} else {
		info.ReadRoot = "Allowed ⚠️ (Sandbox weak or running as root)"
	}

	// Check State Directory (systemd StateDirectory=)
	info.StateDir = os.Getenv("STATE_DIRECTORY")
	if info.StateDir == "" {
		info.WriteState = "Not configured (set state_directory = true in unit.toml)"
	} else {
		testPath := filepath.Join(info.StateDir, "inspector-write-test.txt")
		if err := os.WriteFile(testPath, []byte("ok"), 0o600); err != nil {
			info.WriteState = fmt.Sprintf("Blocked (err: %v)", err)
		} else {
			info.WriteState = fmt.Sprintf("Allowed (wrote %s)", testPath)
			os.Remove(testPath)
		}
	}

	// Check Logs Directory (systemd LogsDirectory=)
	info.LogsDir = os.Getenv("LOGS_DIRECTORY")
	if info.LogsDir == "" {
		info.WriteLog = "Not configured (set logs_directory = true in unit.toml)"
	} else {
		logPath := filepath.Join(info.LogsDir, "inspector.log")
		entry := fmt.Sprintf("%s request from %s\n", time.Now().Format(time.RFC3339), r.RemoteAddr)
		f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			info.WriteLog = fmt.Sprintf("Blocked (err: %v)", err)
		} else {
			_, _ = f.WriteString(entry)
			f.Close()
			info.WriteLog = fmt.Sprintf("Allowed (appended to %s)", logPath)
		}
	}

	info.Resources = gatherResourceInfo()

	return info
}

const htmlTemplate = `
<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Unit Inspector</title>
    <style>
        :root {
            --bg-color: #0f172a;
            --panel-bg: #1e293b;
            --text-color: #f8fafc;
            --accent: #38bdf8;
            --success: #10b981;
            --danger: #ef4444;
            --warning: #f59e0b;
        }
        body {
            font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", "Courier New", monospace;
            background-color: var(--bg-color);
            color: var(--text-color);
            margin: 0;
            padding: 2rem;
            line-height: 1.6;
        }
        .container {
            max-width: 800px;
            margin: 0 auto;
        }
        h1 {
            color: var(--accent);
            border-bottom: 2px solid var(--panel-bg);
            padding-bottom: 0.5rem;
            margin-bottom: 2rem;
        }
        .panel {
            background-color: var(--panel-bg);
            border-radius: 8px;
            padding: 1.5rem;
            margin-bottom: 1.5rem;
            box-shadow: 0 4px 6px -1px rgba(0, 0, 0, 0.1), 0 2px 4px -1px rgba(0, 0, 0, 0.06);
        }
        .panel h2 {
            margin-top: 0;
            color: var(--accent);
            font-size: 1.2rem;
            margin-bottom: 1rem;
        }
        .row {
            display: flex;
            padding: 0.5rem 0;
            border-bottom: 1px solid rgba(255,255,255,0.05);
        }
        .row:last-child {
            border-bottom: none;
        }
        .key {
            width: 200px;
            color: #94a3b8;
            font-weight: bold;
        }
        .val {
            flex-grow: 1;
        }
        .val.blocked { color: var(--success); }
        .val.allowed { color: var(--danger); font-weight: bold; }
        .val.neutral { color: var(--text-color); }
        
        /* Helper logic for coloring based on content */
        .status-blocked { color: var(--success); } /* Blocked is good for sandbox */
        .status-allowed { color: var(--danger); }  /* Allowed network/etc is a warning if strict */
    </style>
</head>
<body>
    <div class="container">
        <h1>🔍 Unit Sandbox Inspector</h1>
        <p>This binary demonstrates the isolation and configuration provided by <code>unit</code> and <code>systemd</code>.</p>
        
        <div class="panel">
            <h2>System Context</h2>
            <div class="row"><div class="key">App Name</div><div class="val">{{.AppName}}</div></div>
            <div class="row"><div class="key">Go Environment</div><div class="val">{{.GoVersion}} ({{.Architecture}})</div></div>
            <div class="row"><div class="key">Process ID</div><div class="val">{{.PID}}</div></div>
            <div class="row"><div class="key">System User</div><div class="val">{{.Username}} (UID: {{.UID}})</div></div>
            <div class="row"><div class="key">Socket Activation</div><div class="val">{{if .SocketActive}}<span style="color:var(--success)">Active (FD 3)</span>{{else}}<span style="color:var(--warning)">Inactive (Direct Bind)</span>{{end}}</div></div>
            <div class="row"><div class="key">Client IP</div><div class="val">{{.ClientIP}}</div></div>
            <div class="row"><div class="key">X-Forwarded-For</div><div class="val">{{if .ForwardedFor}}{{.ForwardedFor}}{{else}}None{{end}}</div></div>
        </div>

        <div class="panel">
            <h2>Configuration & Secrets</h2>
            <div class="row"><div class="key">DB_PASSWORD (Secret)</div><div class="val">{{.SecretStatus}}</div></div>
            <div class="row"><div class="key">FOO (Env)</div><div class="val">{{if .EnvFoo}}{{.EnvFoo}}{{else}}None{{end}}</div></div>
        </div>

        <div class="panel">
            <h2>Security Sandbox Capabilities</h2>
            <div class="row">
                <div class="key">Outbound Network</div>
                <div class="val {{if contains .OutboundNetwork "Blocked"}}status-blocked{{else}}status-allowed{{end}}">{{.OutboundNetwork}}</div>
            </div>
            <div class="row">
                <div class="key">Write to /tmp</div>
                <div class="val {{if contains .WriteTmp "Blocked"}}status-blocked{{else}}neutral{{end}}">{{.WriteTmp}}</div>
            </div>
            <div class="row">
                <div class="key">Write to /etc</div>
                <div class="val {{if contains .WriteEtc "Blocked"}}status-blocked{{else}}status-allowed{{end}}">{{.WriteEtc}}</div>
            </div>
            <div class="row">
                <div class="key">Read /root</div>
                <div class="val {{if contains .ReadRoot "Blocked"}}status-blocked{{else}}status-allowed{{end}}">{{.ReadRoot}}</div>
            </div>
            <div class="row">
                <div class="key">State Directory</div>
                <div class="val {{if .StateDir}}neutral{{else}}status-allowed{{end}}">{{if .StateDir}}{{.StateDir}}{{else}}Not set{{end}}</div>
            </div>
            <div class="row">
                <div class="key">Write to State</div>
                <div class="val {{if contains .WriteState "Allowed"}}status-blocked{{else}}status-allowed{{end}}">{{.WriteState}}</div>
            </div>
            <div class="row">
                <div class="key">Logs Directory</div>
                <div class="val {{if .LogsDir}}neutral{{else}}status-allowed{{end}}">{{if .LogsDir}}{{.LogsDir}}{{else}}Not set{{end}}</div>
            </div>
            <div class="row">
                <div class="key">Write to Logs</div>
                <div class="val {{if contains .WriteLog "Allowed"}}status-blocked{{else}}status-allowed{{end}}">{{.WriteLog}}</div>
            </div>
        </div>

        <div class="panel">
            <h2>Resource Limits (cgroup v2)</h2>
            <div class="row">
                <div class="key">memory.max</div>
                <div class="val {{if eq .Resources.MemoryMax "max"}}status-allowed{{else}}status-blocked{{end}}">{{.Resources.MemoryMax}}</div>
            </div>
            <div class="row">
                <div class="key">memory.high</div>
                <div class="val">{{.Resources.MemoryHigh}}</div>
            </div>
            <div class="row">
                <div class="key">memory.current</div>
                <div class="val">{{.Resources.MemoryCurrent}}</div>
            </div>
            <div class="row">
                <div class="key">cpu.max</div>
                <div class="val {{if eq .Resources.CPUMax "unlimited"}}status-allowed{{else}}status-blocked{{end}}">{{.Resources.CPUMax}}</div>
            </div>
            <div class="row">
                <div class="key">pids.max</div>
                <div class="val">{{.Resources.TasksMax}}</div>
            </div>
            <div class="row">
                <div class="key">pids.current</div>
                <div class="val">{{.Resources.TasksCurrent}}</div>
            </div>
        </div>

        <div class="panel">
            <h2>Stress Tests</h2>
            <p style="color: #94a3b8; margin-top: 0;">
                These endpoints test resource limit enforcement. If a limit is configured,
                exceeding it will cause systemd to kill and restart the process.
            </p>
            <div class="row">
                <div class="key">Memory</div>
                <div class="val">
                    <a href="/stress/memory?mb=32" style="color:var(--accent)">+32 MB</a> &nbsp;
                    <a href="/stress/memory?mb=64" style="color:var(--accent)">+64 MB</a> &nbsp;
                    <a href="/stress/memory?mb=128" style="color:var(--accent)">+128 MB</a>
                    <span style="color:#94a3b8"> — allocates and holds memory, triggers OOM kill if over limit</span>
                </div>
            </div>
            <div class="row">
                <div class="key">CPU</div>
                <div class="val">
                    <a href="/stress/cpu?seconds=10" style="color:var(--accent)">10s burn</a>
                    <span style="color:#94a3b8"> — saturates one core, visible in systemd-cgtop</span>
                </div>
            </div>
        </div>
    </div>
</body>
</html>
`

func main() {
	// Dispatch to subcommands before starting the HTTP server.
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "report":
			runReport()
			return
		}
	}

	var (
		listener net.Listener
		err      error
	)

	// 1. SOCKET ACTIVATION
	if os.Getenv("LISTEN_PID") == strconv.Itoa(os.Getpid()) {
		fmt.Println("🚀 Socket activation detected -> Taking over FD 3")
		file := os.NewFile(3, "systemd-socket")
		listener, err = net.FileListener(file)
		if err != nil {
			fmt.Printf("❌ Failed to adopt systemd socket: %v\n", err)
			os.Exit(1)
		}
	} else {
		fmt.Println("🔌 No socket activation -> Binding directly to :8080")
		listener, err = net.Listen("tcp", ":8080")
		if err != nil {
			fmt.Printf("❌ Failed to bind :8080: %v\n", err)
			os.Exit(1)
		}
	}

	// Template setup
	tmpl := template.Must(template.New("inspector").Funcs(template.FuncMap{
		"contains": strings.Contains,
	}).Parse(htmlTemplate))

	// 2. HTTP ENDPOINTS
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		info := gatherInfo(r)

		// Content negotiation
		if strings.Contains(r.Header.Get("Accept"), "application/json") {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(info)
			return
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := tmpl.Execute(w, info); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	})

	// /report serves the last report written by the "report" job.
	// This demonstrates shared state between the long-running service and
	// the scheduled job — both use the same binary and $STATE_DIRECTORY.
	http.HandleFunc("/report", func(w http.ResponseWriter, r *http.Request) {
		stateDir := os.Getenv("STATE_DIRECTORY")
		if stateDir == "" {
			http.Error(w, "STATE_DIRECTORY not set (add state_directory = true to unit.toml)", http.StatusServiceUnavailable)
			return
		}
		reportPath := filepath.Join(stateDir, "last-report.json")
		data, err := os.ReadFile(reportPath)
		if err != nil {
			http.Error(w, "No report yet — timer has not run. Trigger manually: systemctl start unit-inspector-report.service", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(data)
	})

	// /stress/memory?mb=N allocates N megabytes and holds them.
	// If MemoryMax is set below N, the process will be OOM-killed by the kernel
	// and systemd will restart it — demonstrating the limit is enforced.
	http.HandleFunc("/stress/memory", func(w http.ResponseWriter, r *http.Request) {
		mbStr := r.URL.Query().Get("mb")
		mb, err := strconv.Atoi(mbStr)
		if err != nil || mb <= 0 || mb > 4096 {
			http.Error(w, "usage: /stress/memory?mb=<1-4096>", http.StatusBadRequest)
			return
		}
		fmt.Fprintf(w, "Allocating %d MB — if this exceeds memory.max the process will be killed and restarted by systemd.\n\n", mb)
		// Flush before allocating so the response header reaches the client.
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		buf := make([]byte, mb*1024*1024)
		// Touch every page to force physical allocation (Go runtime may not commit lazily).
		for i := range buf {
			buf[i] = 1
		}
		// Hold the allocation for 30 seconds so memory.current is visible in cgtop.
		time.Sleep(30 * time.Second)
		fmt.Fprintf(w, "Released after 30s. Current memory: %s\n", func() string {
			var ms runtime.MemStats
			runtime.ReadMemStats(&ms)
			return fmt.Sprintf("%.1f MB RSS", float64(ms.Sys)/(1<<20))
		}())
		_ = buf
	})

	// /stress/cpu?seconds=N runs a tight loop for N seconds on one goroutine.
	// With CPUQuota set, systemd will throttle the process — visible in systemd-cgtop.
	http.HandleFunc("/stress/cpu", func(w http.ResponseWriter, r *http.Request) {
		secStr := r.URL.Query().Get("seconds")
		sec, err := strconv.Atoi(secStr)
		if err != nil || sec <= 0 || sec > 60 {
			http.Error(w, "usage: /stress/cpu?seconds=<1-60>", http.StatusBadRequest)
			return
		}
		fmt.Fprintf(w, "Burning CPU for %ds — watch systemd-cgtop to see throttling if CPUQuota is set.\n", sec)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		deadline := time.Now().Add(time.Duration(sec) * time.Second)
		for time.Now().Before(deadline) {
			// tight loop
		}
		fmt.Fprintln(w, "Done.")
	})

	go func() {
		if err := http.Serve(listener, nil); err != nil {
			fmt.Printf("❌ HTTP server exited: %v\n", err)
			os.Exit(1)
		}
	}()

	// 3. SYSTEMD NOTIFICATIONS
	notify := func(state string) {
		socket := os.Getenv("NOTIFY_SOCKET")
		if socket == "" {
			return
		}

		conn, err := net.Dial("unixgram", socket)
		if err != nil {
			return
		}
		defer conn.Close()

		_, _ = conn.Write([]byte(state))
	}

	notify("READY=1")
	fmt.Println("✅ READY=1 sent to systemd")

	// 4. WATCHDOG SUPPORT
	go func() {
		usecStr := os.Getenv("WATCHDOG_USEC")
		if usecStr == "" {
			return
		}

		usec, err := strconv.Atoi(usecStr)
		if err != nil {
			return
		}

		interval := time.Duration(usec) * time.Microsecond
		fmt.Printf("🐕 Watchdog active (interval: %v)\n", interval)

		for {
			notify("WATCHDOG=1")
			time.Sleep(interval / 2)
		}
	}()

	select {}
}

// runReport is the entrypoint for the "report" scheduled job.
// It collects sandbox and runtime info, writes a JSON snapshot to
// $STATE_DIRECTORY/last-report.json, and exits. The long-running service
// exposes this file via GET /report, demonstrating shared state between the
// service and the periodic job.
func runReport() {
	type Report struct {
		GeneratedAt  string `json:"generated_at"`
		GoVersion    string `json:"go_version"`
		Architecture string `json:"architecture"`
		PID          int    `json:"pid"`
		UID          int    `json:"uid"`
		Username     string `json:"username"`
		SecretStatus string `json:"secret_status"`
		OutboundNet  string `json:"outbound_network"`
		StateDir     string `json:"state_directory"`
		WriteState   string `json:"write_state"`
	}

	report := Report{
		GeneratedAt:  time.Now().UTC().Format(time.RFC3339),
		GoVersion:    runtime.Version(),
		Architecture: fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH),
		PID:          os.Getpid(),
		UID:          os.Getuid(),
	}

	if u, err := user.Current(); err == nil {
		report.Username = u.Username
	}

	if _, ok := ReadSecret("DB_PASSWORD"); ok {
		report.SecretStatus = "Injected (via systemd LoadCredential)"
	} else {
		report.SecretStatus = "Missing"
	}

	conn, err := net.DialTimeout("tcp", "1.1.1.1:53", 2*time.Second)
	if err != nil {
		report.OutboundNet = fmt.Sprintf("Blocked (%v)", err)
	} else {
		report.OutboundNet = "Allowed"
		conn.Close()
	}

	report.StateDir = os.Getenv("STATE_DIRECTORY")

	if report.StateDir == "" {
		report.WriteState = "Skipped (STATE_DIRECTORY not set)"
	} else {
		report.WriteState = fmt.Sprintf("Written to %s", filepath.Join(report.StateDir, "last-report.json"))
	}

	if report.StateDir != "" {
		reportPath := filepath.Join(report.StateDir, "last-report.json")
		data, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			fmt.Printf("❌ Failed to marshal report: %v\n", err)
			os.Exit(1)
		}
		if err := os.WriteFile(reportPath, data, 0o644); err != nil {
			fmt.Printf("❌ Failed to write report to %s: %v\n", reportPath, err)
			os.Exit(1)
		}
		// UMask=0077 in the job service strips group/world bits at the OS level.
		// Chmod explicitly so the long-running service (different DynamicUser UID) can read the file.
		if err := os.Chmod(reportPath, 0o644); err != nil {
			fmt.Printf("❌ Failed to chmod report: %v\n", err)
			os.Exit(1)
		}
	}

	fmt.Printf("✅ Report complete at %s\n", report.GeneratedAt)
	fmt.Printf("   User:    %s (UID %d)\n", report.Username, report.UID)
	fmt.Printf("   Network: %s\n", report.OutboundNet)
	fmt.Printf("   Secret:  %s\n", report.SecretStatus)
	fmt.Printf("   State:   %s\n", report.WriteState)
}

// ReadSecret reads a secret injected via systemd LoadCredential=.
func ReadSecret(name string) (string, bool) {
	credDir := os.Getenv("CREDENTIALS_DIRECTORY")
	if credDir != "" {
		path := filepath.Join(credDir, name)
		if data, err := os.ReadFile(path); err == nil {
			return strings.TrimSpace(string(data)), true
		}
	}

	val := os.Getenv(strings.ToUpper(name))
	if val != "" {
		return val, true
	}

	return "", false
}
