package main

import (
	"os"
	"os/exec"
)

// relaunch starts a fresh copy of this program with the same arguments,
// environment, directory and output, and returns so this one can exit.
// Windows has no exec that keeps the process.
func relaunch() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, os.Args[1:]...)
	cmd.Env = os.Environ()
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if wd, err := os.Getwd(); err == nil {
		cmd.Dir = wd
	}
	return cmd.Start()
}
