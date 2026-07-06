//go:build windows

// Package hidden suppresses console windows for child processes on Windows.
package hidden

import (
	"os/exec"
	"syscall"
)

const createNoWindow = 0x08000000 // CREATE_NO_WINDOW

// Apply configures cmd so it does not spawn a visible console window.
func Apply(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
}
