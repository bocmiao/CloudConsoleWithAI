//go:build !windows

package main

import (
	"os"
	"syscall"

	"github.com/bocmiao/CloudConsoleWithAI/internal/update"
)

// restartSelf replaces this process with the program now on disk (after
// an update), keeping its arguments and its process ID, so a service
// manager sees the same process carry on.
func restartSelf() error {
	exe, err := update.Executable()
	if err != nil {
		return err
	}
	return syscall.Exec(exe, os.Args, os.Environ())
}
