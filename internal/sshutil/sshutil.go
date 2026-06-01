// Package sshutil provides SSH connection and authentication utilities.
package sshutil

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
)

// Connect returns a functional SSH client, negotiating authentication and strictly verifying the host key.
func Connect(user, target string, port int, strictHostKeyChecking bool, timeout time.Duration) (*ssh.Client, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("could not determine home directory: %w", err)
	}

	knownHostsPath := filepath.Join(homeDir, ".ssh", "known_hosts")
	
	// Ensure known_hosts file exists, otherwise knownhosts.New will fail
	if _, err := os.Stat(knownHostsPath); os.IsNotExist(err) {
		// Create empty known_hosts file with 0600 permissions
		if err := os.MkdirAll(filepath.Dir(knownHostsPath), 0700); err != nil {
			return nil, fmt.Errorf("failed to create .ssh directory: %w", err)
		}
		if err := os.WriteFile(knownHostsPath, []byte(""), 0600); err != nil {
			return nil, fmt.Errorf("failed to create known_hosts file: %w", err)
		}
	}

	hostKeyCallback, err := knownhosts.New(knownHostsPath)
	if err != nil {
		return nil, fmt.Errorf("failed to parse known_hosts: %w", err)
	}

	// We wrap the knownhosts callback to provide TOFU (Trust On First Use) or at least a clear error message.
	interactiveHostKeyCallback := func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		err := hostKeyCallback(hostname, remote, key)
		if err != nil {
			var keyErr *knownhosts.KeyError
			if errors.As(err, &keyErr) {
				if len(keyErr.Want) == 0 {
					// The host is unknown (no keys found for this host)
					return handleUnknownHost(hostname, remote, key, knownHostsPath)
				}
				// The host is known, but the key doesn't match! (MITM attack or host key changed)
				return fmt.Errorf("⚠️  WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED!\n"+
					"IT IS POSSIBLE THAT SOMEONE IS DOING SOMETHING NASTY!\n"+
					"Someone could be eavesdropping on you right now (man-in-the-middle attack)!\n"+
					"It is also possible that a host key has just been changed.\n"+
					"The fingerprint for the %s key sent by the remote host is %s.\n"+
					"Please contact your system administrator or remove the old key from %s.\n"+
					"Host key verification failed.",
					key.Type(), ssh.FingerprintSHA256(key), knownHostsPath)
			}
			return err
		}
		return nil
	}

	authMethods := gatherAuthMethods(homeDir)
	if len(authMethods) == 0 {
		return nil, fmt.Errorf("no valid SSH authentication methods found (agent or private keys)")
	}

	var finalHostKeyCallback ssh.HostKeyCallback
	if !strictHostKeyChecking {
		finalHostKeyCallback = ssh.InsecureIgnoreHostKey()
	} else {
		finalHostKeyCallback = interactiveHostKeyCallback
	}

	config := &ssh.ClientConfig{
		User:            user,
		Auth:            authMethods,
		HostKeyCallback: finalHostKeyCallback,
		Timeout:         timeout,
	}

	addr := fmt.Sprintf("%s:%d", target, port)
	return ssh.Dial("tcp", addr, config)
}
// handleUnknownHost prompts the user to trust the new host key and appends it to known_hosts.
// If not running in a terminal, it fails automatically.
func handleUnknownHost(hostname string, remote net.Addr, key ssh.PublicKey, knownHostsPath string) error {
	fingerprint := ssh.FingerprintSHA256(key)

	// Check if Stdin is a terminal.
	stat, _ := os.Stdin.Stat()
	isTerminal := (stat.Mode() & os.ModeCharDevice) != 0

	if !isTerminal {
		return fmt.Errorf("host key verification failed: no terminal to prompt for trusting unknown host '%s' (set strict_host_key_checking=false in config to bypass)", hostname)
	} else {
		fmt.Printf("The authenticity of host '%s' can't be established.\n", hostname)
		fmt.Printf("%s key fingerprint is %s.\n", key.Type(), fingerprint)
		fmt.Print("Are you sure you want to continue connecting (yes/no)? ")

		reader := bufio.NewReader(os.Stdin)
		response, err := reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("aborted host key verification")
		}

		response = strings.TrimSpace(strings.ToLower(response))
		if response != "yes" && response != "y" {
			return fmt.Errorf("host key verification failed")
		}
	}

	// Append to known_hosts
	f, err := os.OpenFile(knownHostsPath, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("failed to open known_hosts for appending: %w", err)
	}
	defer f.Close()

	// Use knownhosts.Normalize to format the hostname correctly (including port if necessary)
	line := knownhosts.Line([]string{knownhosts.Normalize(remote.String())}, key)
	if _, err := f.WriteString(line + "\n"); err != nil {
		return fmt.Errorf("failed to write to known_hosts: %w", err)
	}

	fmt.Printf("Warning: Permanently added '%s' (%s) to the list of known hosts.\n", hostname, key.Type())
	return nil
}

// gatherAuthMethods tries the SSH agent first, then common private key files.
func gatherAuthMethods(homeDir string) []ssh.AuthMethod {
	var methods []ssh.AuthMethod

	// 1. Try SSH Agent
	if socket := os.Getenv("SSH_AUTH_SOCK"); socket != "" {
		if conn, err := net.Dial("unix", socket); err == nil {
			agentClient := agent.NewClient(conn)
			methods = append(methods, ssh.PublicKeysCallback(agentClient.Signers))
		}
	}

	// 2. Try common private key files
	keyFiles := []string{
		"id_ed25519",
		"id_rsa",
		"id_ecdsa",
	}

	var signers []ssh.Signer
	for _, filename := range keyFiles {
		path := filepath.Join(homeDir, ".ssh", filename)
		keyData, err := os.ReadFile(path)
		if err != nil {
			continue // Skip missing files
		}

		signer, err := ssh.ParsePrivateKey(keyData)
		if err != nil {
			continue // Skip unparseable keys (e.g., encrypted with passphrase, unsupported for now)
		}
		signers = append(signers, signer)
	}

	if len(signers) > 0 {
		methods = append(methods, ssh.PublicKeys(signers...))
	}

	return methods
}
