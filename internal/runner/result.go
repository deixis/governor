package runner

// Result holds the output of a command execution.
type Result struct {
	RunID     string // unique identifier for this run
	ExitCode  int    // process exit code
	Stdout    []byte // captured stdout (may be truncated)
	Stderr    []byte // captured stderr (may be truncated)
	Truncated bool   // true if output exceeded the size cap
}
