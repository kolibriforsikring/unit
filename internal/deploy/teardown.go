package deploy

import (
	"fmt"
	"strings"

	"github.com/kolibriforsikring/unit/internal/config"
	"golang.org/x/crypto/ssh"
)

// Teardown completely uninstalls the application from the server.
// It stops the service, removes systemd files, caddy configs, secrets, and the deployment directory.
func Teardown(client *ssh.Client, cfg *config.Config) error {
	fmt.Printf("🗑️  Unit: Starting teardown for '%s'...\n", cfg.Name)

	// 1. Stop and disable systemd services and timers
	fmt.Println("    → Stopping and disabling systemd services...")
	if err := runRemoteCmd(client, fmt.Sprintf("sudo systemctl stop %s.service %s.socket || true", cfg.Name, cfg.Name)); err != nil {
		fmt.Printf("      ⚠️  Failed to stop services: %v\n", err)
	}
	if err := runRemoteCmd(client, fmt.Sprintf("sudo systemctl disable %s.service %s.socket || true", cfg.Name, cfg.Name)); err != nil {
		fmt.Printf("      ⚠️  Failed to disable services: %v\n", err)
	}

	for _, job := range cfg.Jobs {
		timerName := fmt.Sprintf("%s-%s.timer", cfg.Name, job.Command)
		serviceName := fmt.Sprintf("%s-%s.service", cfg.Name, job.Command)
		fmt.Printf("    → Stopping job timer: %s\n", timerName)
		if err := runRemoteCmd(client, fmt.Sprintf("sudo systemctl stop %s || true", timerName)); err != nil {
			fmt.Printf("      ⚠️  Failed to stop timer: %v\n", err)
		}
		if err := runRemoteCmd(client, fmt.Sprintf("sudo systemctl disable %s %s || true", timerName, serviceName)); err != nil {
			fmt.Printf("      ⚠️  Failed to disable timer: %v\n", err)
		}
	}

	// 2. Remove systemd unit files
	fmt.Println("    → Removing systemd unit files...")
	if err := runRemoteCmd(client, fmt.Sprintf("sudo rm -f /etc/systemd/system/%s.service /etc/systemd/system/%s.socket", cfg.Name, cfg.Name)); err != nil {
		return fmt.Errorf("failed to remove systemd files: %w", err)
	}

	for _, job := range cfg.Jobs {
		pattern := fmt.Sprintf("/etc/systemd/system/%s-%s.*", cfg.Name, job.Command)
		if err := runRemoteCmd(client, fmt.Sprintf("sudo rm -f %s", pattern)); err != nil {
			fmt.Printf("      ⚠️  Failed to remove job unit files for %s: %v\n", job.Command, err)
		}
	}

	// Reload systemd daemon
	if err := runRemoteCmd(client, "sudo systemctl daemon-reload"); err != nil {
		return fmt.Errorf("failed to reload systemd daemon: %w", err)
	}

	// 3. Remove Caddy config and reload
	if cfg.Domain != "" {
		fmt.Println("    → Removing Caddy proxy configuration...")
		caddyConfPath := fmt.Sprintf("/etc/caddy/conf.d/%s.conf", cfg.Name)
		if err := runRemoteCmd(client, fmt.Sprintf("sudo rm -f %s", caddyConfPath)); err != nil {
			fmt.Printf("      ⚠️  Failed to remove Caddy config: %v\n", err)
		} else {
			if err := runRemoteCmd(client, "sudo systemctl reload caddy || true"); err != nil {
				fmt.Printf("      ⚠️  Failed to reload Caddy: %v\n", err)
			}
		}
	}

	// 4. Remove credentials
	if len(cfg.Secrets) > 0 || cfg.SecretScript != "" {
		fmt.Println("    → Removing credentials...")
		credsDir := fmt.Sprintf("/etc/credentials/%s", cfg.Name)
		if err := runRemoteCmd(client, fmt.Sprintf("sudo rm -rf %s", credsDir)); err != nil {
			fmt.Printf("      ⚠️  Failed to remove credentials: %v\n", err)
		}
	}

	// 5. Remove deployment directory
	deployDir := fmt.Sprintf("%s/%s", cfg.DeployPath, cfg.Name)
	fmt.Printf("    → Removing deployment directory (%s)...\n", deployDir)
	if err := runRemoteCmd(client, fmt.Sprintf("sudo rm -rf %s", deployDir)); err != nil {
		return fmt.Errorf("failed to remove deployment directory: %w", err)
	}

	fmt.Println("✅ Teardown complete. Application uninstalled.")
	return nil
}

// runRemoteCmd is a small helper to execute a single command over SSH
func runRemoteCmd(client *ssh.Client, cmd string) error {
	session, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("failed to create session: %w", err)
	}
	defer session.Close()

	output, err := session.CombinedOutput(cmd)
	if err != nil {
		return fmt.Errorf("command '%s' failed: %w\nOutput: %s", cmd, err, strings.TrimSpace(string(output)))
	}
	return nil
}
