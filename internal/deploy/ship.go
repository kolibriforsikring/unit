package deploy

import (
	"fmt"
	"os"
	"path"

	"github.com/kolibriforsikring/unit/internal/config"
	"golang.org/x/crypto/ssh"
)

// Ship takes a local binary and moves it to the remote target releases folder
func Ship(client *ssh.Client, cfg *config.Config, version string) error {
	remoteDir := fmt.Sprintf("%s/%s/releases/%s", cfg.DeployPath, cfg.Name, version)
	remotePath := path.Join(remoteDir, "bin")

	// 1. Create the versioned directory first
	session, err := client.NewSession()
	if err != nil {
		return err
	}
	// No PTY needed here, just a simple command
	if err := session.Run(fmt.Sprintf("mkdir -p %s", remoteDir)); err != nil {
		session.Close()
		return fmt.Errorf("failed to create remote dir: %w", err)
	}
	session.Close()

	// 2. Open local binary
	file, err := os.Open(cfg.Executable)
	if err != nil {
		return fmt.Errorf("failed to open local binary: %w", err)
	}
	defer file.Close()

	// 3. Stream the file over SSH
	shipSession, err := client.NewSession()
	if err != nil {
		return err
	}
	defer shipSession.Close()

	shipSession.Stdin = file

	// rm -f first so we can replace the binary even if the running service has it open.
	cmd := fmt.Sprintf("rm -f %[1]s && cat > %[1]s && chmod +x %[1]s", remotePath)

	fmt.Printf("🚢 Unit: Shipping %s to %s...\n", cfg.Name, remotePath)
	if err := shipSession.Run(cmd); err != nil {
		return fmt.Errorf("failed to stream binary: %w", err)
	}

	return nil
}
