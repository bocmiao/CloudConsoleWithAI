package actions

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bocmiao/CloudConsoleWithAI/internal/onepanel"
	"github.com/bocmiao/CloudConsoleWithAI/internal/sshx"
	"github.com/bocmiao/CloudConsoleWithAI/scripts"
)

// Env is what running an action on one server needs.
type Env struct {
	SSH  *sshx.Client
	User string // login user; others than root go through passwordless sudo
	// OnePanel is set when the server's 1Panel API is configured.
	OnePanel *onepanel.Client
	// PanelApps is the 1Panel app list from discovery, e.g. "mysql/mysql".
	PanelApps []string
	// Reconnect replaces SSH after a dropped connection while waiting.
	Reconnect func(ctx context.Context) (*sshx.Client, error)
	// PollInterval defaults to one second.
	PollInterval time.Duration
}

// Step outcomes.
const (
	StatusDone       = "done"
	StatusRefused    = "refused"     // precondition not met, nothing changed
	StatusRolledBack = "rolled_back" // failed, changes undone
	StatusFailed     = "failed"      // failed, may need a person
	StatusUndone     = "undone"
)

// Outcome is the result of running one action.
type Outcome struct {
	Status string            `json:"status"`
	Log    []string          `json:"log"`
	Undo   map[string]string `json:"undo,omitempty"`
	Result map[string]string `json:"result,omitempty"`
}

func (o *Outcome) logf(format string, args ...any) {
	o.Log = append(o.Log, fmt.Sprintf(format, args...))
}

// Progress receives the log lines so far while an action runs.
type Progress func(log []string)

// Apply runs a validated step.
func Apply(ctx context.Context, env *Env, r Resolved, progress Progress) Outcome {
	if r.Impl.Script != "" {
		return runScript(ctx, env, r, "apply", nil, progress)
	}
	return applyPanel(ctx, env, r, progress)
}

// Undo reverts a step using the data its Apply recorded.
func Undo(ctx context.Context, env *Env, r Resolved, undo map[string]string) Outcome {
	if !r.Cap.Reversible {
		return Outcome{Status: StatusRefused, Log: []string{"这个操作不能撤销"}}
	}
	if r.Impl.Script != "" {
		return runScript(ctx, env, r, "undo", undo, nil)
	}
	return undoPanel(ctx, env, r, undo)
}

// ---- Scripts ----

const remoteDir = "/var/tmp/miaopanel"

// loadScript returns an action script's text; tests replace it.
var loadScript = scripts.Action

// shq quotes s for a POSIX shell.
func shq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

var envKeyRe = regexp.MustCompile(`[^A-Za-z0-9]`)

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// runScript uploads an action script and runs it detached from the SSH
// session (so a dropped connection cannot kill it halfway), then polls
// for its exit code.
func runScript(ctx context.Context, env *Env, r Resolved, mode string, undo map[string]string, progress Progress) Outcome {
	out := Outcome{}
	script, err := loadScript(r.Impl.Script)
	if err != nil {
		out.Status = StatusFailed
		out.logf("%v", err)
		return out
	}
	sudo := ""
	if env.User != "root" {
		sudo = "sudo -n "
	}
	id := newID()
	base := remoteDir + "/" + id

	args := []string{mode}
	for _, name := range r.Impl.Args {
		args = append(args, r.Values[name])
	}
	vars := []string{"MIAO_BACKUP_DIR=" + shq("/var/backups/miaopanel/"+time.Now().Format("20060102-150405")+"-"+id)}
	keys := make([]string, 0, len(undo))
	for k := range undo {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		vars = append(vars, "UNDO_"+envKeyRe.ReplaceAllString(k, "_")+"="+shq(undo[k]))
	}
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = shq(a)
	}
	inner := fmt.Sprintf("%s sh %s.sh %s >%s.log 2>&1 </dev/null; echo $? >%s.rc",
		strings.Join(vars, " "), base, strings.Join(quoted, " "), base, base)
	start := "cd " + remoteDir + " && if command -v setsid >/dev/null 2>&1; then setsid sh -c " + shq(inner) +
		" >/dev/null 2>&1 </dev/null & else nohup sh -c " + shq(inner) + " >/dev/null 2>&1 </dev/null & fi"

	up, err := env.SSH.Run(ctx, sudo+"sh -c "+shq("umask 077; mkdir -p "+remoteDir+" && cat >"+base+".sh"), script, 4096)
	if err != nil || up.ExitCode != 0 {
		out.Status = StatusRefused
		if env.User != "root" {
			out.logf("需要 root 权限或免密 sudo 才能修改服务器（%s）", strings.TrimSpace(up.Stderr))
		} else {
			out.logf("上传脚本失败：%v %s", err, strings.TrimSpace(up.Stderr))
		}
		return out
	}
	if res, err := env.SSH.Run(ctx, sudo+"sh -c "+shq(start), "", 4096); err != nil || res.ExitCode != 0 {
		out.Status = StatusFailed
		out.logf("启动失败：%v %s", err, strings.TrimSpace(res.Stderr))
		return out
	}

	interval := env.PollInterval
	if interval == 0 {
		interval = time.Second
	}
	poll := sudo + "sh -c " + shq("cat "+base+".rc 2>/dev/null; echo '<<MIAO-LOG>>'; cat "+base+".log 2>/dev/null")
	failures := 0
	for {
		select {
		case <-ctx.Done():
			out.Status = StatusFailed
			out.logf("等待超时：操作可能还在服务器上运行，请稍后重新识别服务器确认结果")
			return out
		case <-time.After(interval):
		}
		res, err := env.SSH.Run(ctx, poll, "", 1<<20)
		if err != nil {
			// The action keeps running on the server; reconnect and keep waiting.
			failures++
			if failures > 30 || env.Reconnect == nil {
				out.Status = StatusFailed
				out.logf("和服务器的连接断开了：%v", err)
				return out
			}
			if c, rerr := env.Reconnect(ctx); rerr == nil {
				env.SSH = c
			}
			continue
		}
		failures = 0
		rc, log, _ := strings.Cut(res.Stdout, "<<MIAO-LOG>>\n")
		parsed := parseProtocol(log)
		if progress != nil {
			progress(parsed.Log)
		}
		rc = strings.TrimSpace(rc)
		if rc == "" {
			continue
		}
		_, _ = env.SSH.Run(ctx, sudo+"rm -f "+base+".sh "+base+".rc "+base+".log", "", 1024)
		code, _ := strconv.Atoi(rc)
		parsed.Status = statusForExit(code)
		if mode == "undo" && parsed.Status == StatusDone {
			parsed.Status = StatusUndone
		}
		return parsed
	}
}

func statusForExit(code int) string {
	switch code {
	case 0:
		return StatusDone
	case 10:
		return StatusRefused
	case 20:
		return StatusRolledBack
	}
	return StatusFailed
}

// parseProtocol reads the MIAO_* lines an action script prints. Other
// output (error messages from commands) is kept in the log.
func parseProtocol(text string) Outcome {
	o := Outcome{Undo: map[string]string{}, Result: map[string]string{}}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case strings.HasPrefix(line, "MIAO_INFO "):
			o.Log = append(o.Log, strings.TrimPrefix(line, "MIAO_INFO "))
		case strings.HasPrefix(line, "MIAO_UNDO "):
			if k, v, ok := strings.Cut(strings.TrimPrefix(line, "MIAO_UNDO "), "="); ok {
				o.Undo[k] = v
			}
		case strings.HasPrefix(line, "MIAO_RESULT "):
			if k, v, ok := strings.Cut(strings.TrimPrefix(line, "MIAO_RESULT "), "="); ok {
				o.Result[k] = v
			}
		case strings.TrimSpace(line) != "":
			if len(line) > 300 {
				line = line[:300]
			}
			o.Log = append(o.Log, line)
		}
	}
	return o
}

// ---- 1Panel API ----

func applyPanel(ctx context.Context, env *Env, r Resolved, progress Progress) Outcome {
	out := Outcome{Undo: map[string]string{}}
	if env.OnePanel == nil {
		out.Status = StatusRefused
		out.logf("还没有配置这台服务器的 1Panel 接口，请在服务器页面填写 API 密钥")
		return out
	}
	report := func(format string, args ...any) {
		out.logf(format, args...)
		if progress != nil {
			progress(out.Log)
		}
	}
	switch r.Impl.Panel {
	case "php_fpm":
		return applyFPM(ctx, env.OnePanel, r.Values, out, report)
	case "mysql_vars":
		return applyMySQL(ctx, env, r.Values, out, report)
	}
	out.Status = StatusFailed
	out.logf("未知的面板操作 %s", r.Impl.Panel)
	return out
}

func findRuntime(ctx context.Context, c *onepanel.Client, name string) (onepanel.Runtime, error) {
	list, err := c.PHPRuntimes(ctx)
	if err != nil {
		return onepanel.Runtime{}, err
	}
	var names []string
	for _, rt := range list {
		if name != "" && rt.Name == name {
			return rt, nil
		}
		names = append(names, rt.Name)
	}
	switch {
	case name != "":
		return onepanel.Runtime{}, fmt.Errorf("1Panel 里没有名为 %s 的 PHP 运行环境（现有：%s）", name, strings.Join(names, "、"))
	case len(list) == 1:
		return list[0], nil
	case len(list) == 0:
		return onepanel.Runtime{}, errors.New("1Panel 里没有 PHP 运行环境")
	}
	return onepanel.Runtime{}, fmt.Errorf("1Panel 里有多个 PHP 运行环境（%s），需要指定 runtime", strings.Join(names, "、"))
}

// fpmParams computes consistent process-manager settings.
func fpmParams(maxChildren int, pm string) map[string]string {
	p := map[string]string{"pm": pm, "pm.max_children": strconv.Itoa(maxChildren)}
	switch pm {
	case "dynamic":
		minS := max(maxChildren/8, 1)
		maxS := maxChildren / 2
		if maxS <= minS {
			maxS = minS + 1
		}
		start := min(max(maxChildren/4, minS), maxS)
		p["pm.start_servers"] = strconv.Itoa(start)
		p["pm.min_spare_servers"] = strconv.Itoa(minS)
		p["pm.max_spare_servers"] = strconv.Itoa(maxS)
	case "ondemand":
		p["pm.process_idle_timeout"] = "10s"
	}
	return p
}

func applyFPM(ctx context.Context, c *onepanel.Client, v map[string]string, out Outcome, report func(string, ...any)) Outcome {
	rt, err := findRuntime(ctx, c, v["runtime"])
	if err != nil {
		out.Status = StatusRefused
		out.logf("%v", err)
		return out
	}
	old, err := c.FPMConfig(ctx, rt.ID)
	if err != nil {
		out.Status = StatusRefused
		out.logf("读取当前配置失败：%v", err)
		return out
	}
	prev := map[string]string{}
	for k, val := range old {
		prev[k] = fmt.Sprint(val)
	}
	prevJSON, _ := json.Marshal(prev)
	out.Undo["runtime_id"] = strconv.FormatUint(uint64(rt.ID), 10)
	out.Undo["params"] = string(prevJSON)

	n, _ := strconv.Atoi(v["max_children"])
	next := fpmParams(n, v["pm"])
	report("正在修改 PHP 运行环境 %s：pm = %s，pm.max_children = %d（原来 %s = %s）", rt.Name, v["pm"], n, "pm.max_children", prev["pm.max_children"])
	if err := c.UpdateFPMConfig(ctx, rt.ID, next); err != nil {
		report("修改失败：%v，正在恢复原来的配置", err)
		if rerr := c.UpdateFPMConfig(ctx, rt.ID, prev); rerr != nil {
			out.Status = StatusFailed
			out.logf("恢复也失败了：%v", rerr)
			return out
		}
		out.Status = StatusRolledBack
		return out
	}
	report("完成：1Panel 已写入新配置并重启了 PHP 运行环境")
	out.Status = StatusDone
	return out
}

// mysqlApp picks the MySQL/MariaDB app to change from the discovered list.
func mysqlApp(apps []string, name string) (dbType, appName string, err error) {
	var found []string
	for _, a := range apps {
		t, n, ok := strings.Cut(a, "/")
		if !ok || (t != "mysql" && t != "mariadb") {
			continue
		}
		if name != "" && n == name {
			return t, n, nil
		}
		found = append(found, a)
	}
	switch {
	case name != "":
		return "", "", fmt.Errorf("1Panel 里没有名为 %s 的 MySQL 应用", name)
	case len(found) == 1:
		t, n, _ := strings.Cut(found[0], "/")
		return t, n, nil
	case len(found) == 0:
		return "", "", errors.New("没有发现 1Panel 安装的 MySQL / MariaDB（请先重新识别服务器）")
	}
	return "", "", fmt.Errorf("有多个 MySQL 应用（%s），需要指定 database", strings.Join(found, "、"))
}

func applyMySQL(ctx context.Context, env *Env, v map[string]string, out Outcome, report func(string, ...any)) Outcome {
	c := env.OnePanel
	dbType, name, err := mysqlApp(env.PanelApps, v["database"])
	if err != nil {
		out.Status = StatusRefused
		out.logf("%v", err)
		return out
	}
	cur, err := c.MySQLVariables(ctx, dbType, name)
	if err != nil {
		out.Status = StatusRefused
		out.logf("读取当前 MySQL 参数失败：%v", err)
		return out
	}
	var next, prev []onepanel.MySQLVariable
	if mb := v["innodb_buffer_pool_size_mb"]; mb != "" {
		n, _ := strconv.Atoi(mb)
		next = append(next, onepanel.MySQLVariable{Param: "innodb_buffer_pool_size", Value: float64(n) * 1024 * 1024})
		prev = append(prev, onepanel.MySQLVariable{Param: "innodb_buffer_pool_size", Value: cur["innodb_buffer_pool_size"]})
	}
	if mc := v["max_connections"]; mc != "" {
		// A string, so 1Panel does not turn 1024 into "1K".
		next = append(next, onepanel.MySQLVariable{Param: "max_connections", Value: mc})
		prev = append(prev, onepanel.MySQLVariable{Param: "max_connections", Value: cur["max_connections"]})
	}
	prevJSON, _ := json.Marshal(prev)
	out.Undo["db_type"], out.Undo["db_name"], out.Undo["variables"] = dbType, name, string(prevJSON)
	report("正在修改 %s 的参数（原来：缓冲池 %s，最大连接数 %s）", name, cur["innodb_buffer_pool_size"], cur["max_connections"])
	if err := c.UpdateMySQLVariables(ctx, dbType, name, next); err != nil {
		report("修改失败：%v，正在恢复原来的参数", err)
		if rerr := c.UpdateMySQLVariables(ctx, dbType, name, prev); rerr != nil {
			out.Status = StatusFailed
			out.logf("恢复也失败了：%v", rerr)
			return out
		}
		out.Status = StatusRolledBack
		return out
	}
	report("完成：1Panel 已写入新参数并重启了 MySQL")
	out.Status = StatusDone
	return out
}

func undoPanel(ctx context.Context, env *Env, r Resolved, undo map[string]string) Outcome {
	out := Outcome{}
	if env.OnePanel == nil {
		out.Status = StatusRefused
		out.logf("还没有配置这台服务器的 1Panel 接口")
		return out
	}
	var err error
	switch r.Impl.Panel {
	case "php_fpm":
		id, _ := strconv.ParseUint(undo["runtime_id"], 10, 64)
		prev := map[string]string{}
		if err = json.Unmarshal([]byte(undo["params"]), &prev); err == nil {
			err = env.OnePanel.UpdateFPMConfig(ctx, uint(id), prev)
		}
	case "mysql_vars":
		var prev []onepanel.MySQLVariable
		if err = json.Unmarshal([]byte(undo["variables"]), &prev); err == nil {
			err = env.OnePanel.UpdateMySQLVariables(ctx, undo["db_type"], undo["db_name"], prev)
		}
	default:
		err = fmt.Errorf("未知的面板操作 %s", r.Impl.Panel)
	}
	if err != nil {
		out.Status = StatusFailed
		out.logf("撤销失败：%v", err)
		return out
	}
	out.Status = StatusUndone
	out.logf("已撤销：恢复了原来的配置")
	return out
}
