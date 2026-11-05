//go:build !windows

package main

import (
	"os"
	"os/exec"
)

// disableEcho hides typed characters and returns a function that restores
// them. It calls stty, because the alternative in Go is a new dependency. If
// stdin is not a terminal, stty fails and there is nothing to hide.
func disableEcho(stdin *os.File) (restore func()) {
	stty := func(arg string) error {
		cmd := exec.Command("stty", arg)
		cmd.Stdin = stdin
		return cmd.Run()
	}
	if err := stty("-echo"); err != nil {
		return func() {}
	}
	return func() { _ = stty("echo") }
}
