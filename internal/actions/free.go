package actions

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/bocmiao/CloudConsoleWithAI/internal/core"
)

// FileDiff is how one file changed in a dry run.
type FileDiff = core.FileDiff

// DryRunResult is what running a free command in an isolated layer showed.
type DryRunResult struct {
	Supported bool       `json:"supported"`
	Reason    string     `json:"reason,omitempty"` // why it could not be tried
	ExitCode  int        `json:"exitCode"`
	Files     []FileDiff `json:"files,omitempty"`
	Services  []string   `json:"services,omitempty"` // service control the command attempted, recorded not done
	Extra     []string   `json:"extra,omitempty"`    // files written that were not declared
	Output    string     `json:"output,omitempty"`
	// Expect is the declared files' state before the run; applying refuses
	// when they changed since.
	Expect string `json:"-"`
}

// DryRun runs a free command on the server with its file changes kept in
// a throwaway layer and its service reloads only recorded.
func DryRun(ctx context.Context, env *Env, r Resolved) (DryRunResult, error) {
	script, err := loadScript(r.Impl.Script)
	if err != nil {
		return DryRunResult{}, err
	}
	args := append([]string{"dryrun"}, scriptArgs(r)...)
	res, err := env.SSH.RunScript(ctx, env.User, script, args, 1<<20)
	if err != nil {
		return DryRunResult{}, err
	}
	if res.ExitCode != 0 {
		return DryRunResult{}, fmt.Errorf("试运行没能开始（退出码 %d）：%s %s", res.ExitCode,
			strings.TrimSpace(parseProtocol(res.Stdout).LogText()), strings.TrimSpace(res.Stderr))
	}
	return parseDryRun(res.Stdout), nil
}

// LogText joins an outcome's log lines.
func (o Outcome) LogText() string { return strings.Join(o.Log, "；") }

func parseDryRun(text string) DryRunResult {
	var d DryRunResult
	var expect, output []string
	var cur *FileDiff
	for _, line := range strings.Split(text, "\n") {
		tag, rest, _ := strings.Cut(line, " ")
		switch tag {
		case "MIAO_HASH":
			expect = append(expect, rest)
		case "MIAO_DRY":
			status, why, _ := strings.Cut(rest, " ")
			d.Supported, d.Reason = status == "ok", why
		case "MIAO_DRY_RC":
			d.ExitCode, _ = strconv.Atoi(rest)
		case "MIAO_FILE":
			d.Files = append(d.Files, FileDiff{Path: rest})
			cur = &d.Files[len(d.Files)-1]
		case "MIAO_D":
			if cur != nil {
				cur.Diff += rest + "\n"
			}
		case "MIAO_SVC":
			d.Services = append(d.Services, rest)
		case "MIAO_EXTRA":
			d.Extra = append(d.Extra, rest)
		case "MIAO_O":
			output = append(output, rest)
		case "MIAO_INFO":
			if !d.Supported && d.Reason == "" {
				d.Reason = rest
			}
		}
	}
	d.Expect = strings.Join(expect, "\n")
	d.Output = strings.Join(output, "\n")
	return d
}
