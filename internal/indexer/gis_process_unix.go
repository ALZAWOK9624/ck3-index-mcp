//go:build !windows

package indexer

import (
	"os"
	"os/exec"
	"runtime"
	"syscall"
)

func gisPlatform() string { return runtime.GOOS + "-x64" }

func configureGISCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killGISProcessTree(process *os.Process) {
	if process == nil {
		return
	}
	_ = syscall.Kill(-process.Pid, syscall.SIGKILL)
	_ = process.Kill()
}
