//go:build windows

package dice

import "os/exec"

func configureJsExecCommand(_ *exec.Cmd) {
	// exec.CommandContext terminates the direct process on Windows. Keeping the
	// default cancellation avoids launching an extra shell or taskkill process.
}
