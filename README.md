# unit

> Deployment tool for statically compiled binaries using systemd — no containers, no daemons, just PID 1.

Describe what your app needs in a toml file. unit generates hardened systemd units, ships the binary over SSH, and activates the service.

---

## Background

At [Kolibri](https://github.com/kolibriforsikring) we build with Go. The stack is deliberately small — Go, htmx, Postgres. Static binaries, no runtime dependencies.

When it came to deployment we kept reaching for containers out of habit. But a statically compiled binary runs on any Linux host as-is. The container adds nothing except complexity: a daemon to manage, an image registry to push to, a base image to pull, a runtime to exec into. We were paying a cost we didn't need to pay.

We got curious: systemd is already running as PID 1 on every Linux server. It has spent two decades accumulating exactly the primitives you'd want for running software safely in production — isolation, secrets, resource limits, socket activation, scheduled jobs. Could it replace the container layer entirely for static binaries?

It can. The gap is ergonomics. A correct, hardened systemd service unit is 40+ lines of directives before you write a single line specific to your app. unit closes that gap — it takes a short description of what your app needs and writes the unit files correctly every time.

This is an experiment born from curiosity, not a prescription. It makes sense when you have a static binary and want to use what Linux already provides rather than adding a layer on top of it.

---

## How it works

unit starts from a default-deny security posture and you whitelist what the app needs:

| What | Default | How to enable |
|---|---|---|
| Outbound network | **Denied** | `allow_outbound_network = true` |
| Filesystem writes | **Denied** | `state_directory`, `logs_directory`, `writable_paths` |
| Capabilities | **None** | not configurable — empty by default |
| Inbound connections | Socket activation | `port = <n>` |
| Secrets | Credential files | `secrets = "./secrets.sh"` |
| Memory | Unconstrained | `memory_max = "512M"` |
| CPU | Unconstrained | `cpu_quota = "50%"` |
| Tasks | Unconstrained | `tasks_max = 512` |

---

## Usage

```sh
go install github.com/kolibriforsikring/unit/cmd/unit@latest

unit init --name myapp --port 8080   # scaffold a unit.toml
unit setup -e prod                    # provision the server once
unit deploy -e prod                   # ship and activate
unit logs -e prod                     # stream journald output
unit restart -e prod                  # restart without redeploying
unit audit -e prod                    # run systemd-analyze security
unit uninstall -e prod                # remove everything
```

Requires Go 1.21+. The binary lands in `$(go env GOPATH)/bin`.

---

## Configuration

`unit.toml` is the base configuration. `unit.<env>.toml` is merged on top — it only needs to contain what differs per environment. Both are committed. Secrets never go in either file, they come from the secrets script. CI deploys by running `unit deploy -e prod` with no additional setup.

```toml
# unit.toml
name       = "api"
executable = "bin/api"
port       = 8080

[sandbox]
allow_outbound_network = true
state_directory        = true   # /var/lib/api, persists across restarts

[resources]
memory_max = "512M"
cpu_quota  = "100%"
tasks_max  = 256

[depends_on]
# requires = hard dependency — if postgres stops, this service stops too
# after    = start ordering only — does not imply dependency
# you typically want both: one for safety, one for correct boot order
after    = ["postgresql.service"]
requires = ["postgresql.service"]
```

```toml
# unit.prod.toml
[server]
host = "prod-1"
user = "deploy"

domain  = "api.example.com"
secrets = "./secrets.prod.sh"
```

---

## Secrets

Provide a script that exports secrets as environment variables. unit runs it locally at deploy time, captures the exports, and uploads them to the server.

The script is safe to commit — it typically just describes how to fetch secrets, not the secrets themselves:

```bash
export DB_PASSWORD=$(op read "op://vault/db/password")
export API_KEY=$(cat ~/.config/myapp/key)
```

On the server, unit writes each secret to `/etc/credentials/<name>/<KEY>` — root-owned, mode 600, inside a directory only root can list. systemd reads these files and injects them into a private memory-backed directory for the service at `$CREDENTIALS_DIRECTORY`. The service reads them as files from there.

This is the recommended secret handling strategy on Linux with systemd. The reason secrets are files rather than environment variables: environment variables are visible in `/proc/<pid>/environ`, leak into core dumps, and are inherited by every child process. A file in a private credential store is scoped to the service and never touches disk.

At runtime:

```go
credDir := os.Getenv("CREDENTIALS_DIRECTORY")
data, _ := os.ReadFile(filepath.Join(credDir, "DB_PASSWORD"))
```

In development there is no `$CREDENTIALS_DIRECTORY`, so the typical pattern is to fall back to a regular environment variable or a local `.env` file:

```go
func secret(name string) string {
    if dir := os.Getenv("CREDENTIALS_DIRECTORY"); dir != "" {
        data, err := os.ReadFile(filepath.Join(dir, name))
        if err == nil {
            return strings.TrimSpace(string(data))
        }
    }
    return os.Getenv(name) // local development fallback
}
```

---

## Scheduled jobs

Each `[[jobs]]` entry generates a `.service` + `.timer` pair. The same binary is invoked with the subcommand as an argument, inheriting the same sandbox and credentials as the main service.

```toml
[[jobs]]
command     = "cleanup"
on_calendar = "daily"
persistent  = true   # catch up on missed runs after downtime
```

---

## HTTPS

Set `domain =` in your environment file and unit writes a Caddy config snippet and reloads it on every deploy. Caddy handles certificate provisioning and renewal automatically — you get HTTPS with no extra steps.

```toml
# unit.prod.toml
domain = "api.example.com"
```

This requires [Caddy](https://caddyserver.com) on the server. If you are not using Caddy, omit `domain =` and handle TLS separately.

---

## Server requirements

- Linux with systemd and cgroup v2 (Ubuntu 22.04+)
- Deploy user with `sudo` rights for `/etc/systemd/system/` and `systemctl`
- SSH agent (`$SSH_AUTH_SOCK`) with a key authorised on the target

---

## Contributing

`cmd/unit-inspector` is a small HTTP server used to verify that unit configures things correctly. It introspects its own sandbox at runtime — credentials, filesystem restrictions, cgroup limits, socket activation. Useful for development and debugging, not something you'd deploy for your own app.

To run it, create `cmd/unit-inspector/unit.<env>.toml` with your server details (see `unit.example.toml` for the format):

```sh
make build
cd cmd/unit-inspector
unit deploy -e <env>
```
