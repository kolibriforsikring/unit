package deploy

import (
	"fmt"
	"os"

	"golang.org/x/crypto/ssh"
)

// Restart restarts the named service on the remote server.
func Restart(client *ssh.Client, serviceName string) error {
	session, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("failed to create session: %w", err)
	}
	defer session.Close()

	session.Stdout = os.Stdout
	session.Stderr = os.Stderr

	return session.Run(fmt.Sprintf("sudo systemctl restart %s.service", serviceName))
}

// StreamLogs connects to the remote server and streams the journald logs
// for the specified service back to the local stdout and stderr.
func StreamLogs(client *ssh.Client, serviceName string) error {
	session, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("failed to create session: %w", err)
	}
	defer session.Close()

	session.Stdout = os.Stdout
	session.Stderr = os.Stderr

	// We use "cat" format for a clean look, or "short-iso" for timestamps
	cmd := fmt.Sprintf("sudo journalctl -u %s.service -f -n 50", serviceName)
	return session.Run(cmd)
}
