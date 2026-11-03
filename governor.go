// Package governor provides the version string shared by the CLI and
// MCP server.
package governor

import "runtime/debug"

// Version is the module version of this build, as recorded by the go
// command ("v0.1.0", "v0.1.1-0.20260101120000-abcdef123456"). Source
// builds without a module version report "(devel)".
var Version = func() string {
	info, ok := debug.ReadBuildInfo()
	if !ok || info.Main.Version == "" {
		return "(devel)"
	}
	return info.Main.Version
}()
