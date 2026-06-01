package deploy

import (
	"fmt"
	"path"
	"strings"

	"github.com/kolibriforsikring/unit/internal/config"
	"golang.org/x/crypto/ssh"
)

// UploadSecrets securely uploads credentials to the server using systemd's
// LoadCredential mechanism. It streams each secret directly to a root-owned
// file in /etc/credentials with strict permissions.
func UploadSecrets(client *ssh.Client, cfg *config.Config) error {
	if len(cfg.Secrets) == 0 {
		return nil // No secrets to upload.
	}

	fmt.Println("🔐 Uploading credentials...")

	for key, value := range cfg.Secrets {
		credsDir := fmt.Sprintf("/etc/credentials/%s", cfg.Name)
		remotePath := path.Join(credsDir, key)

		// This script is executed atomically for each secret.
		script := fmt.Sprintf(`
set -e
sudo mkdir -p %[1]s
sudo chown root:root %[1]s
sudo chmod 700 %[1]s
sudo tee %[2]s > /dev/null
sudo chown root:root %[2]s
sudo chmod 600 %[2]s
`,
			credsDir, remotePath)

		session, err := client.NewSession()
		if err != nil {
			return fmt.Errorf("failed to create session for secret '%s': %w", key, err)
		}

		// Pipe the secret value directly to the script's stdin, which `tee` will read.
		session.Stdin = strings.NewReader(value)

		// We run CombinedOutput to get detailed errors from stderr if something fails.
		output, err := session.CombinedOutput(script)
		if err != nil {
			session.Close()
			return fmt.Errorf("failed to upload secret '%s': %w\n---\n%s---", key, err, string(output))
		}
		session.Close()
	}

	fmt.Println("✅ Credentials successfully uploaded.")
	return nil
}
