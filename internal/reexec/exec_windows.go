//go:build windows

package reexec

import (
	"errors"
	"os"
	"os/exec"
)

// execve runs argv as a child sharing this process's stdio and exits with
// its status. Windows has no exec(2), so the parent stays alive as a
// transparent wrapper. It only returns on failure to start.
func execve(argv, env []string) error {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = env
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		os.Exit(exitErr.ExitCode())
	}
	if err != nil {
		return err
	}
	os.Exit(0)
	return nil
}
