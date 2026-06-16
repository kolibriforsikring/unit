package deploy

import (
	"bytes"
	"fmt"
	"strings"
	"text/template"

	"github.com/kolibriforsikring/unit/internal/config"
	"golang.org/x/crypto/ssh"
)

// TemplateData holds the data needed to fill out the systemd templates.
type TemplateData struct {
	AppName string
	Port    int
	Domain  string
}

// jobTemplateData is passed to JobServiceTemplate and JobTimerTemplate.
type jobTemplateData struct {
	Name       string
	DeployPath string
	Sandbox    config.SandboxConfig
	Resources  config.ResourceConfig
	DependsOn  config.DependsOnConfig
	Env        map[string]string
	Secrets    map[string]string
	Job        config.JobConfig
}

// Activate uploads systemd unit files, updates the release symlink, and reloads the service.
func Activate(client *ssh.Client, cfg *config.Config, version string) error {
	// Upload the Service unit
	if err := renderAndUpload(client, cfg.Name+".service", ServiceTemplate, cfg); err != nil {
		return fmt.Errorf("service upload failed: %w", err)
	}

	// Upload the Socket unit
	if err := renderAndUpload(client, cfg.Name+".socket", SocketTemplate, cfg); err != nil {
		return fmt.Errorf("socket upload failed: %w", err)
	}

	// Atomic Symlink + Systemd Reload
	if err := triggerSystemd(client, cfg, version); err != nil {
		return err
	}

	// Enable any scheduled jobs defined alongside the service
	if len(cfg.Jobs) > 0 {
		if err := activateJobs(client, cfg); err != nil {
			return fmt.Errorf("job activation failed: %w", err)
		}
	}

	return nil
}

// ActivateJobsOnly handles deployments that have no long-running service — only
// scheduled jobs. It swaps the symlink, reloads systemd, then enables the timers.
func ActivateJobsOnly(client *ssh.Client, cfg *config.Config, version string) error {
	currentLinkPath := fmt.Sprintf("%s/%s/current", cfg.DeployPath, cfg.Name)
	newReleasePath := fmt.Sprintf("%s/%s/releases/%s", cfg.DeployPath, cfg.Name, version)

	fmt.Printf("🚀 Activating new version: %s\n", version)
	swapSession, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("failed to create swap session: %w", err)
	}
	defer swapSession.Close()

	swapCmd := fmt.Sprintf("sudo ln -sfn %s %s && sudo systemctl daemon-reload", newReleasePath, currentLinkPath)
	if output, err := swapSession.CombinedOutput(swapCmd); err != nil {
		return fmt.Errorf("symlink swap failed: %w\n%s", err, string(output))
	}

	return activateJobs(client, cfg)
}

// activateJobs uploads and enables the timer units for all configured jobs.
func activateJobs(client *ssh.Client, cfg *config.Config) error {
	for _, job := range cfg.Jobs {
		data := jobTemplateData{
			Name:       cfg.Name,
			DeployPath: cfg.DeployPath,
			Sandbox:    cfg.Sandbox,
			Resources:  cfg.Resources,
			DependsOn:  cfg.DependsOn,
			Env:        cfg.Env,
			Secrets:    cfg.Secrets,
			Job:        job,
		}

		serviceName := fmt.Sprintf("%s-%s.service", cfg.Name, job.Command)
		timerName := fmt.Sprintf("%s-%s.timer", cfg.Name, job.Command)

		fmt.Printf("⏱️  Configuring job: %s (schedule: %s)\n", job.Command, job.OnCalendar)

		if err := renderAndUpload(client, serviceName, JobServiceTemplate, data); err != nil {
			return fmt.Errorf("job service upload failed (%s): %w", job.Command, err)
		}
		if err := renderAndUpload(client, timerName, JobTimerTemplate, data); err != nil {
			return fmt.Errorf("job timer upload failed (%s): %w", job.Command, err)
		}

		enableSession, err := client.NewSession()
		if err != nil {
			return fmt.Errorf("failed to create session for timer enable: %w", err)
		}
		defer enableSession.Close()

		if output, err := enableSession.CombinedOutput(fmt.Sprintf("sudo systemctl enable --now %s", timerName)); err != nil {
			return fmt.Errorf("failed to enable timer %s: %w\n%s", timerName, err, string(output))
		}

		fmt.Printf("    ✅ Timer enabled: %s\n", timerName)
	}
	return nil
}

// 2. The private helper that handles the template + sudo tee pipe
func renderAndUpload(client *ssh.Client, filename, templ string, data any) error {
	t, err := template.New(filename).Parse(templ)
	if err != nil {
		return err
	}

	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return err
	}

	session, err := client.NewSession()
	if err != nil {
		return err
	}
	defer session.Close()

	session.Stdin = &buf
	remotePath := fmt.Sprintf("/etc/systemd/system/%s", filename)

	// We use sudo tee because /etc/systemd/system is root-owned
	return session.Run(fmt.Sprintf("sudo tee %s > /dev/null", remotePath))
}

// fetchJournalLogs retrieves the last n log lines for the given service.
func fetchJournalLogs(client *ssh.Client, serviceName string, lines int) string {
	session, err := client.NewSession()
	if err != nil {
		return fmt.Sprintf("(could not open journal session: %v)", err)
	}
	defer session.Close()

	output, _ := session.CombinedOutput(fmt.Sprintf(
		"journalctl -u %s.service -n %d --no-pager --output=short-precise 2>&1 || true",
		serviceName, lines,
	))
	return strings.TrimSpace(string(output))
}

// printFailureContext prints captured command output and journal logs in a clearly
// delimited block so failure context is immediately visible.
func printFailureContext(cmdOutput []byte, journalLogs string) {
	sep := strings.Repeat("─", 60)
	if len(bytes.TrimSpace(cmdOutput)) > 0 {
		fmt.Printf("\n%s\n  Command output:\n%s\n%s\n", sep, strings.TrimSpace(string(cmdOutput)), sep)
	}
	if journalLogs != "" {
		fmt.Printf("\n%s\n  Recent service logs (journalctl):\n%s\n%s\n\n", sep, journalLogs, sep)
	}
}

// 3. The final shell execution
func triggerSystemd(client *ssh.Client, cfg *config.Config, version string) error {
	// 1. Capture the 'previous' version before we touch anything
	readSession, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("failed to create session for reading symlink: %w", err)
	}

	currentLinkPath := fmt.Sprintf("%s/%s/current", cfg.DeployPath, cfg.Name)
	var stdoutBuf, stderrBuf bytes.Buffer
	readSession.Stdout = &stdoutBuf
	readSession.Stderr = &stderrBuf
	err = readSession.Run(fmt.Sprintf("readlink -f %s", currentLinkPath))
	readSession.Close() // Close session as soon as we're done with it.

	previousRelease := strings.TrimSpace(string(stdoutBuf.Bytes()))
	if err != nil {
		// This can happen on a first deploy, which is not an error.
		fmt.Println("⚠️  Could not determine previous release. Assuming first deploy.")
		previousRelease = ""
	} else {
		fmt.Printf("ℹ️  Previous version was: %s\n", previousRelease)
	}

	newReleasePath := fmt.Sprintf("%s/%s/releases/%s", cfg.DeployPath, cfg.Name, version)

	// This function will be called if activation fails. Journal logs must be
	// fetched by the caller BEFORE rollback so they reflect the failed version.
	rollback := func() error {
		if previousRelease == "" {
			return fmt.Errorf("new version failed to start, but no previous version was found to roll back to")
		}
		fmt.Println("❌ Rolling back to previous version...")
		rbSession, err := client.NewSession()
		if err != nil {
			return fmt.Errorf("fatal: failed to create rollback session: %w", err)
		}
		defer rbSession.Close()

		rollbackCmd := fmt.Sprintf(
			"set -e; sudo ln -sfn %s %s; sudo systemctl daemon-reload; sudo systemctl reset-failed %s.service; sudo systemctl restart %s.socket %s.service",
			previousRelease, currentLinkPath, cfg.Name, cfg.Name, cfg.Name,
		)

		output, err := rbSession.CombinedOutput(rollbackCmd)
		if err != nil {
			return fmt.Errorf("fatal: automatic rollback failed: %w\n---\n%s---", err, string(output))
		}
		fmt.Println("✅ Rollback successful. Previous version restored.")
		return fmt.Errorf("deployment failed, but Unit restored the previous version")
	}

	// 2. Perform the Swap and Restart
	activateSession, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("failed to create activation session: %w", err)
	}
	defer activateSession.Close()

	activateCmd := fmt.Sprintf(`
set -e
sudo ln -sfn %s %s
sudo systemctl daemon-reload
sudo systemctl enable %s.socket
sudo systemctl restart %s.socket %s.service
`, newReleasePath, currentLinkPath, cfg.Name, cfg.Name, cfg.Name)

	fmt.Printf("🚀 Activating new version: %s\n", version)
	activateOutput, err := activateSession.CombinedOutput(activateCmd)
	if err != nil {
		fmt.Printf("❌ Activation command failed: %v\n", err)
		printFailureContext(activateOutput, fetchJournalLogs(client, cfg.Name, 30))
		return rollback()
	}

	// 3. Verify health
	fmt.Println("✅ Service restarted. Verifying health...")

	checkSession, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("failed to create health check session: %w", err)
	}
	defer checkSession.Close()

	checkCmd := fmt.Sprintf("systemctl is-active --quiet %s.service", cfg.Name)
	if err := checkSession.Run(checkCmd); err != nil {
		fmt.Printf("❌ Health check failed for new version: %v\n", err)
		printFailureContext(nil, fetchJournalLogs(client, cfg.Name, 30))
		return rollback()
	}

	fmt.Println("✅ Health check passed. New version is live.")
	return nil
}

// SyncProxy uploads a Caddy configuration snippet and reloads the Caddy server.
func SyncProxy(client *ssh.Client, appName string, domain string, port int) error {
	// 1. Prepare the snippet
	data := struct {
		Domain string
		Port   int
	}{
		Domain: domain,
		Port:   port,
	}

	var buf bytes.Buffer
	t := template.Must(template.New("caddy").Parse(CaddySnippetTemplate))
	if err := t.Execute(&buf, &data); err != nil {
		return err
	}
	// 2. Upload to /etc/caddy/conf.d/
	remotePath := fmt.Sprintf("/etc/caddy/conf.d/%s.conf", appName)

	session, err := client.NewSession()
	if err != nil {
		return err
	}
	defer session.Close()

	session.Stdin = &buf
	// We use sudo tee to write to the protected directory
	fmt.Printf("🔌 Unit: Syncing Caddy proxy for %s...\n", domain)
	err = session.Run(fmt.Sprintf("sudo tee %s > /dev/null", remotePath))
	if err != nil {
		return fmt.Errorf("failed to upload caddy config: %w", err)
	}

	// 3. Reload Caddy
	reloadSess, err := client.NewSession()
	if err != nil {
		return err
	}
	defer reloadSess.Close()

	return reloadSess.Run("sudo systemctl reload caddy")
}
