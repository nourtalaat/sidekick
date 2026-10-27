//go:build !unix

package validate

import "os/exec"

// setProcessGroup does nothing where there are no process groups; only the
// tool itself is killed when its context ends.
func setProcessGroup(*exec.Cmd) {}
