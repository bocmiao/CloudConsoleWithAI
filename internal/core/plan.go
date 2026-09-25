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

	// Filled in when the plan is saved.
	Title      string `json:"title,omitempty"`
	Executable bool   `json:"executable"`
	Blocked    string `json:"blocked,omitempty"` // why it cannot run
	Via        string `json:"via,omitempty"`     // 系统脚本 / 1Panel 接口
	Downtime   string `json:"downtime,omitempty"`
	Reversible bool   `json:"reversible"`

	// Execution state.
	Status     string            `json:"status,omitempty"` // queued, running, done, refused, rolled_back, failed, skipped, undone
	Log        []string          `json:"log,omitempty"`
	Undo       map[string]string `json:"undo,omitempty"`
	FinishedAt string            `json:"finishedAt,omitempty"`
	LogID      int64             `json:"logId,omitempty"` // execution log entry of the latest run
}

// capabilityRisk is the policy table for known capabilities. Anything else
// would have to run as a free command, which is at least R2.
var capabilityRisk = map[string]Risk{
	"backup.create":         R1,
	"container.restart":     R2,
	"java.heap.set":         R2,
	"dns.record.set":        R2,
	"eo.domain.add":         R1,
	"eo.https.set":          R2,
	"cloud.firewall.open":   R1,
	"cloud.firewall.close":  R2,
	"cloud.snapshot.create": R1,
	"cloud.server.reboot":   R3,
	"cloud.server.stop":     R3,
	"cloud.server.start":    R2,
	"eo.cache.purge":        R2,
	"eo.cache.prefetch":     R1,
	"eo.domain.status":      R2,
	"eo.origin.set":         R2,
	"eo.zone.create":        R2,
	"eo.ip.block":           R2,
	"eo.ip.unblock":         R1,
	"eo.ratelimit.set":      R2,
	"eo.ratelimit.remove":   R2,
	"eo.cc.set":             R2,
	"site.create":           R2,
	"snapshot.create":       R1,
	"firewall.open":         R1,
	"swap.set":              R2,
	"php_fpm.set":           R2,
	"php_ini.set":           R2,
	"mysql.vars.set":        R2,
	"redis.conf.set":        R2,
	"app.limits.set":        R2,
	"nginx.conf.set":        R2,
	"logs.clean":            R2,
	"service.restart":       R2,
	"free_command":          R2,
	"package.install":       R3,
	"server.reboot":         R3,
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

// Plan statuses.
const (
	PlanProposed = "proposed"
	PlanRunning  = "running"
	PlanDone     = "done"    // every selected step succeeded
	PlanPartial  = "partial" // some steps did not
)
