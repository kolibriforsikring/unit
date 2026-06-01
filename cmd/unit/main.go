// Package main is the entry point for the unit CLI.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/kolibriforsikring/unit/internal/config"
	"github.com/kolibriforsikring/unit/internal/deploy"
	"github.com/kolibriforsikring/unit/internal/secrets"
	"github.com/kolibriforsikring/unit/internal/sshutil"
	"golang.org/x/crypto/ssh"
)

func main() {
	// Manually find the command and separate it from the flags
	command := ""
	args := os.Args[1:]
	for i, arg := range args {
		if arg[0] != '-' {
			command = arg
			// Remove the command from the slice for flag parsing
			args = append(args[:i], args[i+1:]...)
			break
		}
	}

	if command == "" {

		printUsage()
		return
	}

	// Define and parse flags from the remaining arguments
	env := flag.NewFlagSet(command, flag.ExitOnError)
	envFlag := env.String("e", "", "Environment to use (e.g., 'dev', 'prod')")

	// init-specific flags — parsed before config load since no unit.toml exists yet
	if command == "init" {
		nameFlag := env.String("name", "", "App name (required)")
		portFlag := env.Int("port", 8080, "Port the service listens on (0 for jobs-only)")
		env.Parse(args)

		if *nameFlag == "" {
			fmt.Print("App name: ")
			fmt.Scan(nameFlag)
		}
		if *nameFlag == "" {
			fmt.Println("❌ App name is required.")
			os.Exit(1)
		}

		if err := deploy.Init(*nameFlag, *portFlag); err != nil {
			fmt.Printf("❌ Init failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("✅ Created unit.toml for '%s'.\n", *nameFlag)
		fmt.Println("   Next: create unit.<env>.toml with your server host, then run 'unit deploy -e <env>'.")
		return
	}

	env.Parse(args)

	cfg, err := config.Load(*envFlag)
	if err != nil {
		fmt.Printf("❌ Could not load configuration: %v\n", err)
		os.Exit(1)
	}

	if cfg.Server.Host == "" {
		fmt.Println("❌ No server host configured. Specify an environment with -e <env> (e.g. unit logs -e dev)")
		os.Exit(1)
	}
	if cfg.Server.User == "" {
		fmt.Println("❌ No server user configured. Add 'user = \"<user>\"' under [server] in your unit.toml or environment override.")
		os.Exit(1)
	}

	client, err := sshutil.Connect(cfg.Server.User, cfg.Server.Host, cfg.Server.SSHPort, cfg.Server.StrictHostKeyChecking, time.Duration(cfg.Server.Timeout)*time.Second)
	if err != nil {
		log.Fatalf("❌ Failed to connect: %v", err)
	}
	defer client.Close()

	switch command {
	case "setup":
		fmt.Println("🛠️  Unit: Starting setup on " + cfg.Server.Host)
		if err := deploy.Setup(client, cfg); err != nil {
			fmt.Printf("❌ Setup failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("✅ Setup complete.")
	case "deploy":
		startTime := time.Now()
		version := getGitHash()

		// Load secrets from script and upload them

		if cfg.SecretScript != "" {

			fmt.Printf("🤫 Sourcing secrets from script: %s\n", cfg.SecretScript)

			var err error

			cfg.Secrets, err = secrets.GetSecrets(cfg.SecretScript, *envFlag)
			if err != nil {
				log.Fatalf("❌ Failed to load secrets: %v", err)
			}

			if len(cfg.Secrets) > 0 {

				fmt.Printf("    → Found %d secrets: %s\n", len(cfg.Secrets), getKeys(cfg.Secrets))

				if err := deploy.UploadSecrets(client, cfg); err != nil {
					log.Fatalf("❌ Failed to upload secrets: %v", err)
				}

			} else {
				fmt.Println("    → No secrets found in script output.")
			}

		}

		fmt.Printf("🚀 Unit: Deploying version %s\n", version)

		fmt.Println("⚙️  Applying configuration:")

		fmt.Printf("    Name:       %s\n", cfg.Name)
		fmt.Printf("    Executable: %s\n", cfg.Executable)
		fmt.Printf("    Port:       %d\n", cfg.Port)
		fmt.Printf("    Server:     %s@%s\n", cfg.Server.User, cfg.Server.Host)
		fmt.Printf("    Domain:     %s\n", cfg.Domain)
		fmt.Printf("    Secrets script:  %s\n", cfg.SecretScript)
		if len(cfg.Jobs) > 0 {
			fmt.Printf("    Jobs:\n")
			for _, j := range cfg.Jobs {
				fmt.Printf("        %s  [%s]\n", j.Command, j.OnCalendar)
			}
		}

		// 2. Ship the binary
		err = deploy.Ship(client, cfg, version)
		if err != nil {
			log.Fatal(err)
		}

		// 3. Activate — socket service path or jobs-only path
		if cfg.Port != 0 {
			err = deploy.Activate(client, cfg, version)
		} else if len(cfg.Jobs) > 0 {
			err = deploy.ActivateJobsOnly(client, cfg, version)
		} else {
			fmt.Println("❌ No port and no jobs configured — nothing to activate.")
			os.Exit(1)
		}
		if err != nil {
			fmt.Printf("❌ Activation failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("✅ Ship successful.")

		if cfg.Domain != "" {
			err = deploy.SyncProxy(client, cfg.Name, cfg.Domain, cfg.Port)
			if err != nil {
				log.Fatalf("❌ Proxy sync failed: %v", err)
			}
			fmt.Printf("✅ App is live at https://%s\n", cfg.Domain)
		}

		// Run security analysis
		var analysisTarget string
		if cfg.Port != 0 {
			analysisTarget = cfg.Name + ".service"
		} else if len(cfg.Jobs) > 0 {
			analysisTarget = fmt.Sprintf("%s-%s.service", cfg.Name, cfg.Jobs[0].Command)
		}

		if analysisTarget != "" {
			var session *ssh.Session
			var output []byte
			session, err = client.NewSession()
			if err != nil {
				log.Fatalf("❌ Failed to create session for security analysis: %v", err)
			}
			defer session.Close()

			securityCmd := fmt.Sprintf("systemd-analyze security %s | tail -1", analysisTarget)
			output, err = session.CombinedOutput(securityCmd)
			if err != nil {
				fmt.Printf("⚠️  Could not run security analysis: %v\n", err)
			} else {
				fmt.Printf("📊 Security assessment: %s", string(output))
			}
		}
		fmt.Printf("⏱️  Deployment finished in %s\n", time.Since(startTime))
	case "audit":
		fmt.Println("🔎 Unit: running security audit")
		auditTargets := []string{}
		if cfg.Port != 0 {
			auditTargets = append(auditTargets, cfg.Name)
		}
		for _, job := range cfg.Jobs {
			auditTargets = append(auditTargets, fmt.Sprintf("%s-%s", cfg.Name, job.Command))
		}
		for _, target := range auditTargets {
			if err := deploy.Audit(client, target); err != nil {
				fmt.Printf("❌ Audit failed: %v\n", err)
				os.Exit(1)
			}
		}
		fmt.Println("✅ Audit complete.")
	case "uninstall":
		if err := deploy.Teardown(client, cfg); err != nil {
			fmt.Printf("❌ Teardown failed: %v\n", err)
			os.Exit(1)
		}
	case "restart":
		target := cfg.Name
		fmt.Printf("🔄 Restarting %s.service on %s...\n", target, cfg.Server.Host)
		if err := deploy.Restart(client, target); err != nil {
			fmt.Printf("❌ Restart failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("✅ Restarted.")
	case "logs":
		fmt.Println("📡 Unit: streaming logs...")
		logsTarget := cfg.Name
		if cfg.Port == 0 && len(cfg.Jobs) > 0 {
			logsTarget = fmt.Sprintf("%s-%s", cfg.Name, cfg.Jobs[0].Command)
		}
		if err := deploy.StreamLogs(client, logsTarget); err != nil {
			fmt.Printf("❌ Log streaming failed: %v\n", err)
			os.Exit(1)
		}
	default:
		fmt.Printf("Unknown command: %s\n", command)
		printUsage()
	}
}

// getGitHash retrieves the short Git commit hash of the current directory.
// It hard-stops if the working tree has uncommitted changes or unpushed commits,
// so every deployed version is reproducible from the remote.
// Falls back to a timestamp if git is unavailable (not a repo, no git binary).
func getGitHash() string {
	out, err := exec.Command("git", "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		return "manual-" + time.Now().Format("150405")
	}

	if err := exec.Command("git", "diff", "--quiet", "HEAD").Run(); err != nil {
		fmt.Println("❌ Uncommitted changes detected. Commit or stash before deploying.")
		os.Exit(1)
	}

	untracked, err := exec.Command("git", "ls-files", "--others", "--exclude-standard").Output()
	if err == nil && len(bytes.TrimSpace(untracked)) > 0 {
		fmt.Println("❌ Untracked files present. Add them to git or .gitignore before deploying.")
		os.Exit(1)
	}

	// Check if HEAD has been pushed to any remote branch.
	unpushed, err := exec.Command("git", "log", "@{u}..", "--oneline").Output()
	if err == nil && len(bytes.TrimSpace(unpushed)) > 0 {
		fmt.Println("❌ Unpushed commits detected. Push your branch before deploying.")
		os.Exit(1)
	}

	return string(bytes.TrimSpace(out))
}

// printUsage prints the CLI help text.
func printUsage() {
	fmt.Println("Usage: unit <command>")
	fmt.Println("\nCommands:")
	fmt.Println("  init       Generate a unit.toml scaffold in the current directory")
	fmt.Println("  setup      Provision the remote server")
	fmt.Println("  deploy     Ship the binary")
	fmt.Println("  audit      Run a security audit of the systemd service")
	fmt.Println("  restart    Restart the service on the remote server")
	fmt.Println("  logs       Stream service logs")
	fmt.Println("  uninstall  Completely remove the app, services, and configurations from the server")
}

// getKeys is a helper function to extract and format map keys for logging.
func getKeys(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return strings.Join(keys, ", ")
}
