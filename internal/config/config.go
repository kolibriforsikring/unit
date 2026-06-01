// Package config provides configuration loading and parsing for unit.
package config

import (
	"fmt"
	"os"

	"github.com/pelletier/go-toml/v2"
)

// ResourceConfig represents cgroup-based resource limits for the service.
type ResourceConfig struct {
	// MemoryMax is the hard memory limit. The process is OOM-killed if exceeded.
	// Accepts systemd size suffixes: K, M, G (e.g. "512M").
	MemoryMax string `toml:"memory_max"`
	// MemoryHigh is the soft memory limit. The kernel throttles the process
	// before it reaches MemoryMax. Should be lower than MemoryMax.
	MemoryHigh string `toml:"memory_high"`
	// CPUQuota limits CPU time as a percentage of one core.
	// "100%" = one full core, "50%" = half a core, "200%" = two cores.
	CPUQuota string `toml:"cpu_quota"`
	// TasksMax limits the number of concurrent tasks (threads + processes).
	// Prevents fork bombs. 0 means no limit.
	TasksMax int `toml:"tasks_max"`
}

// Config represents the application configuration loaded from unit.toml files.
type Config struct {
	Name         string            `toml:"name"`
	Executable   string            `toml:"executable"`
	Port         int               `toml:"port"`
	Server       ServerConfig      `toml:"server"`
	Sandbox      SandboxConfig     `toml:"sandbox"`
	Domain       string            `toml:"domain"`
	SecretScript string            `toml:"secrets"`
	DeployPath   string            `toml:"deploy_path"`
	Env          map[string]string `toml:"env"`
	Secrets      map[string]string `toml:"-"`
	Jobs         []JobConfig       `toml:"jobs"`
	Resources    ResourceConfig    `toml:"resources"`
	DependsOn    DependsOnConfig   `toml:"depends_on"`
}

// DependsOnConfig controls systemd ordering and dependency declarations.
// All three fields append to the generated [Unit] section — they do not
// replace the defaults unit always includes.
type DependsOnConfig struct {
	// After lists units this service must start after (ordering only).
	// e.g. ["postgresql.service", "network-online.target"]
	After []string `toml:"after"`
	// Requires lists units this service hard-depends on. If a required unit
	// stops or fails, this service will be stopped too.
	Requires []string `toml:"requires"`
	// Wants lists units systemd should try to start alongside this service,
	// but will not fail if they are unavailable.
	Wants []string `toml:"wants"`
}

// JobConfig represents a scheduled one-shot job backed by the same binary.
// Each job produces a <name>-<command>.service and <name>-<command>.timer unit.
type JobConfig struct {
	// Command is the subcommand passed to the binary, e.g. "report" → ./bin report
	Command    string `toml:"command"`
	OnCalendar string `toml:"on_calendar"`
	Persistent bool   `toml:"persistent"`
}

// SandboxConfig represents the security boundaries for the application.
type SandboxConfig struct {
	AllowOutboundNetwork bool     `toml:"allow_outbound_network"`
	WritablePaths        []string `toml:"writable_paths"`
	// StateDirectory creates /var/lib/<name> via systemd StateDirectory= and
	// gives the DynamicUser ownership of it across restarts. Use this for apps
	// that need persistent storage (caches, databases, workspaces, etc.).
	StateDirectory bool `toml:"state_directory"`
	// LogsDirectory creates /var/log/<name> via systemd LogsDirectory= and
	// gives the DynamicUser ownership of it. Use this for apps that write
	// structured log files alongside the standard journal output.
	LogsDirectory bool `toml:"logs_directory"`
	// AllowWriteExecute removes the MemoryDenyWriteExecute=yes directive.
	// Required for runtimes that use JIT compilation (e.g. DuckDB via LLVM,
	// LuaJIT, V8) which need mmap(PROT_WRITE|PROT_EXEC).
	AllowWriteExecute bool `toml:"allow_write_execute"`
}

// ServerConfig represents the SSH server configuration for deployment.
type ServerConfig struct {
	Host                  string `toml:"host"`
	User                  string `toml:"user"`
	SSHPort               int    `toml:"ssh_port"`
	StrictHostKeyChecking bool   `toml:"strict_host_key_checking"`
	Timeout               int    `toml:"timeout"`
}

// Load reads the base unit.toml and an optional environment override file.
func Load(env string) (*Config, error) {
	// 1. Load the base configuration file first.
	baseData, err := os.ReadFile("unit.toml")
	if err != nil {
		return nil, fmt.Errorf("failed to read base configuration unit.toml: %w", err)
	}

	var cfg Config
	cfg.Server.StrictHostKeyChecking = true
	cfg.Server.Timeout = 30 // Default to 30 seconds
	cfg.Server.SSHPort = 22 // Default standard SSH port

	if err := toml.Unmarshal(baseData, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse unit.toml: %w", err)
	}

	if cfg.DeployPath == "" {
		cfg.DeployPath = "/opt/unit"
	}

	// 2. If an environment is specified, load that file and merge it.
	if env != "" {
		envFilename := "unit." + env + ".toml"
		envData, err := os.ReadFile(envFilename)

		if err != nil {
			if os.IsNotExist(err) {
				fmt.Fprintf(os.Stderr, "⚠️  Warning: Environment override file '%s' not found. Continuing with base configuration only.\n", envFilename)
				return &cfg, nil
			}
			return nil, fmt.Errorf("failed to read override configuration %s: %w", envFilename, err)
		}

		fmt.Fprintf(os.Stderr, "ℹ️  Merging override configuration from %s...\n", envFilename)
		if err := toml.Unmarshal(envData, &cfg); err != nil {
			return nil, fmt.Errorf("failed to parse override configuration %s: %w", envFilename, err)
		}
	}

	return &cfg, nil
}
