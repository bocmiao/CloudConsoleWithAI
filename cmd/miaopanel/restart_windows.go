package main

import (
	"os"
	"os/exec"
	"strconv"

	"github.com/bocmiao/CloudConsoleWithAI/internal/update"
)

// restartSelf starts the program now on disk (after an update) and ends
// this one; the new one waits for this one to go before taking over.
func restartSelf() error {
	exe, err := update.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, os.Args[1:]...)
	cmd.Env = append(os.Environ(), "MIAO_WAIT_PID="+strconv.Itoa(os.Getpid()))
	if err := cmd.Start(); err != nil {
		return err
	}
	os.Exit(0)
	return nil
}
