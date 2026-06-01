package deploy

import (
	"bytes"
	"fmt"

	"golang.org/x/crypto/ssh"
)

// Audit runs systemd-analyze security against the deployed service.
func Audit(client *ssh.Client, serviceName string) error {
	fmt.Println("Running security audit for " + serviceName + "...")

	session, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("failed to create session: %w", err)
	}
	defer session.Close()

	var stdoutBuf, stderrBuf bytes.Buffer
	session.Stdout = &stdoutBuf
	session.Stderr = &stderrBuf

	cmd := "systemd-analyze security " + serviceName
	err = session.Run(cmd)

	// Combine stdout and stderr
	var output []byte
	output = append(output, stdoutBuf.Bytes()...)
	output = append(output, stderrBuf.Bytes()...)

	if err != nil {
		return fmt.Errorf("failed to run audit command: %w\nOutput:\n%s", err, string(output))
	}

	fmt.Println(string(output))
	return nil
}
