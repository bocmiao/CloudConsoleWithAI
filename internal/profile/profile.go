// Package profile turns discover.sh output into a structured server
// profile and a list of health findings. The raw output stays the source
// of truth; parsing is best-effort and tolerates missing sections.
package profile

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Profile is what Miao Panel knows about a server's software stack.
type Profile struct {
	OS       string `json:"os"`
	Kernel   string `json:"kernel"`
	CPUCores int    `json:"cpuCores"`
	Load     string `json:"load"`
	RunAs    string `json:"runAs"`

	Memory Memory `json:"memory"`
	Disks  []Disk `json:"disks"`

	Panel     Panel    `json:"panel"`
	Adapter   string   `json:"adapter"` // 1panel | bt | linux
	WebServer string   `json:"webServer"`
	Websites  []string `json:"websites"`
	WordPress []string `json:"wordpress"`
	Java      []string `json:"java"`
	Databases []string `json:"databases"`
	Docker    Docker   `json:"docker"`

	Findings []Finding `json:"findings"`

	sections map[string][]string
}

// Memory is in MiB, from `free -m`.
type Memory struct {
	TotalMB     int `json:"totalMB"`
	UsedMB      int `json:"usedMB"`
	AvailableMB int `json:"availableMB"`
	SwapTotalMB int `json:"swapTotalMB"`
	SwapUsedMB  int `json:"swapUsedMB"`
}

// Disk is one mounted filesystem from `df -hT`.
type Disk struct {
	Mount  string `json:"mount"`
	Size   string `json:"size"`
	Used   string `json:"used"`
	UsePct int    `json:"usePct"`
}

// Panel describes a detected server management panel.
type Panel struct {
	Kind     string   `json:"kind"` // 1panel | bt | none
	Version  string   `json:"version"`
	Port     string   `json:"port"`
	Apps     []string `json:"apps"`
	Websites []string `json:"websites"`
	Runtimes []string `json:"runtimes"`
}

// Docker summarizes the container runtime.
type Docker struct {
	Status     string   `json:"status"` // "", "no", "unreachable", or the server version
	Containers []string `json:"containers"`
}

// Finding is a health or security observation shown to the user.
type Finding struct {
	Level  string `json:"level"` // danger | warn | info
	Title  string `json:"title"`
	Detail string `json:"detail"`
}

// Parse builds a Profile from discover.sh output.
func Parse(raw string) *Profile {
	p := &Profile{sections: splitSections(raw)}
	p.parseSystem()
	p.parsePanel()
	p.parseWeb()
	p.parseApps()
	p.parseDB()
	p.parseDocker()
	p.findings()
	return p
}

// Section returns the raw lines of one section, or nil.
func (p *Profile) Section(name string) []string { return p.sections[name] }

var sectionRe = regexp.MustCompile(`^== ([a-z_]+) ==$`)

func splitSections(raw string) map[string][]string {
	out := map[string][]string{}
	cur := ""
	for _, line := range strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n") {
		if m := sectionRe.FindStringSubmatch(line); m != nil {
			cur = m[1]
			out[cur] = []string{}
			continue
		}
		if cur != "" {
			out[cur] = append(out[cur], line)
		}
	}
	return out
}

// value returns the text after "key: " on the first matching line.
func (p *Profile) value(section, key string) string {
	prefix := key + ":"
	for _, l := range p.sections[section] {
		if strings.HasPrefix(l, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(l, prefix))
		}
	}
	return ""
}

// block returns the lines after a "-- title" marker up to the next marker.
func (p *Profile) block(section, title string) []string {
	var out []string
	in := false
	for _, l := range p.sections[section] {
		if strings.HasPrefix(l, "-- ") {
			if in {
				break
			}
			in = strings.HasPrefix(l, "-- "+title)
			continue
		}
		if in && strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

func atoi(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

func (p *Profile) parseSystem() {
	p.OS = p.value("system", "os")
	p.Kernel = p.value("system", "kernel")
	p.CPUCores = atoi(p.value("system", "cpu_cores"))
	p.Load = p.value("system", "loadavg")
	p.RunAs = p.value("system", "run_as")
	for _, l := range p.sections["system"] {
		f := strings.Fields(l)
		switch {
		case len(f) >= 7 && f[0] == "Mem:":
			p.Memory.TotalMB, p.Memory.UsedMB, p.Memory.AvailableMB = atoi(f[1]), atoi(f[2]), atoi(f[6])
		case len(f) >= 3 && f[0] == "Swap:":
			p.Memory.SwapTotalMB, p.Memory.SwapUsedMB = atoi(f[1]), atoi(f[2])
		}
	}
	for _, l := range p.block("system", "disk") {
		f := strings.Fields(l)
		if len(f) < 7 || f[0] == "Filesystem" {
			continue
		}
		p.Disks = append(p.Disks, Disk{
			Mount: f[6], Size: f[2], Used: f[3],
			UsePct: atoi(strings.TrimSuffix(f[5], "%")),
		})
	}
}

func (p *Profile) parsePanel() {
	p.Panel.Kind = "none"
	p.Adapter = "linux"
	if strings.HasPrefix(p.value("panel", "1panel"), "yes") {
		p.Panel = Panel{
			Kind:     "1panel",
			Version:  p.value("panel", "1panel_ORIGINAL_VERSION"),
			Port:     p.value("panel", "1panel_ORIGINAL_PORT"),
			Apps:     strings.Fields(p.value("panel", "1panel_apps")),
			Websites: strings.Fields(p.value("panel", "1panel_websites")),
			Runtimes: strings.Fields(p.value("panel", "1panel_php_runtimes")),
		}
		p.Adapter = "1panel"
	} else if p.value("panel", "bt_panel") == "yes" {
		p.Panel = Panel{Kind: "bt", Port: p.value("panel", "bt_panel_port"),
			Apps: strings.Fields(p.value("panel", "bt_software"))}
		p.Adapter = "bt"
	}
}

var (
	serverNameRe = regexp.MustCompile(`server_name\s+([^;]+)`)
	// "nginx version: nginx/1.24.0 (Ubuntu) bin=..." -> "nginx/1.24.0"
	webVersionRe = regexp.MustCompile(`(?i)(nginx|openresty|apache|caddy)/v?[0-9][0-9.]*|v[0-9]+\.[0-9.]+`)
)

func (p *Profile) parseWeb() {
	for _, key := range []string{"nginx", "apache", "caddy"} {
		if v := p.value("web", key); v != "" {
			p.WebServer = key
			if m := webVersionRe.FindString(v); m != "" {
				p.WebServer = m
				if strings.HasPrefix(m, "v") {
					p.WebServer = key + " " + m
				}
			}
			break
		}
	}
	seen := map[string]bool{}
	for _, l := range p.sections["web"] {
		m := serverNameRe.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		for _, name := range strings.Fields(m[1]) {
			if name == "_" || name == "localhost" || seen[name] {
				continue
			}
			seen[name] = true
			p.Websites = append(p.Websites, name)
		}
	}
	if p.WebServer == "" && len(p.Websites) > 0 {
		p.WebServer = "nginx/openresty（容器内）"
	}
}

func (p *Profile) parseApps() {
	for _, l := range p.sections["apps"] {
		switch {
		case strings.HasPrefix(l, "wordpress: "):
			p.WordPress = append(p.WordPress, strings.TrimPrefix(l, "wordpress: "))
		case strings.HasPrefix(l, "java: "):
			p.Java = append(p.Java, strings.TrimPrefix(l, "java: "))
		}
	}
}

func (p *Profile) parseDB() {
	for _, l := range p.block("db", "db processes") {
		f := strings.Fields(l)
		if len(f) >= 3 {
			// "pid=1 rss_mb=820 /usr/sbin/mysqld ..." -> "mysqld 820MB"
			name := f[2]
			if i := strings.LastIndex(name, "/"); i >= 0 {
				name = name[i+1:]
			}
			p.Databases = append(p.Databases, fmt.Sprintf("%s %sMB", name, strings.TrimPrefix(f[1], "rss_mb=")))
		}
	}
}

func (p *Profile) parseDocker() {
	v := p.value("docker", "docker")
	switch {
	case v == "":
	case v == "no":
		p.Docker.Status = "no"
	case strings.Contains(v, "not reachable"):
		p.Docker.Status = "unreachable"
	default:
		p.Docker.Status = v
	}
	for _, l := range p.block("docker", "containers") {
		if name, _, ok := strings.Cut(l, "|"); ok {
			p.Docker.Containers = append(p.Docker.Containers, name)
		}
	}
}

func (p *Profile) add(level, title, detail string) {
	p.Findings = append(p.Findings, Finding{Level: level, Title: title, Detail: detail})
}

// findings applies simple rules; the AI explains and prioritizes them.
func (p *Profile) findings() {
	m := p.Memory
	if m.TotalMB > 0 {
		pct := m.AvailableMB * 100 / m.TotalMB
		switch {
		case pct < 5:
			p.add("danger", "可用内存严重不足", fmt.Sprintf("可用 %dMB / 总共 %dMB（%d%%）", m.AvailableMB, m.TotalMB, pct))
		case pct < 10:
			p.add("warn", "可用内存偏低", fmt.Sprintf("可用 %dMB / 总共 %dMB（%d%%）", m.AvailableMB, m.TotalMB, pct))
		}
		if m.SwapTotalMB == 0 && m.TotalMB < 4096 {
			p.add("info", "没有 swap", "小内存服务器没有 swap，内存突增时容易被系统杀进程")
		}
	}
	for _, d := range p.Disks {
		switch {
		case d.UsePct >= 90:
			p.add("danger", "磁盘快满了："+d.Mount, fmt.Sprintf("已用 %d%%（%s / %s）", d.UsePct, d.Used, d.Size))
		case d.UsePct >= 85:
			p.add("warn", "磁盘使用率偏高："+d.Mount, fmt.Sprintf("已用 %d%%（%s / %s）", d.UsePct, d.Used, d.Size))
		}
	}
	if oom := p.block("health", "oom"); len(oom) > 0 {
		p.add("danger", "近 7 天发生过内存不足（OOM）", fmt.Sprintf("系统因内存不足杀掉过进程 %d 次", len(oom)))
	}
	if failed := strings.TrimSpace(p.value("services", "failed")); failed != "" {
		p.add("warn", "有服务启动失败", failed)
	}
	if strings.EqualFold(p.value("security", "sshd_password_auth"), "yes") {
		p.add("warn", "SSH 允许密码登录", "容易被暴力破解，建议改用密钥登录")
	}
	if strings.EqualFold(p.value("security", "sshd_root_login"), "yes") {
		p.add("warn", "SSH 允许 root 直接登录", "建议改为只允许密钥登录（prohibit-password）")
	}
	if p.RunAs != "" && p.RunAs != "root" {
		p.add("info", "识别时不是 root 身份", "部分信息可能看不到；如果能用 sudo，结果会更完整")
	}
	if p.Docker.Status == "unreachable" {
		p.add("info", "Docker 已安装但连接不上", "Docker 服务可能没有运行")
	}
}

// Summary is a compact text description for the AI.
func (p *Profile) Summary() string {
	var b strings.Builder
	fmt.Fprintf(&b, "系统: %s, 内核 %s, %d 核, 负载 %s, 识别身份 %s\n", p.OS, p.Kernel, p.CPUCores, p.Load, p.RunAs)
	m := p.Memory
	fmt.Fprintf(&b, "内存: 总 %dMB, 已用 %dMB, 可用 %dMB; swap %dMB (已用 %dMB)\n",
		m.TotalMB, m.UsedMB, m.AvailableMB, m.SwapTotalMB, m.SwapUsedMB)
	for _, d := range p.Disks {
		fmt.Fprintf(&b, "磁盘 %s: %s/%s (%d%%)\n", d.Mount, d.Used, d.Size, d.UsePct)
	}
	fmt.Fprintf(&b, "面板: %s %s 端口 %s; 适配器 %s\n", p.Panel.Kind, p.Panel.Version, p.Panel.Port, p.Adapter)
	if len(p.Panel.Apps) > 0 {
		fmt.Fprintf(&b, "面板应用: %s\n", strings.Join(p.Panel.Apps, ", "))
	}
	if len(p.Panel.Runtimes) > 0 {
		fmt.Fprintf(&b, "PHP 运行环境: %s\n", strings.Join(p.Panel.Runtimes, ", "))
	}
	fmt.Fprintf(&b, "Web 服务: %s; 站点: %s\n", p.WebServer, strings.Join(p.Websites, ", "))
	for _, w := range p.WordPress {
		fmt.Fprintf(&b, "WordPress: %s\n", w)
	}
	for _, j := range p.Java {
		fmt.Fprintf(&b, "Java: %s\n", j)
	}
	if len(p.Databases) > 0 {
		fmt.Fprintf(&b, "数据库进程: %s\n", strings.Join(p.Databases, ", "))
	}
	fmt.Fprintf(&b, "Docker: %s, 容器 %d 个\n", p.Docker.Status, len(p.Docker.Containers))
	for _, f := range p.Findings {
		fmt.Fprintf(&b, "发现[%s]: %s（%s）\n", f.Level, f.Title, f.Detail)
	}
	return b.String()
}
