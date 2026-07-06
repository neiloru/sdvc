//go:build !windows

// Package hidden suppresses console windows for child processes on Windows.
// On non-Windows platforms this is a no-op.
package hidden

import "os/exec"

// Apply is a no-op outside Windows.
func Apply(cmd *exec.Cmd) {}
