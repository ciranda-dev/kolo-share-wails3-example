//go:build !windows

package main

import "os/exec"

// runReveal starts the command with its arguments as they are, without a shell, and waits for it.
func runReveal(c command) error {
	return exec.Command(c.name, c.args...).Run()
}
