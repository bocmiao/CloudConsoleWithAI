// Package freecmd checks the shell commands the AI writes when no
// template fits (docs/DESIGN.md 7.5). Safety cannot rely on the user
// reading commands, so the script is parsed with a real shell parser and
// held to a small language: only listed programs, no variables, command
// substitution or globs, every file it writes named literally, under an
// allowed directory, and declared up front together with the services it
// reloads. A script is accepted only when what it does matches what the
// AI declared.
package freecmd

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// Limits on one free command.
const (
	MaxScript   = 4000
	MaxFiles    = 10
	MaxServices = 5
)

// Declaration is what the AI says a free command will do.
type Declaration struct {
	Script   string
	Files    []string // absolute paths the script writes, creates or deletes
	Services []string // services it reloads or restarts: nginx, php8.2-fpm, docker:halo
}

// Analysis is what parsing the script found.
type Analysis struct {
	Writes     []string // files written, created or deleted
	Dirs       []string // directories created
	ServiceOps []string // e.g. "reload nginx", "restart docker:halo"
	Services   []string // services acted on
	Programs   []string // programs it runs
	Problems   []string // why it cannot run; empty when accepted
}

// OK reports whether the script may run.
func (a Analysis) OK() bool { return len(a.Problems) == 0 }

type analyzer struct {
	Analysis
	writes, dirs, services, programs, problems map[string]bool
}

func (a *analyzer) problem(format string, args ...any) {
	a.problems[fmt.Sprintf(format, args...)] = true
}

// Analyze parses and checks a declaration.
func Analyze(d Declaration) Analysis {
	a := &analyzer{writes: map[string]bool{}, dirs: map[string]bool{}, services: map[string]bool{}, programs: map[string]bool{}, problems: map[string]bool{}}
	switch {
	case strings.TrimSpace(d.Script) == "":
		a.problem("没有命令")
	case len(d.Script) > MaxScript:
		a.problem("命令太长（最多 %d 字节），请拆成更小的步骤或改用正式模板", MaxScript)
	case len(d.Files) > MaxFiles:
		a.problem("一次最多修改 %d 个文件", MaxFiles)
	case len(d.Services) > MaxServices:
		a.problem("一次最多重载 %d 个服务", MaxServices)
	}
	for _, f := range d.Files {
		if err := CheckPath(f); err != nil {
			a.problem("声明的文件 %s：%v", f, err)
		}
	}
	for _, s := range d.Services {
		if err := checkService(s); err != nil {
			a.problem("%v", err)
		}
	}
	if len(a.problems) == 0 {
		f, err := syntax.NewParser(syntax.Variant(syntax.LangPOSIX)).Parse(strings.NewReader(d.Script), "")
		if err != nil {
			a.problem("命令有语法错误：%v", err)
		} else {
			a.expansions(f)
			a.stmts(f.Stmts)
			a.compare(d)
		}
	}
	a.Writes, a.Dirs, a.Services, a.Programs = keys(a.writes), keys(a.dirs), keys(a.services), keys(a.programs)
	a.Problems = keys(a.problems)
	return a.Analysis
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// expansions refuses everything that is only known when the script runs.
func (a *analyzer) expansions(f *syntax.File) {
	syntax.Walk(f, func(n syntax.Node) bool {
		switch n.(type) {
		case *syntax.ParamExp:
			a.problem("不能使用变量（$...）：要写入的内容和路径必须事先确定；写配置文件时 here-doc 的结束符要加引号，例如 <<'EOF'")
		case *syntax.CmdSubst:
			a.problem("不能使用命令替换（$(...) 或反引号）")
		case *syntax.ProcSubst:
			a.problem("不能使用进程替换")
		case *syntax.ArithmExp:
			a.problem("不能使用算术展开 $((...))")
		case *syntax.ExtGlob, *syntax.BraceExp:
			a.problem("不能使用通配或花括号展开")
		}
		return true
	})
}

func (a *analyzer) stmts(list []*syntax.Stmt) {
	for _, s := range list {
		a.stmt(s)
	}
}

func (a *analyzer) stmt(s *syntax.Stmt) {
	if s.Background || s.Coprocess {
		a.problem("不能在后台运行命令（&）")
	}
	for _, r := range s.Redirs {
		a.redirect(r)
	}
	if s.Cmd != nil {
		a.cmd(s.Cmd)
	}
}

func (a *analyzer) cmd(c syntax.Command) {
	switch x := c.(type) {
	case *syntax.CallExpr:
		if len(x.Assigns) > 0 {
			a.problem("不能设置变量或环境变量")
		}
		if len(x.Args) > 0 {
			a.call(x.Args)
		}
	case *syntax.BinaryCmd:
		a.stmt(x.X)
		a.stmt(x.Y)
	case *syntax.IfClause:
		a.stmts(x.Cond)
		a.stmts(x.Then)
		if x.Else != nil {
			a.cmd(x.Else)
		}
	case *syntax.Block:
		a.stmts(x.Stmts)
	case *syntax.Subshell:
		a.stmts(x.Stmts)
	case *syntax.FuncDecl:
		a.problem("不能定义函数")
	default:
		a.problem("不支持循环、case 等复杂语法，请把每一步直接写出来")
	}
}

// lit returns a word's value when it is fully known before running.
func lit(w *syntax.Word) (string, bool) {
	var b strings.Builder
	for _, p := range w.Parts {
		switch x := p.(type) {
		case *syntax.Lit:
			b.WriteString(x.Value)
		case *syntax.SglQuoted:
			b.WriteString(x.Value)
		case *syntax.DblQuoted:
			for _, q := range x.Parts {
				l, ok := q.(*syntax.Lit)
				if !ok {
					return "", false
				}
				b.WriteString(l.Value)
			}
		default:
			return "", false
		}
	}
	return b.String(), true
}

// hasGlob reports an unquoted glob character in a word.
func hasGlob(w *syntax.Word) bool {
	for _, p := range w.Parts {
		if l, ok := p.(*syntax.Lit); ok && strings.ContainsAny(l.Value, "*?[") {
			return true
		}
	}
	return false
}

func (a *analyzer) redirect(r *syntax.Redirect) {
	switch r.Op {
	case syntax.RdrIn, syntax.Hdoc, syntax.DashHdoc, syntax.WordHdoc:
		return
	case syntax.DplOut, syntax.DplIn:
		return // 2>&1 and the like
	}
	target, ok := lit(r.Word)
	if !ok || hasGlob(r.Word) {
		a.problem("重定向的目标必须是写明的文件路径")
		return
	}
	if target == "/dev/null" {
		return
	}
	a.write(target)
}

func (a *analyzer) write(p string) {
	if err := CheckPath(p); err != nil {
		a.problem("不能写 %s：%v", p, err)
		return
	}
	a.writes[p] = true
}

// call checks one command and records what it does.
func (a *analyzer) call(words []*syntax.Word) {
	args := make([]string, len(words))
	for i, w := range words {
		v, ok := lit(w)
		if !ok {
			return // reported by expansions
		}
		if i > 0 && hasGlob(w) && !globOK(args[0]) {
			a.problem("参数里不能有通配符（* ? [），要写明具体的文件")
			return
		}
		args[i] = v
	}
	name := args[0]
	if strings.Contains(name, "/") {
		a.problem("请直接写命令名，不要带路径：%s", name)
		return
	}
	a.programs[name] = true
	if why, bad := denied(name); bad {
		a.problem("不允许使用 %s：%s", name, why)
		return
	}
	check, ok := commands[name]
	if !ok {
		if strings.HasPrefix(name, "php-fpm") || strings.HasPrefix(name, "php") && strings.HasSuffix(name, "-fpm") {
			check = phpFPM
		} else {
			a.problem("%s 不在自由命令允许的范围内（允许的有：查看类命令、sed -i、tee、cp、mv、rm、mkdir、touch、chmod、chown、ln、systemctl/service 重载或重启、nginx -t / -s reload）", name)
			return
		}
	}
	check(a, args[1:])
}

// globOK lists commands whose arguments are patterns, not file names.
func globOK(name string) bool {
	return name == "grep" || name == "egrep" || name == "fgrep" || name == "sed" || name == "find"
}

// compare holds the script to its declaration.
func (a *analyzer) compare(d Declaration) {
	declared := map[string]bool{}
	for _, f := range d.Files {
		declared[f] = true
	}
	for w := range a.writes {
		if !declared[w] {
			a.problem("命令会修改 %s，但声明里没有这个文件", w)
		}
	}
	for f := range declared {
		if !a.writes[f] {
			a.problem("声明要修改 %s，但命令里没有改它", f)
		}
	}
	for dir := range a.dirs {
		ok := declared[dir]
		for f := range declared {
			ok = ok || strings.HasPrefix(f, dir+"/")
		}
		if !ok {
			a.problem("命令会创建目录 %s，但声明的文件都不在里面", dir)
		}
	}
	svc := map[string]bool{}
	for _, s := range d.Services {
		svc[unit(s)] = true
	}
	for s := range a.services {
		if !svc[s] {
			a.problem("命令会重载或重启 %s，但声明里没有这个服务", s)
		}
	}
	for s := range svc {
		if !a.services[s] {
			a.problem("声明要重载或重启 %s，但命令里没有", s)
		}
	}
}

// unit normalises a service name: nginx.service → nginx.
func unit(s string) string { return strings.TrimSuffix(strings.TrimSpace(s), ".service") }

func (a *analyzer) service(op, name string) {
	name = unit(name)
	if err := checkService(name); err != nil {
		a.problem("%v", err)
		return
	}
	a.services[name] = true
	a.ServiceOps = append(a.ServiceOps, op+" "+name)
}

var critical = []string{"ssh", "sshd", "tat_agent", "1panel", "1panel-core", "1panel-agent", "bt", "docker", "containerd", "dbus",
	"networking", "network", "NetworkManager", "firewalld", "ufw", "nftables", "iptables", "polkit", "cron", "crond", "rsyslog", "systemd-journald"}

// CheckService checks a service name a free command may reload or restart.
func CheckService(name string) error { return checkService(name) }

func checkService(name string) error {
	name = unit(name)
	if name == "" || strings.ContainsAny(name, " /*?[$`'\"\\;&|<>") {
		return fmt.Errorf("服务名 %q 不对", name)
	}
	if strings.HasPrefix(name, "docker:") {
		return nil // containers; the panel's apps are named 1Panel-...
	}
	base := strings.ToLower(name)
	if strings.HasPrefix(base, "systemd-") || strings.HasPrefix(base, "1panel") {
		return fmt.Errorf("%s 是关键服务，自由命令不能重启它", name)
	}
	for _, c := range critical {
		if strings.EqualFold(base, c) {
			return fmt.Errorf("%s 是关键服务，自由命令不能重启它", name)
		}
	}
	return nil
}

// Write locations. Configuration and site files may change; accounts,
// logins, boot, scheduling, networking, the firewall, panel-managed files
// and Miao Panel's own records may not.
var (
	writeRoots = []string{"/etc/", "/usr/local/etc/", "/opt/", "/srv/", "/var/www/", "/www/wwwroot/", "/home/", "/root/", "/data/"}
	protected  = []string{
		"/etc/ssh/", "/etc/passwd", "/etc/shadow", "/etc/group", "/etc/gshadow", "/etc/sudoers", "/etc/pam.d/", "/etc/security/",
		"/etc/fstab", "/etc/crontab", "/etc/cron.", "/etc/anacrontab", "/etc/systemd/", "/etc/init.d/", "/etc/init/", "/etc/rc.local", "/etc/rc",
		"/etc/profile", "/etc/bash.bashrc", "/etc/environment", "/etc/ld.so.", "/etc/resolv.conf", "/etc/netplan/", "/etc/network/",
		"/etc/sysconfig/network", "/etc/NetworkManager/", "/etc/iptables/", "/etc/ufw/", "/etc/firewalld/", "/etc/nftables", "/etc/apt/",
		"/etc/yum", "/etc/dnf/", "/etc/modprobe.d/", "/etc/modules", "/etc/default/grub", "/etc/grub", "/etc/selinux/", "/etc/logrotate.",
		"/etc/sysctl.conf", "/etc/hostname", "/etc/machine-id", "/etc/login.defs", "/etc/shells", "/etc/sudo",
		"/opt/1panel", "/www/server/", "/usr/local/qcloud/", "/var/log/miaopanel", "/var/backups/miaopanel",
	}
	protectedNames = []string{".ssh", "authorized_keys", ".bashrc", ".bash_profile", ".profile", ".zshrc", ".bash_login", ".config/systemd"}
)

// AllowForTests lets free commands write under dir, for tests that run
// real scripts in a temporary directory.
func AllowForTests(dir string) (restore func()) {
	old := writeRoots
	writeRoots = append(append([]string(nil), old...), strings.TrimSuffix(dir, "/")+"/")
	return func() { writeRoots = old }
}

// CheckPath checks a file path a free command may write.
func CheckPath(p string) error {
	switch {
	case !strings.HasPrefix(p, "/"):
		return fmt.Errorf("要写完整的绝对路径")
	case path.Clean(p) != p:
		return fmt.Errorf("路径不规范（不能有 ..、// 或结尾的 /）")
	case strings.ContainsAny(p, "*?[]{}$`'\"\\;&|<>!~\n\r\t"):
		return fmt.Errorf("路径里有特殊字符")
	}
	for _, x := range protected {
		if strings.HasPrefix(p, x) {
			return fmt.Errorf("这个位置涉及账号、登录、开机、定时任务、网络、防火墙、面板管理的文件或 Miao Panel 自己的记录，自由命令不能修改")
		}
	}
	for _, seg := range protectedNames {
		if strings.Contains(p+"/", "/"+seg+"/") {
			return fmt.Errorf("登录和 shell 启动相关的文件不能修改")
		}
	}
	for _, r := range writeRoots {
		if strings.HasPrefix(p, r) && len(p) > len(r) {
			return nil
		}
	}
	return fmt.Errorf("只能修改 %s 下面的文件", strings.Join(writeRoots, "、"))
}
