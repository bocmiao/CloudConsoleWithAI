// Package scripts embeds the shell scripts that Miao Panel runs on servers.
package scripts

import _ "embed"

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
