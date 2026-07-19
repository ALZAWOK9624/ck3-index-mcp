//go:build windows

package indexer

import (
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"
)

func gisPlatform() string { return "windows-x64" }

func configureGISCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

func killGISProcessTree(process *os.Process) {
	if process == nil {
		return
	}
	cmd := exec.Command("taskkill.exe", "/PID", strconv.Itoa(process.Pid), "/T", "/F")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	done := make(chan struct{})
	go func() { _ = cmd.Run(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
	}
	_ = process.Kill()
}
