// Package scripts embeds the shell scripts that Miao Panel runs on servers.
package scripts

import (
	"embed"
	"fmt"
)

// Discover is the read-only environment discovery script. Pass section
// names as arguments to limit its output; see the header of discover.sh.
//
//go:embed discover.sh
var Discover string

// Sections lists the section names discover.sh accepts. "logs" is only
// produced when requested explicitly.
var Sections = []string{
	"system", "panel", "ports", "services", "procs", "web", "php",
	"db", "docker", "apps", "cron", "security", "health", "logs",
}

// ValidSection reports whether name is a section discover.sh understands.
func ValidSection(name string) bool {
	for _, s := range Sections {
		if s == name {
			return true
		}
	}
	return false
}

//go:embed actions/*.sh
var actions embed.FS

// Action returns the runnable text of an action script: the shared helper
// library followed by the action itself.
func Action(file string) (string, error) {
	lib, err := actions.ReadFile("actions/_lib.sh")
	if err != nil {
		return "", err
	}
	body, err := actions.ReadFile("actions/" + file)
	if err != nil {
		return "", fmt.Errorf("unknown action script %q", file)
	}
	return string(lib) + "\n" + string(body), nil
}
