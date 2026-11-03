//go:build !windows

package reexec

import "syscall"

// execve replaces the current process with argv. It only returns on
// failure. Replacing rather than spawning keeps stdio, signals and the
// process lifetime exactly as the launching editor or agent expects.
func execve(argv, env []string) error {
	return syscall.Exec(argv[0], argv, env)
}
