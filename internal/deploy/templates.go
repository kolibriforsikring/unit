// Package deploy contains templates and helpers used by unit to
// generate systemd service, socket, and proxy configuration.
//
// The templates are intentionally opinionated and security-focused,
// providing strong isolation by default while remaining compatible
// with statically compiled binaries.
package deploy

// ServiceTemplate is the systemd service unit template.
const ServiceTemplate = `[Unit]
Description=Unit Service: {{.Name}}
Requires={{.Name}}.socket
StartLimitBurst=1
StartLimitIntervalSec=60s
{{- range .DependsOn.After}}
After={{.}}{{end}}
{{- range .DependsOn.Requires}}
Requires={{.}}{{end}}
{{- range .DependsOn.Wants}}
Wants={{.}}{{end}}

[Service]
Type=notify
WatchdogSec=10s
TimeoutStartSec=30s
Restart=always
DynamicUser=yes
ProtectHome=yes
CapabilityBoundingSet=
PrivateDevices=yes
ProtectClock=yes
ProtectKernelLogs=yes
ProtectKernelModules=yes
ProtectKernelTunables=yes
ProtectControlGroups=yes
RestrictNamespaces=yes
{{if not .Sandbox.AllowWriteExecute}}MemoryDenyWriteExecute=yes{{end}}
LockPersonality=yes
SystemCallFilter=@system-service
SystemCallErrorNumber=EPERM

{{if not .Sandbox.AllowOutboundNetwork}}
# Network Lockdown: Inbound socket activation only, no outbound connections
PrivateNetwork=yes
RestrictAddressFamilies=AF_UNIX
IPAddressDeny=any
{{else}}
# Network Open: Outbound connections allowed
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX
{{end}}

# Filesystem Lockdown: Entire OS is read-only except for explicitly whitelisted paths
ProtectSystem=strict
PrivateTmp=yes
{{range .Sandbox.WritablePaths}}
ReadWritePaths={{.}}
{{end}}

UMask=0077
ProtectProc=invisible
ProcSubset=pid
# Prevents switching to non-native system call architectures
SystemCallArchitectures=native
# Prevents the app from changing the system hostname
ProtectHostname=yes
# Disables realtime scheduling to prevent CPU exhaustion attacks
RestrictRealtime=yes
# Sets up a private user namespace to hide other host users
PrivateUsers=yes


{{if .Sandbox.StateDirectory}}
# Persistent storage at /var/lib/{{.Name}}, owned by DynamicUser.
# Accessible at runtime via $STATE_DIRECTORY.
StateDirectory={{.Name}}
{{end}}
{{if .Sandbox.LogsDirectory}}
# Log directory at /var/log/{{.Name}}, owned by DynamicUser.
# Accessible at runtime via $LOGS_DIRECTORY.
LogsDirectory={{.Name}}
{{end}}
WorkingDirectory={{.DeployPath}}/{{.Name}}/current
ExecStart={{.DeployPath}}/{{.Name}}/current/bin
{{range $key, $value := .Env}}
Environment="{{$key}}={{$value}}"{{end}}
{{range $key, $value := .Secrets}}
LoadCredential={{$key}}:/etc/credentials/{{$.Name}}/{{$key}}{{end}}

{{if .Resources.MemoryMax}}MemoryMax={{.Resources.MemoryMax}}{{end}}
{{if .Resources.MemoryHigh}}MemoryHigh={{.Resources.MemoryHigh}}{{end}}
{{if .Resources.CPUQuota}}CPUQuota={{.Resources.CPUQuota}}{{end}}
{{if .Resources.TasksMax}}TasksMax={{.Resources.TasksMax}}{{end}}

[Install]
WantedBy=multi-user.target
`

// SocketTemplate is the systemd socket unit template.
const SocketTemplate = `[Unit]
Description=Unit Socket: {{.Name}}

[Socket]
ListenStream={{.Port}}
NoDelay=true

[Install]
WantedBy=sockets.target
`

// JobServiceTemplate is the systemd service unit template for a scheduled one-shot job.
// It uses the same binary as the main service but invoked with a subcommand argument.
const JobServiceTemplate = `[Unit]
Description=Unit Job: {{.Name}} {{.Job.Command}}
{{- range .DependsOn.After}}
After={{.}}{{end}}
{{- range .DependsOn.Requires}}
Requires={{.}}{{end}}
{{- range .DependsOn.Wants}}
Wants={{.}}{{end}}

[Service]
Type=oneshot
Restart=on-failure
DynamicUser=yes
ProtectHome=yes
CapabilityBoundingSet=
PrivateDevices=yes
ProtectClock=yes
ProtectKernelLogs=yes
ProtectKernelModules=yes
ProtectKernelTunables=yes
ProtectControlGroups=yes
RestrictNamespaces=yes
{{if not .Sandbox.AllowWriteExecute}}MemoryDenyWriteExecute=yes{{end}}
LockPersonality=yes
SystemCallFilter=@system-service
SystemCallErrorNumber=EPERM

{{if not .Sandbox.AllowOutboundNetwork}}
# Network Lockdown: No outbound connections
PrivateNetwork=yes
RestrictAddressFamilies=AF_UNIX
IPAddressDeny=any
{{else}}
# Network Open: Outbound connections allowed
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX
{{end}}

ProtectSystem=strict
PrivateTmp=yes
{{range .Sandbox.WritablePaths}}
ReadWritePaths={{.}}
{{end}}

UMask=0077
ProtectProc=invisible
ProcSubset=pid
SystemCallArchitectures=native
ProtectHostname=yes
RestrictRealtime=yes
PrivateUsers=yes

{{if .Sandbox.StateDirectory}}
StateDirectory={{.Name}}
{{end}}
{{if .Sandbox.LogsDirectory}}
LogsDirectory={{.Name}}
{{end}}
WorkingDirectory={{.DeployPath}}/{{.Name}}/current
ExecStart={{.DeployPath}}/{{.Name}}/current/bin {{.Job.Command}}
{{range $key, $value := .Env}}
Environment="{{$key}}={{$value}}"{{end}}
{{range $key, $value := .Secrets}}
LoadCredential={{$key}}:/etc/credentials/{{$.Name}}/{{$key}}{{end}}

{{if .Resources.MemoryMax}}MemoryMax={{.Resources.MemoryMax}}{{end}}
{{if .Resources.MemoryHigh}}MemoryHigh={{.Resources.MemoryHigh}}{{end}}
{{if .Resources.CPUQuota}}CPUQuota={{.Resources.CPUQuota}}{{end}}
{{if .Resources.TasksMax}}TasksMax={{.Resources.TasksMax}}{{end}}

[Install]
WantedBy=multi-user.target
`

// JobTimerTemplate is the systemd timer unit that schedules a job service.
const JobTimerTemplate = `[Unit]
Description=Unit Timer: {{.Name}} {{.Job.Command}}

[Timer]
OnCalendar={{.Job.OnCalendar}}
{{if .Job.Persistent}}Persistent=true{{end}}
Unit={{.Name}}-{{.Job.Command}}.service

[Install]
WantedBy=timers.target
`

// CaddySnippetTemplate is the Caddy reverse proxy configuration template.
const CaddySnippetTemplate = `{{.Domain}} {
    reverse_proxy localhost:{{.Port}}
}
`
