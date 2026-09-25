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
	"github.com/bocmiao/CloudConsoleWithAI/internal/tencent"
	"github.com/bocmiao/CloudConsoleWithAI/scripts"
)

// Env is what running an action on one server needs.
type Env struct {
	SSH  sshx.Conn
	User string // login user; others than root go through passwordless sudo
	// OnePanel is set when the server's 1Panel API is configured.
	OnePanel *onepanel.Client
	// PanelApps is the 1Panel app list from discovery, e.g. "mysql/mysql".
	PanelApps []string
	// Cloud is set when Tencent Cloud credentials are configured.
	Cloud *tencent.Client
	// Reconnect replaces SSH after a dropped connection while waiting.
	Reconnect func(ctx context.Context) (sshx.Conn, error)
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

	// For the execution log.
	Commands     []string `json:"-"` // what ran on the server, as shell text or API requests
	ScriptName   string   `json:"-"`
	Script       string   `json:"-"` // full text of the action script
	BackupDir    string   `json:"-"` // where the script kept its backups, if it made any
	RollbackFile string   `json:"-"` // standalone rollback script left on the server
}

func (o *Outcome) logf(format string, args ...any) {
	o.Log = append(o.Log, fmt.Sprintf(format, args...))
}

// Progress receives the log lines so far while an action runs.
type Progress func(log []string)

// Apply runs a validated step.
func Apply(ctx context.Context, env *Env, r Resolved, progress Progress) Outcome {
	switch {
	case r.Impl.Script != "":
		return runScript(ctx, env, r, "apply", nil, progress)
	case r.Impl.Cloud != "":
		return applyCloud(ctx, env, r, progress)
	}
	return applyPanel(ctx, env, r, progress)
}

// Undo reverts a step using the data its Apply recorded.
func Undo(ctx context.Context, env *Env, r Resolved, undo map[string]string) Outcome {
	if !r.Cap.Reversible {
		return Outcome{Status: StatusRefused, Log: []string{"这个操作不能撤销"}}
	}
	switch {
	case r.Impl.Script != "":
		return runScript(ctx, env, r, "undo", undo, nil)
	case r.Impl.Cloud != "":
		return undoCloud(ctx, env, r, undo)
	}
	return undoPanel(ctx, env, r, undo)
}

// ---- Scripts ----

// Where action scripts run and leave their traces on the server. Tests
// move them somewhere writable.
var (
	remoteDir  = "/var/tmp/miaopanel"
	backupRoot = "/var/backups/miaopanel"
	journalDir = "/var/log/miaopanel" // actions.log: one line per script run
)

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

// undoEnv turns recorded undo data into UNDO_<key>=<value> assignments.
func undoEnv(undo map[string]string) []string {
	keys := make([]string, 0, len(undo))
	for k := range undo {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, "UNDO_"+envKeyRe.ReplaceAllString(k, "_")+"="+shq(undo[k]))
	}
	return out
}

func quoteAll(args []string) string {
	q := make([]string, len(args))
	for i, a := range args {
		q[i] = shq(a)
	}
	return strings.Join(q, " ")
}

func scriptArgs(r Resolved) []string {
	var args []string
	for _, name := range r.Impl.Args {
		args = append(args, r.Values[name])
	}
	return args
}

// runScript uploads an action script and runs it detached from the SSH
// session (so a dropped connection cannot kill it halfway), then polls
// for its exit code. Each run also appends a line to the server's own
// journal, so what Miao Panel did can be read on the server too.
func runScript(ctx context.Context, env *Env, r Resolved, mode string, undo map[string]string, progress Progress) Outcome {
	out := Outcome{ScriptName: r.Impl.Script}
	script, err := loadScript(r.Impl.Script)
	if err != nil {
		out.Status = StatusFailed
		out.logf("%v", err)
		return out
	}
	out.Script = script
	sudo := ""
	if env.User != "root" {
		sudo = "sudo -n "
	}
	id := newID()
	base := remoteDir + "/" + id
	backupDir := backupRoot + "/" + time.Now().Format("20060102-150405") + "-" + id

	args := scriptArgs(r)
	vars := append([]string{"MIAO_BACKUP_DIR=" + shq(backupDir)}, undoEnv(undo)...)
	entry := strings.ReplaceAll(fmt.Sprintf("id=%s action=%s mode=%s args=%s", id, r.Cap.Name, mode, strings.Join(args, ",")), "\n", " ")
	journal := fmt.Sprintf(`(umask 077; mkdir -p %s && printf '%%s %%s rc=%%s\n' "$(date '+%%Y-%%m-%%d %%H:%%M:%%S')" %s "$rc" >>%s/actions.log) 2>/dev/null`,
		journalDir, shq(entry), journalDir)
	inner := fmt.Sprintf("%s sh %s.sh %s >%s.log 2>&1 </dev/null; rc=$?; %s; echo $rc >%s.rc",
		strings.Join(vars, " "), base, quoteAll(append([]string{mode}, args...)), base, journal, base)
	start := "cd " + remoteDir + " && if command -v setsid >/dev/null 2>&1; then setsid sh -c " + shq(inner) +
		" >/dev/null 2>&1 </dev/null & else nohup sh -c " + shq(inner) + " >/dev/null 2>&1 </dev/null & fi"
	upload := sudo + "sh -c " + shq("umask 077; mkdir -p "+remoteDir+" && cat >"+base+".sh")
	startCmd := sudo + "sh -c " + shq(start)
	poll := sudo + "sh -c " + shq("cat "+base+".rc 2>/dev/null; echo '<<MIAO-LOG>>'; cat "+base+".log 2>/dev/null")
	cleanup := sudo + "rm -f " + base + ".sh " + base + ".rc " + base + ".log"
	out.Commands = []string{
		"# 1. 上传脚本（全文见「脚本」）", upload,
		"# 2. 在后台运行脚本（SSH 断开也不会中断），结束后在 " + journalDir + "/actions.log 里记一行", startCmd,
		"# 3. 每秒读取一次进度和结果", poll,
		"# 4. 结束后删除临时文件", cleanup,
	}

	up, err := env.SSH.Run(ctx, upload, script, 4096)
	if err != nil || up.ExitCode != 0 {
		out.Status = StatusRefused
		if env.User != "root" {
			out.logf("需要 root 权限或免密 sudo 才能修改服务器（%s）", strings.TrimSpace(up.Stderr))
		} else {
			out.logf("上传脚本失败：%v %s", err, strings.TrimSpace(up.Stderr))
		}
		return out
	}
	if res, err := env.SSH.Run(ctx, startCmd, "", 4096); err != nil || res.ExitCode != 0 {
		out.Status = StatusFailed
		out.logf("启动失败：%v %s", err, strings.TrimSpace(res.Stderr))
		return out
	}

	interval := env.PollInterval
	if interval == 0 {
		interval = time.Second
	}
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
		_, _ = env.SSH.Run(ctx, cleanup, "", 1024)
		code, _ := strconv.Atoi(rc)
		parsed.Status = statusForExit(code)
		parsed.Commands, parsed.ScriptName, parsed.Script = out.Commands, out.ScriptName, out.Script
		for k := range parsed.Undo {
			if strings.HasPrefix(k, "backup:") {
				parsed.BackupDir = backupDir
			}
		}
		switch {
		case mode == "undo" && parsed.Status == StatusDone:
			parsed.Status = StatusUndone
		case mode == "apply" && parsed.Status == StatusDone && r.Cap.Reversible:
			writeRollbackFile(ctx, env, sudo, r, backupDir, script, &parsed)
		}
		return parsed
	}
}

// writeRollbackFile leaves a self-contained rollback script next to the
// backups on the server, so the change can be reverted even without Miao
// Panel (for example after reinstalling the computer it runs on).
func writeRollbackFile(ctx context.Context, env *Env, sudo string, r Resolved, dir, script string, out *Outcome) {
	path := dir + "/rollback.sh"
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	fmt.Fprintf(&b, "# Miao Panel 回滚文件：撤销「%s」（%s 执行）\n", r.Cap.Title, time.Now().Format("2006-01-02 15:04:05"))
	fmt.Fprintf(&b, "# 用 root 执行即可恢复到修改前：sh %s\n", path)
	b.WriteString("# 在 Miao Panel 里点「回滚」效果相同；从 Miao Panel 回滚后，这个文件会改名为 rollback.sh.done\n")
	fmt.Fprintf(&b, "export MIAO_BACKUP_DIR=%s\n", shq(dir))
	for _, v := range undoEnv(out.Undo) {
		b.WriteString("export " + v + "\n")
	}
	fmt.Fprintf(&b, "set -- %s\n", quoteAll(append([]string{"undo"}, scriptArgs(r)...)))
	b.WriteString(script)
	cmd := sudo + "sh -c " + shq("umask 077; mkdir -p "+dir+" && cat >"+path)
	out.Commands = append(out.Commands, "# 5. 在服务器上保存回滚文件（不依赖 Miao Panel 也能回滚）", cmd)
	res, err := env.SSH.Run(ctx, cmd, b.String(), 4096)
	if err != nil || res.ExitCode != 0 {
		out.logf("（没能在服务器上保存回滚文件：%v %s；仍然可以在 Miao Panel 里回滚）", err, strings.TrimSpace(res.Stderr))
		return
	}
	out.BackupDir, out.RollbackFile = dir, path
}

// RetireRollbackFile renames a rollback file after Miao Panel has rolled
// the change back, so nobody runs it a second time by mistake.
func RetireRollbackFile(ctx context.Context, env *Env, path string) string {
	if path == "" {
		return ""
	}
	sudo := ""
	if env.User != "root" {
		sudo = "sudo -n "
	}
	cmd := sudo + "mv -f " + shq(path) + " " + shq(path+".done")
	_, _ = env.SSH.Run(ctx, cmd, "", 1024)
	return cmd
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

// tracePanel records every 1Panel API request an operation makes.
func tracePanel(env *Env, run func() Outcome) Outcome {
	var cmds []string
	env.OnePanel.Trace = func(method, path string, body []byte) {
		line := method + " " + path
		if len(body) > 0 {
			line += " " + string(body)
		}
		cmds = append(cmds, line)
	}
	defer func() { env.OnePanel.Trace = nil }()
	out := run()
	out.Commands = append([]string{"# 在服务器本机调用 1Panel 接口（经 SSH 隧道，或用自动化助手运行 curl；请求带签名，密钥不记录）；1Panel 自己的「日志审计 → 操作日志」里也有记录"}, cmds...)
	return out
}

func applyPanel(ctx context.Context, env *Env, r Resolved, progress Progress) Outcome {
	out := &Outcome{Undo: map[string]string{}}
	if env.OnePanel == nil {
		out.Status = StatusRefused
		out.logf("还没有配置这台服务器的 1Panel 接口，请在服务器页面填写 API 密钥")
		return *out
	}
	report := func(format string, args ...any) {
		out.logf(format, args...)
		if progress != nil {
			progress(out.Log)
		}
	}
	return tracePanel(env, func() Outcome {
		switch r.Impl.Panel {
		case "php_fpm":
			return applyFPM(ctx, env.OnePanel, r.Values, out, report)
		case "mysql_vars":
			return applyMySQL(ctx, env, r.Values, out, report)
		case "app_limits":
			return applyAppLimits(ctx, env, r.Values, out, report)
		case "backup":
			return applyBackup(ctx, env, r.Values, out, report)
		case "java_heap":
			return applyJavaHeap(ctx, env, r.Values, out, report)
		case "site_create":
			return applySiteCreate(ctx, env, r.Values, out, report)
		}
		out.Status = StatusFailed
		out.logf("未知的面板操作 %s", r.Impl.Panel)
		return *out
	})
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

func applyFPM(ctx context.Context, c *onepanel.Client, v map[string]string, out *Outcome, report func(string, ...any)) Outcome {
	rt, err := findRuntime(ctx, c, v["runtime"])
	if err != nil {
		out.Status = StatusRefused
		out.logf("%v", err)
		return *out
	}
	old, err := c.FPMConfig(ctx, rt.ID)
	if err != nil {
		out.Status = StatusRefused
		out.logf("读取当前配置失败：%v", err)
		return *out
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
			return *out
		}
		out.Status = StatusRolledBack
		return *out
	}
	report("完成：1Panel 已写入新配置并重启了 PHP 运行环境")
	out.Status = StatusDone
	return *out
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

func applyMySQL(ctx context.Context, env *Env, v map[string]string, out *Outcome, report func(string, ...any)) Outcome {
	c := env.OnePanel
	dbType, name, err := mysqlApp(env.PanelApps, v["database"])
	if err != nil {
		out.Status = StatusRefused
		out.logf("%v", err)
		return *out
	}
	cur, err := c.MySQLVariables(ctx, dbType, name)
	if err != nil {
		out.Status = StatusRefused
		out.logf("读取当前 MySQL 参数失败：%v", err)
		return *out
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
			return *out
		}
		out.Status = StatusRolledBack
		return *out
	}
	report("完成：1Panel 已写入新参数并重启了 MySQL")
	out.Status = StatusDone
	return *out
}

func undoPanel(ctx context.Context, env *Env, r Resolved, undo map[string]string) Outcome {
	out := Outcome{}
	if env.OnePanel == nil {
		out.Status = StatusRefused
		out.logf("还没有配置这台服务器的 1Panel 接口")
		return out
	}
	return tracePanel(env, func() Outcome {
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
		case "app_limits":
			err = undoAppLimits(ctx, env.OnePanel, undo)
		case "site_create":
			err = undoSiteCreate(ctx, env.OnePanel, undo)
		case "java_heap":
			id, _ := strconv.ParseUint(undo["install_id"], 10, 64)
			var cfg onepanel.ContainerConfig
			if cfg, err = env.OnePanel.AppConfig(ctx, uint(id)); err == nil {
				err = env.OnePanel.UpdateAppConfig(ctx, uint(id), cfg, undo["compose"])
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
	})
}

// ---- 1Panel apps ----

// findApp picks an installed app by its name, container name or app key.
func findApp(ctx context.Context, c *onepanel.Client, name string) (onepanel.InstalledApp, error) {
	apps, err := c.InstalledApps(ctx)
	if err != nil {
		return onepanel.InstalledApp{}, err
	}
	var byKey []onepanel.InstalledApp
	var names []string
	for _, a := range apps {
		if strings.EqualFold(a.Name, name) || strings.EqualFold(a.Container, name) {
			return a, nil
		}
		if strings.EqualFold(a.AppKey, name) {
			byKey = append(byKey, a)
		}
		names = append(names, a.Name)
	}
	if len(byKey) == 1 {
		return byKey[0], nil
	}
	if len(byKey) > 1 {
		return onepanel.InstalledApp{}, fmt.Errorf("有多个 %s 应用，需要写应用名称（现有：%s）", name, strings.Join(names, "、"))
	}
	return onepanel.InstalledApp{}, fmt.Errorf("1Panel 里没有名为 %s 的应用（现有：%s）", name, strings.Join(names, "、"))
}

var memUsageRe = regexp.MustCompile(`^([0-9.]+)\s*([KMGT]i?B|B)`)

// containerMemMB reads a container's current memory use with docker stats;
// ok is false when it cannot be read.
func containerMemMB(ctx context.Context, env *Env, container string) (float64, bool) {
	if env.SSH == nil || container == "" {
		return 0, false
	}
	sudo := ""
	if env.User != "root" {
		sudo = "sudo -n "
	}
	name := strings.Split(container, ",")[0]
	res, err := env.SSH.Run(ctx, sudo+"docker stats --no-stream --format '{{.MemUsage}}' "+shq(name), "", 4096)
	if err != nil || res.ExitCode != 0 {
		return 0, false
	}
	m := memUsageRe.FindStringSubmatch(strings.TrimSpace(res.Stdout))
	if m == nil {
		return 0, false
	}
	v, _ := strconv.ParseFloat(m[1], 64)
	switch m[2] {
	case "B":
		v /= 1 << 20
	case "KiB", "KB":
		v /= 1 << 10
	case "GiB", "GB":
		v *= 1 << 10
	case "TiB", "TB":
		v *= 1 << 20
	}
	return v, true
}

func limitText(v float64, unit string) string {
	if v <= 0 {
		return "不限制"
	}
	return strconv.FormatFloat(v, 'f', -1, 64) + unit
}

func applyAppLimits(ctx context.Context, env *Env, v map[string]string, out *Outcome, report func(string, ...any)) Outcome {
	c := env.OnePanel
	app, err := findApp(ctx, c, v["app"])
	if err != nil {
		out.Status = StatusRefused
		out.logf("%v", err)
		return *out
	}
	old, err := c.AppConfig(ctx, app.ID)
	if err != nil {
		out.Status = StatusRefused
		out.logf("读取 %s 的当前设置失败：%v", app.Name, err)
		return *out
	}
	mb, _ := strconv.Atoi(v["memory_mb"])
	if mb > 0 {
		if used, ok := containerMemMB(ctx, env, old.ContainerName); ok && float64(mb) < used*1.2 {
			out.Status = StatusRefused
			out.logf("%s 现在就用了约 %.0fMB 内存，上限设成 %dMB 太紧，容易被反复杀掉重启（至少要 %.0fMB）", app.Name, used, mb, used*1.2)
			return *out
		}
	}
	out.Undo["install_id"] = strconv.FormatUint(uint64(app.ID), 10)
	out.Undo["app"] = app.Name
	out.Undo["memory_limit"] = strconv.FormatFloat(old.MemoryLimit, 'f', -1, 64)
	out.Undo["memory_unit"] = old.MemoryUnit

	next := old
	next.MemoryLimit, next.MemoryUnit = float64(mb), "M"
	report("正在修改 %s 的内存上限：%s → %s（1Panel 会重建容器）", app.Name, limitText(old.MemoryLimit, old.MemoryUnit), limitText(float64(mb), "M"))
	restore := func(why string) Outcome {
		report("%s，正在恢复原来的设置", why)
		if rerr := c.UpdateAppConfig(ctx, app.ID, old, ""); rerr != nil {
			out.Status = StatusFailed
			out.logf("恢复也失败了：%v，请在 1Panel「应用商店 → 已安装 → %s → 参数」里检查", rerr, app.Name)
			return *out
		}
		out.Status = StatusRolledBack
		return *out
	}
	if err := c.UpdateAppConfig(ctx, app.ID, next, ""); err != nil {
		return restore(fmt.Sprintf("修改失败：%v", err))
	}
	now, err := c.AppConfig(ctx, app.ID)
	switch {
	case err != nil:
		return restore(fmt.Sprintf("修改后读取设置失败：%v", err))
	case now.AllowPort != old.AllowPort || now.SpecifyIP != old.SpecifyIP:
		return restore("端口的开放方式被意外改变了")
	case int(now.MemoryLimit) != mb && !(mb == 0 && now.MemoryLimit == 0):
		return restore(fmt.Sprintf("修改后的上限是 %s，和预期不一致", limitText(now.MemoryLimit, now.MemoryUnit)))
	case !stillRunning(ctx, env, old.ContainerName):
		return restore("重建后容器没有正常运行（可能上限太紧，被反复杀掉）")
	}
	report("完成：%s 的内存上限已设为 %s，容器已重建", app.Name, limitText(float64(mb), "M"))
	out.Status = StatusDone
	return *out
}

func undoAppLimits(ctx context.Context, c *onepanel.Client, undo map[string]string) error {
	id, _ := strconv.ParseUint(undo["install_id"], 10, 64)
	cfg, err := c.AppConfig(ctx, uint(id))
	if err != nil {
		return err
	}
	cfg.MemoryLimit, _ = strconv.ParseFloat(undo["memory_limit"], 64)
	cfg.MemoryUnit = undo["memory_unit"]
	if cfg.MemoryUnit == "" {
		cfg.MemoryUnit = "M"
	}
	return c.UpdateAppConfig(ctx, uint(id), cfg, "")
}

// dockerRun runs a docker command over SSH; ok is false when it could not run.
func dockerRun(ctx context.Context, env *Env, args string) (string, bool) {
	if env.SSH == nil {
		return "", false
	}
	sudo := ""
	if env.User != "root" {
		sudo = "sudo -n "
	}
	res, err := env.SSH.Run(ctx, sudo+"docker "+args, "", 64<<10)
	if err != nil || res.ExitCode != 0 {
		return "", false
	}
	return res.Stdout, true
}

// stillRunning waits for a rebuilt container to come up and stay up for a
// few more seconds. Without SSH access to docker it cannot tell, and says yes.
func stillRunning(ctx context.Context, env *Env, container string) bool {
	name := shq(strings.Split(container, ",")[0])
	state := func() (string, bool) {
		out, ok := dockerRun(ctx, env, "inspect -f '{{.State.Status}}' "+name)
		return strings.TrimSpace(out), ok
	}
	if _, ok := dockerRun(ctx, env, "ps -q"); !ok {
		return true // no docker access over SSH: cannot tell
	}
	for i := 0; i < 30; i++ {
		if s, _ := state(); s == "running" {
			for j := 0; j < 5; j++ { // a JVM short of memory dies a few seconds after starting
				select {
				case <-ctx.Done():
					return false
				case <-time.After(pollEvery(env)):
				}
			}
			s, _ := state()
			return s == "running"
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(pollEvery(env)):
		}
	}
	return false
}

var (
	heapFlagRe = regexp.MustCompile(`^-Xmx|^-XX:(Max|Min|Initial)RAMPercentage=|^-XX:MaxRAM=`)
	cmdHeapRe  = regexp.MustCompile(`-Xmx[0-9]|-XX:MaxRAMPercentage=|-XX:MaxRAM=`)
)

// withMaxHeap replaces any heap size limit in JVM options with -Xmx<mb>m.
func withMaxHeap(opts string, mb int) string {
	var keep []string
	for _, f := range strings.Fields(opts) {
		if !heapFlagRe.MatchString(f) {
			keep = append(keep, f)
		}
	}
	return strings.Join(append(keep, fmt.Sprintf("-Xmx%dm", mb)), " ")
}

// applyJavaHeap fixes the maximum heap of a Java app installed by 1Panel
// through JAVA_TOOL_OPTIONS, which every JVM reads, in its docker-compose file.
func applyJavaHeap(ctx context.Context, env *Env, v map[string]string, out *Outcome, report func(string, ...any)) Outcome {
	c := env.OnePanel
	app, err := findApp(ctx, c, v["app"])
	if err != nil {
		out.Status = StatusRefused
		out.logf("%v", err)
		return *out
	}
	old, err := c.AppConfig(ctx, app.ID)
	if err != nil || strings.TrimSpace(old.DockerCompose) == "" {
		out.Status = StatusRefused
		out.logf("读取 %s 的 docker-compose 配置失败：%v", app.Name, err)
		return *out
	}
	// Options on the java command line win over JAVA_TOOL_OPTIONS.
	if procs, ok := dockerRun(ctx, env, "top "+shq(strings.Split(old.ContainerName, ",")[0])+" -o pid,args"); ok {
		var java []string
		for _, line := range strings.Split(procs, "\n") {
			if f := strings.Fields(line); len(f) > 1 && (f[1] == "java" || strings.HasSuffix(f[1], "/java")) {
				java = append(java, line)
			}
		}
		if len(java) == 0 {
			out.Status = StatusRefused
			out.logf("%s 的容器里没有正在运行的 Java 程序，不需要设置 Java 堆", app.Name)
			return *out
		}
		for _, line := range java {
			if m := cmdHeapRe.FindString(line); m != "" {
				out.Status = StatusRefused
				out.logf("%s 的启动命令里已经指定了堆大小（%s…），它比环境变量优先，需要在 1Panel 的应用参数里修改", app.Name, m)
				return *out
			}
		}
	}
	mb, _ := strconv.Atoi(v["max_heap_mb"])
	var before string
	compose, err := setComposeEnv(old.DockerCompose, app.ServiceName, "JAVA_TOOL_OPTIONS", func(cur string) string {
		before = cur
		return withMaxHeap(cur, mb)
	})
	if err != nil {
		out.Status = StatusRefused
		out.logf("%v", err)
		return *out
	}
	out.Undo["install_id"] = strconv.FormatUint(uint64(app.ID), 10)
	out.Undo["app"] = app.Name
	out.Undo["compose"] = old.DockerCompose
	if before == "" {
		before = "没有设置"
	}
	report("正在给 %s 固定 Java 最大堆为 %dMB（环境变量 JAVA_TOOL_OPTIONS，原来：%s），1Panel 会重建容器", app.Name, mb, before)
	restore := func(why string) Outcome {
		report("%s，正在恢复原来的配置", why)
		if rerr := c.UpdateAppConfig(ctx, app.ID, old, old.DockerCompose); rerr != nil {
			out.Status = StatusFailed
			out.logf("恢复也失败了：%v，请在 1Panel「应用商店 → 已安装 → %s → 参数」里检查", rerr, app.Name)
			return *out
		}
		out.Status = StatusRolledBack
		return *out
	}
	if err := c.UpdateAppConfig(ctx, app.ID, old, compose); err != nil {
		return restore(fmt.Sprintf("修改失败：%v", err))
	}
	now, err := c.AppConfig(ctx, app.ID)
	switch {
	case err != nil:
		return restore(fmt.Sprintf("修改后读取设置失败：%v", err))
	case now.AllowPort != old.AllowPort || now.SpecifyIP != old.SpecifyIP:
		return restore("端口的开放方式被意外改变了")
	case !strings.Contains(now.DockerCompose, fmt.Sprintf("-Xmx%dm", mb)):
		return restore("修改后的配置里没有新的堆设置")
	case !stillRunning(ctx, env, old.ContainerName):
		return restore("重建后容器没有正常运行（可能堆设得太小）")
	}
	report("完成：%s 的 Java 最大堆已固定为 %dMB，容器已重建并正常运行", app.Name, mb)
	out.Status = StatusDone
	return *out
}

// backupTimeout bounds how long to wait for 1Panel to finish one backup.
var backupTimeout = 30 * time.Minute

func newTaskID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// applyBackup has 1Panel back up an app, or the databases of a MySQL app,
// and waits until the backup files are written.
func applyBackup(ctx context.Context, env *Env, v map[string]string, out *Outcome, report func(string, ...any)) Outcome {
	c := env.OnePanel
	app, err := findApp(ctx, c, v["app"])
	if err != nil {
		out.Status = StatusRefused
		out.logf("%v", err)
		return *out
	}
	type job struct{ kind, name, detail, label string }
	var jobs []job
	switch app.AppKey {
	case "mysql", "mariadb", "mysql-cluster":
		dbs := []string{v["database"]}
		if v["database"] == "" {
			if dbs, err = c.Databases(ctx, app.Name); err != nil {
				out.Status = StatusRefused
				out.logf("读取 %s 里的数据库列表失败：%v", app.Name, err)
				return *out
			}
		}
		for _, db := range dbs {
			jobs = append(jobs, job{app.AppKey, app.Name, db, "数据库 " + db})
		}
		if len(jobs) == 0 {
			out.Status = StatusRefused
			out.logf("%s 里没有 1Panel 管理的数据库，不需要备份", app.Name)
			return *out
		}
	default:
		if v["database"] != "" {
			out.Status = StatusRefused
			out.logf("%s 不是数据库应用，不能指定 database", app.Name)
			return *out
		}
		jobs = append(jobs, job{"app", app.AppKey, app.Name, "应用 " + app.Name})
	}
	for _, j := range jobs {
		task := newTaskID()
		report("正在备份%s……", j.label)
		if err := c.Backup(ctx, j.kind, j.name, j.detail, task); err != nil {
			out.Status = StatusFailed
			out.logf("备份%s失败：%v", j.label, err)
			return *out
		}
		deadline := time.Now().Add(backupTimeout)
		for {
			rec, found, err := c.FindBackup(ctx, j.kind, j.name, j.detail, task)
			if err == nil && found && rec.Status == "Success" {
				report("已备份%s：%s/%s", j.label, rec.FileDir, rec.FileName)
				break
			}
			if err == nil && found && rec.Status == "Failed" {
				out.Status = StatusFailed
				out.logf("备份%s失败：%s", j.label, rec.Message)
				return *out
			}
			if time.Now().After(deadline) {
				out.Status = StatusFailed
				out.logf("等了 %d 分钟备份还没完成，请在 1Panel「备份」里查看", int(backupTimeout.Minutes()))
				return *out
			}
			select {
			case <-ctx.Done():
				out.Status = StatusFailed
				out.logf("等待备份时超时了")
				return *out
			case <-time.After(pollEvery(env)):
			}
		}
	}
	report("完成：备份文件在 1Panel「备份」页面可以查看、下载和恢复")
	out.Status = StatusDone
	return *out
}

func pollEvery(env *Env) time.Duration {
	if env.PollInterval > 0 {
		return env.PollInterval
	}
	return 2 * time.Second
}

// UseTestScripts makes action scripts come from load and keeps everything
// they leave on the server under dir. It is for tests of packages that run
// actions; call the returned function to undo it.
func UseTestScripts(dir string, load func(string) (string, error)) (restore func()) {
	oldLoad, oldRemote, oldBackup, oldJournal := loadScript, remoteDir, backupRoot, journalDir
	loadScript, remoteDir, backupRoot, journalDir = load, dir+"/tmp", dir+"/backups", dir+"/log"
	return func() { loadScript, remoteDir, backupRoot, journalDir = oldLoad, oldRemote, oldBackup, oldJournal }
}
