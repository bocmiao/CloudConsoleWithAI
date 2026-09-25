// Package core holds the plan and risk model shared by the AI layer and
// the executor.
package core

import "sort"

// Risk levels, see docs/DESIGN.md section 2.3.
type Risk string

const (
	R0 Risk = "R0" // read only
	R1 Risk = "R1" // additive, reversible
	R2 Risk = "R2" // changes something live
	R3 Risk = "R3" // destructive or costs money
)

// Step is one action in a plan. Risk comes from the policy table below,
// never from the AI.
type Step struct {
	Capability string         `json:"capability"`
	Summary    string         `json:"summary"`
	Params     map[string]any `json:"params,omitempty"`
	Risk       Risk           `json:"risk"`
}

// capabilityRisk is the policy table for known capabilities. Anything else
// would have to run as a free command, which is at least R2.
var capabilityRisk = map[string]Risk{
	"backup.create":   R1,
	"snapshot.create": R1,
	"firewall.open":   R1,
	"swap.set":        R2,
	"php_fpm.set":     R2,
	"php_ini.set":     R2,
	"mysql.vars.set":  R2,
	"redis.conf.set":  R2,
	"app.limits.set":  R2,
	"nginx.conf.set":  R2,
	"logs.clean":      R2,
	"service.restart": R2,
	"free_command":    R2,
	"package.install": R3,
	"server.reboot":   R3,
}

// RiskOf returns the policy risk for a capability.
func RiskOf(capability string) Risk {
	if r, ok := capabilityRisk[capability]; ok {
		return r
	}
	return R2
}

// Capabilities lists the known capability names, sorted.
func Capabilities() []string {
	out := make([]string, 0, len(capabilityRisk))
	for c := range capabilityRisk {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// PlanProposed is the only status in M0: plans are recorded, not executed.
const PlanProposed = "proposed"
