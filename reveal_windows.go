package main

import (
	"os/exec"
	"strings"
	"syscall"
)

// runReveal starts Explorer with the command line exactly as revealCommand made it: Go's own quoting of the
// argument would add a backslash before the quotes, which Explorer does not read.
func runReveal(c command) error {
	cmd := exec.Command(c.name)
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: c.name + " " + strings.Join(c.args, " ")}
	return cmd.Start() // Explorer's exit code says nothing about success
}
