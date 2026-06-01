// Package secrets handles loading environment variables from external scripts.
package secrets

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// isUserDefined filters out common system environment variables to avoid
// uploading shell internals as credentials.
func isUserDefined(key string) bool {
	switch key {
	case "PATH", "PWD", "SHLVL", "HOME", "_", "LANG", "LC_ALL", "USER", "LOGNAME", "SHELL", "TERM":
		return false
	}
	return strings.ToUpper(key) == key
}

// GetSecrets executes the provided script and extracts the environment variables it sets.
// The dest argument (e.g. "dev", "prod") is passed to the script as $1.
// Uses env -0 (null-delimited output) so values containing newlines — PEM keys,
// certificates, JSON blobs — are captured correctly.
func GetSecrets(scriptPath string, dest string) (map[string]string, error) {
	if scriptPath == "" {
		return nil, nil
	}

	// Snapshot the caller's environment before sourcing the script.
	initialEnv := make(map[string]string)
	for _, e := range os.Environ() {
		if k, v, ok := strings.Cut(e, "="); ok {
			initialEnv[k] = v
		}
	}

	// Source the script then dump the environment as null-delimited entries.
	// env -0 separates records with \0 instead of \n, so multiline values are safe.
	shellCmd := fmt.Sprintf(". %s %s && env -0", scriptPath, dest)
	cmd := exec.Command("sh", "-c", shellCmd)
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("secret script '%s' failed:\n%s", scriptPath, string(exitErr.Stderr))
		}
		return nil, fmt.Errorf("failed to execute secret script '%s': %w", scriptPath, err)
	}

	secrets := make(map[string]string)
	for _, entry := range bytes.Split(out, []byte{0}) {
		if len(entry) == 0 {
			continue
		}
		k, v, ok := strings.Cut(string(entry), "=")
		if !ok {
			continue
		}
		if initialVal, exists := initialEnv[k]; !exists || initialVal != v {
			if isUserDefined(k) {
				secrets[k] = v
			}
		}
	}

	return secrets, nil
}
