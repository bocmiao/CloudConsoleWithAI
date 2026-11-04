package main

import (
	"os"
	"os/exec"
	"strconv"
)

// restartSelf starts the program at exe (just updated; this one is now
// exe.old) and ends this one; the new one waits for this one to go
// before taking over.
func restartSelf(exe string) error {
	cmd := exec.Command(exe, os.Args[1:]...)
	cmd.Env = append(os.Environ(), "MIAO_WAIT_PID="+strconv.Itoa(os.Getpid()))
	if err := cmd.Start(); err != nil {
		return err
	}
	os.Exit(0)
	return nil
}
