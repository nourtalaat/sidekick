//go:build unix

package validate

import (
	"fmt"
	"os/exec"
	"syscall"
)

// setProcessGroup starts the tool in a process group of its own, and makes
// the end of its context kill the whole group, so provider plugins that
// OpenTofu started stop with it.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
			return fmt.Errorf("kill process group %d: %w", cmd.Process.Pid, err)
		}

		return nil
	}
}
