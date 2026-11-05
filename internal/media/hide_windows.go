//go:build windows

package media

import (
	"os/exec"
	"syscall"
)

// hideWindow stops a console window from flashing up for every conversion.
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
}
