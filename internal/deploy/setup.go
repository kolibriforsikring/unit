package deploy

import (
	"fmt"
	"os"

	"github.com/kolibriforsikring/unit/internal/config"
	"golang.org/x/crypto/ssh"
)

// Setup creates the necessary directories and permissions on the remote server.
func Setup(client *ssh.Client, cfg *config.Config) error {
	session, err := client.NewSession()
	if err != nil {
		return err
	}
	defer session.Close()

	session.Stdout = os.Stdout
	session.Stderr = os.Stderr

	provisionScript := fmt.Sprintf(`
set -e

echo "📂 Creating Unit directories..."
# Base directory for all apps
sudo mkdir -p %[1]s

echo "🔌 Preparing Caddy drop-in directory..."
# Ensure Caddy can read our site snippets
sudo mkdir -p /etc/caddy/conf.d

echo "🔐 Setting permissions..."
# Give the deploy user (whoami) ownership of %[1]s 
# so 'unit ship' can upload files without sudo.
sudo chown -R $(whoami):$(whoami) %[1]s
sudo chmod 755 %[1]s

# Caddy usually runs as user 'caddy', so it needs to read this folder
sudo chown -R root:root /etc/caddy/conf.d
sudo chmod 755 /etc/caddy/conf.d
`, cfg.DeployPath)

	fmt.Println("🚀 Running provision script...")
	if err := session.Run(provisionScript); err != nil {
		return fmt.Errorf("provision script failed: %w", err)
	}

	fmt.Println("✅ Setup complete. Server is ready for 'unit deploy'.")
	return nil
}
