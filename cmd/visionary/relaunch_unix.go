//go:build !windows

package main

import (
	"os"
	"syscall"
)

// relaunch replaces this process with a fresh one of the same program,
// arguments and environment. The process id stays, so systemd and container
// runtimes see the same service.
func relaunch() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return syscall.Exec(exe, os.Args, os.Environ())
}
