//go:build !windows

package main

import (
	"os"
	"syscall"
)

// restartSelf replaces this process with the program at exe (just
// updated), keeping its arguments and its process ID, so a service
// manager sees the same process carry on.
func restartSelf(exe string) error {
	return syscall.Exec(exe, os.Args, os.Environ())
}
