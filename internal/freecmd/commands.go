package freecmd

import (
	"regexp"
	"strings"
)

// split separates flags from operands. Flags listed in withValue take the
// next argument as their value.
func split(args []string, withValue ...string) (flags, ops []string) {
	takes := map[string]bool{}
	for _, f := range withValue {
		takes[f] = true
	}
	for i := 0; i < len(args); i++ {
		x := args[i]
		switch {
		case x == "--":
			return flags, append(ops, args[i+1:]...)
		case len(x) > 1 && strings.HasPrefix(x, "-"):
			flags = append(flags, x)
			if takes[x] && i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
		default:
			ops = append(ops, x)
		}
	}
	return flags, ops
}

// hasFlag reports whether any flag is one of names, including combined
// short flags such as -rf.
func hasFlag(flags []string, short string, long ...string) bool {
	for _, f := range flags {
		if strings.HasPrefix(f, "--") {
			for _, l := range long {
				if f == l || strings.HasPrefix(f, l+"=") {
					return true
				}
			}
			continue
		}
		if strings.HasPrefix(f, "-") && strings.ContainsAny(f[1:], short) {
			return true
		}
	}
	return false
}

type check func(a *analyzer, args []string)

func readOnly(*analyzer, []string) {}

var commands = map[string]check{
	"cat": readOnly, "head": readOnly, "tail": readOnly, "grep": readOnly, "egrep": readOnly, "fgrep": readOnly,
	"ls": readOnly, "stat": readOnly, "test": readOnly, "[": readOnly, "echo": readOnly, "printf": readOnly,
	"true": readOnly, "false": readOnly, "wc": readOnly, "cut": readOnly, "tr": readOnly, "diff": readOnly, "cmp": readOnly,
	"sha256sum": readOnly, "md5sum": readOnly, "basename": readOnly, "dirname": readOnly, "readlink": readOnly, "realpath": readOnly,
	"id": readOnly, "whoami": readOnly, "hostname": readOnly, "uname": readOnly, "date": readOnly, "df": readOnly, "du": readOnly,
	"free": readOnly, "uptime": readOnly, "ps": readOnly, "pgrep": readOnly, "ss": readOnly, "netstat": readOnly, "which": readOnly,
	"nproc": readOnly, "file": readOnly, "getent": readOnly, "sleep": readOnly,
	"set": func(a *analyzer, args []string) {
		for _, x := range args {
			if x != "-e" && x != "-u" && x != "-eu" && x != "-ue" && x != "-x" {
				a.problem("set 只能用 -e、-u、-x")
			}
		}
	},
	"command": func(a *analyzer, args []string) {
		if len(args) == 0 || args[0] != "-v" {
			a.problem("command 只能用来查询命令是否存在（command -v）")
		}
	},
	"sort": func(a *analyzer, args []string) {
		if flags, _ := split(args); hasFlag(flags, "o", "--output") {
			a.problem("sort 不能用 -o 写文件，请用重定向")
		}
	},
	"uniq": func(a *analyzer, args []string) {
		if _, ops := split(args); len(ops) > 1 {
			a.problem("uniq 不能直接写文件，请用重定向")
		}
	},
	"find": func(a *analyzer, args []string) {
		for _, x := range args {
			switch x {
			case "-exec", "-execdir", "-ok", "-okdir", "-delete", "-fprint", "-fprint0", "-fprintf", "-fls":
				a.problem("find 只能用来查找，不能用 %s", x)
			}
		}
	},
	"sed":   sed,
	"tee":   tee,
	"cp":    cp,
	"mv":    mv,
	"rm":    rm,
	"mkdir": mkdir,
	"touch": touch,
	"chmod": chmod,
	"chown": func(a *analyzer, args []string) { owner(a, "chown", args) },
	"chgrp": func(a *analyzer, args []string) { owner(a, "chgrp", args) },
	"ln":    ln,

	"systemctl":  systemctl,
	"service":    service,
	"nginx":      nginx,
	"openresty":  nginx,
	"apachectl":  apachectl,
	"apache2ctl": apachectl,
	"docker":     docker,
}

// denied explains commands that are never allowed.
func denied(name string) (string, bool) {
	groups := []struct {
		why   string
		names []string
	}{
		{"磁盘、分区和挂载操作不允许", []string{"dd", "fdisk", "sfdisk", "gdisk", "parted", "wipefs", "mkswap", "swapoff", "swapon", "mount", "umount", "losetup", "shred", "truncate"}},
		{"不允许访问网络或下载东西", []string{"curl", "wget", "nc", "ncat", "netcat", "scp", "sftp", "rsync", "ftp", "ssh", "telnet", "git"}},
		{"安装或升级软件需要额外确认，这个版本还不支持，请让用户在面板里安装", []string{"apt", "apt-get", "aptitude", "yum", "dnf", "apk", "zypper", "pacman", "snap", "dpkg", "rpm", "pip", "pip3", "npm", "yarn", "gem", "composer"}},
		{"账号、密码和权限相关的操作不允许", []string{"useradd", "userdel", "usermod", "adduser", "deluser", "passwd", "chpasswd", "groupadd", "groupdel", "gpasswd", "visudo", "sudo", "su", "doas", "chattr", "setfacl", "setcap"}},
		{"防火墙请用 cloud.firewall.open / cloud.firewall.close", []string{"iptables", "ip6tables", "iptables-restore", "nft", "ufw", "firewall-cmd"}},
		{"重启或关闭服务器请用 cloud.server.reboot", []string{"reboot", "shutdown", "poweroff", "halt", "init", "telinit"}},
		{"不能直接结束进程，请重启对应的服务", []string{"kill", "pkill", "killall", "skill"}},
		{"不能执行嵌套的脚本、解释器或改变命令的执行方式", []string{"sh", "bash", "dash", "zsh", "ksh", "busybox", "python", "python2", "python3", "perl", "ruby", "node", "php", "lua", "awk", "gawk", "mawk", "eval", "exec", "source", ".", "xargs", "env", "nohup", "setsid", "timeout", "nice", "ionice", "chroot", "unshare", "nsenter", "script", "watch", "trap", "alias"}},
		{"不能添加定时任务", []string{"crontab", "at", "batch", "systemd-run"}},
		{"不能修改内核参数或加载模块", []string{"sysctl", "modprobe", "insmod", "rmmod"}},
	}
	for _, g := range groups {
		for _, n := range g.names {
			if name == n {
				return g.why, true
			}
		}
	}
	if strings.HasPrefix(name, "mkfs") || strings.HasPrefix(name, "python") {
		return "不允许", true
	}
	return "", false
}

func touch(a *analyzer, args []string) {
	_, ops := split(args)
	if len(ops) == 0 {
		a.problem("touch 缺少文件")
	}
	for _, p := range ops {
		a.write(p)
	}
}

func tee(a *analyzer, args []string) {
	flags, ops := split(args)
	for _, f := range flags {
		if f != "-a" && f != "--append" {
			a.problem("tee 只能用 -a")
		}
	}
	for _, p := range ops {
		a.write(p)
	}
}

func cp(a *analyzer, args []string) {
	flags, ops := split(args)
	if hasFlag(flags, "rRa", "--recursive", "--archive") {
		a.problem("cp 不能复制整个目录（-r、-a），请逐个文件复制")
		return
	}
	if len(ops) != 2 {
		a.problem("cp 要写成 cp 源文件 目标文件")
		return
	}
	a.write(ops[1])
}

func mv(a *analyzer, args []string) {
	_, ops := split(args)
	if len(ops) != 2 {
		a.problem("mv 要写成 mv 源文件 目标文件")
		return
	}
	a.write(ops[0])
	a.write(ops[1])
}

func rm(a *analyzer, args []string) {
	flags, ops := split(args)
	if hasFlag(flags, "rRd", "--recursive", "--dir") {
		a.problem("rm 不能删除目录（-r），只能删除写明的文件")
		return
	}
	for _, p := range ops {
		a.write(p)
	}
}

func mkdir(a *analyzer, args []string) {
	_, ops := split(args, "-m", "--mode")
	for _, p := range ops {
		if err := CheckPath(p); err != nil {
			a.problem("不能创建目录 %s：%v", p, err)
			continue
		}
		a.dirs[p] = true
	}
}

var worldWritable = regexp.MustCompile(`(^[0-7]*[2367]$)|([oa][+=][rwxXst]*w)|(^\+?[rwxXst]*w)`)

func chmod(a *analyzer, args []string) {
	flags, ops := split(args)
	if hasFlag(flags, "R", "--recursive") {
		a.problem("chmod 不能用 -R")
		return
	}
	if len(ops) < 2 {
		a.problem("chmod 要写成 chmod 权限 文件")
		return
	}
	if worldWritable.MatchString(ops[0]) {
		a.problem("不能把文件设成所有人可写（%s）", ops[0])
	}
	for _, p := range ops[1:] {
		a.write(p)
	}
}

func owner(a *analyzer, name string, args []string) {
	flags, ops := split(args)
	if hasFlag(flags, "R", "--recursive") {
		a.problem("%s 不能用 -R", name)
		return
	}
	if len(ops) < 2 {
		a.problem("%s 要写成 %s 用户 文件", name, name)
		return
	}
	for _, p := range ops[1:] {
		a.write(p)
	}
}

func ln(a *analyzer, args []string) {
	flags, ops := split(args)
	if !hasFlag(flags, "s", "--symbolic") || len(ops) != 2 {
		a.problem("ln 只能用来建软链接：ln -s 目标 链接")
		return
	}
	a.write(ops[1])
}

func systemctl(a *analyzer, args []string) {
	_, ops := split(args)
	if len(ops) == 0 {
		return
	}
	switch ops[0] {
	case "status", "is-active", "is-enabled", "is-failed", "cat", "show", "list-units", "list-unit-files":
	case "daemon-reload":
		a.problem("不能执行 systemctl daemon-reload（服务定义文件不允许修改）")
	case "reload", "restart", "try-restart", "reload-or-restart", "try-reload-or-restart", "start":
		if len(ops) < 2 {
			a.problem("systemctl %s 要写明服务名", ops[0])
		}
		for _, u := range ops[1:] {
			a.service(ops[0], u)
		}
	default:
		a.problem("systemctl %s 不允许（停止、禁用、开机启动等需要正式模板）", ops[0])
	}
}

func service(a *analyzer, args []string) {
	if len(args) < 2 {
		a.problem("service 要写成 service 服务名 reload")
		return
	}
	switch args[1] {
	case "status":
	case "reload", "restart", "force-reload", "start":
		a.service(args[1], args[0])
	default:
		a.problem("service %s 不允许", args[1])
	}
}

func nginx(a *analyzer, args []string) {
	for i, x := range args {
		switch x {
		case "-t", "-T", "-v", "-V", "-q":
		case "-c", "-p", "-g":
			if i+1 >= len(args) {
				a.problem("nginx %s 缺少参数", x)
			}
		case "-s":
			if i+1 < len(args) && args[i+1] == "reload" {
				a.service("reload", "nginx")
			} else {
				a.problem("nginx -s 只能用 reload")
			}
		}
	}
}

func apachectl(a *analyzer, args []string) {
	if len(args) == 0 {
		a.problem("apachectl 要写明操作")
		return
	}
	switch args[0] {
	case "configtest", "-t", "-v", "-V", "-S":
	case "graceful":
		a.service("reload", "apache")
	default:
		a.problem("apachectl %s 不允许，只能用 configtest 和 graceful", args[0])
	}
}

func phpFPM(a *analyzer, args []string) {
	for _, x := range args {
		if x != "-t" && x != "-v" && x != "-i" && x != "-m" {
			a.problem("php-fpm 只能用 -t 检查配置")
		}
	}
}

func docker(a *analyzer, args []string) {
	if len(args) == 0 {
		return
	}
	switch args[0] {
	case "ps", "inspect", "logs", "stats", "images", "version", "info":
	case "restart":
		_, ops := split(args[1:])
		if len(ops) != 1 {
			a.problem("docker restart 一次只能重启一个容器")
			return
		}
		a.service("restart", "docker:"+ops[0])
	default:
		a.problem("docker %s 不允许（只能查看和重启容器）", args[0])
	}
}

// ---- sed ----

func sed(a *analyzer, args []string) {
	var exprs, files []string
	inPlace := false
	for i := 0; i < len(args); i++ {
		x := args[i]
		switch {
		case x == "-i" || x == "--in-place":
			inPlace = true
		case strings.HasPrefix(x, "-i"), strings.HasPrefix(x, "--in-place="):
			a.problem("sed -i 不要带备份后缀，系统会自动备份")
			inPlace = true
		case x == "-e" || x == "--expression":
			if i+1 < len(args) {
				i++
				exprs = append(exprs, args[i])
			}
		case strings.HasPrefix(x, "--expression="):
			exprs = append(exprs, strings.TrimPrefix(x, "--expression="))
		case x == "-f" || x == "--file" || strings.HasPrefix(x, "--file="):
			a.problem("sed 不能从文件读取脚本（-f）")
		case x == "-E" || x == "-r" || x == "-n" || x == "-z" || x == "-s" || x == "--regexp-extended" || x == "--quiet" || x == "--silent" || x == "--posix":
		case strings.HasPrefix(x, "-") && len(x) > 1:
			a.problem("sed 不支持参数 %s", x)
		default:
			files = append(files, x)
		}
	}
	if len(exprs) == 0 {
		if len(files) == 0 {
			a.problem("sed 缺少表达式")
			return
		}
		exprs, files = files[:1], files[1:]
	}
	for _, e := range exprs {
		if err := checkSed(e); err != "" {
			a.problem("sed 表达式 %q：%s", e, err)
		}
	}
	if inPlace {
		if len(files) == 0 {
			a.problem("sed -i 要写明文件")
		}
		for _, f := range files {
			a.write(f)
		}
	}
}

// checkSed accepts sed scripts made of substitutions (s///), deletions
// (d), prints (p) and one-line a/i/c text commands, with optional line or
// regex addresses. Commands that run programs (e) or write files (w) are
// refused.
func checkSed(s string) string {
	i := 0
	n := len(s)
	skipSpace := func() {
		for i < n && (s[i] == ' ' || s[i] == '\t') {
			i++
		}
	}
	readDelimited := func(delim byte) bool {
		for i < n {
			switch s[i] {
			case '\\':
				i += 2
				continue
			case delim:
				i++
				return true
			case '\n':
				return false
			}
			i++
		}
		return false
	}
	address := func() bool {
		switch {
		case i < n && s[i] == '$':
			i++
		case i < n && s[i] >= '0' && s[i] <= '9':
			for i < n && s[i] >= '0' && s[i] <= '9' {
				i++
			}
		case i < n && s[i] == '/':
			i++
			return readDelimited('/')
		case i+1 < n && s[i] == '\\':
			d := s[i+1]
			i += 2
			return readDelimited(d)
		}
		return true
	}
	for i < n {
		skipSpace()
		if i < n && (s[i] == ';' || s[i] == '\n') {
			i++
			continue
		}
		if i >= n {
			break
		}
		start := i
		if !address() {
			return "地址没有结束"
		}
		if i > start && i < n && s[i] == ',' {
			i++
			if !address() {
				return "地址没有结束"
			}
		}
		skipSpace()
		if i < n && s[i] == '!' {
			i++
			skipSpace()
		}
		if i >= n {
			return "缺少命令"
		}
		c := s[i]
		i++
		switch c {
		case 's':
			if i >= n || s[i] == '\\' || s[i] == '\n' {
				return "替换命令的格式不对"
			}
			d := s[i]
			i++
			if !readDelimited(d) || !readDelimited(d) {
				return "替换命令没有结束"
			}
			for i < n && strings.IndexByte("gpiIm0123456789", s[i]) >= 0 {
				i++
			}
			if i < n && strings.IndexByte("ewW", s[i]) >= 0 {
				return "替换命令不能带 e 或 w 标志（会执行命令或写文件）"
			}
		case 'd', 'p', 'D', 'P', 'N', 'n', '=':
		case 'a', 'i', 'c':
			// text runs to the end of the line
			for i < n && s[i] != '\n' {
				if s[i] == '\\' {
					i++
				}
				i++
			}
		default:
			return "只支持 s（替换）、d（删除）、p、a/i/c（插入文本）这些 sed 命令"
		}
		skipSpace()
		if i < n && s[i] != ';' && s[i] != '\n' {
			return "命令后面有多余的内容"
		}
	}
	return ""
}
