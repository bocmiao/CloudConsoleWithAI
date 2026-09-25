package profile

import (
	"os"
	"strings"
	"testing"
)

func load(t *testing.T, name string) *Profile {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return Parse(string(raw))
}

func hasFinding(p *Profile, level, titlePart string) bool {
	for _, f := range p.Findings {
		if f.Level == level && strings.Contains(f.Title, titlePart) {
			return true
		}
	}
	return false
}

func TestParse1Panel(t *testing.T) {
	p := load(t, "1panel.txt")

	if p.OS != "Ubuntu 24.04.1 LTS" || p.CPUCores != 2 {
		t.Errorf("system: os=%q cores=%d", p.OS, p.CPUCores)
	}
	if p.Memory != (Memory{TotalMB: 1967, UsedMB: 1712, AvailableMB: 150}) {
		t.Errorf("memory = %+v", p.Memory)
	}
	if len(p.Disks) != 2 || p.Disks[0].Mount != "/" || p.Disks[0].UsePct != 92 {
		t.Errorf("disks = %+v", p.Disks)
	}
	if p.Adapter != "1panel" || p.Panel.Version != "v2.0.10" || p.Panel.Port != "23456" {
		t.Errorf("panel = %+v adapter=%s", p.Panel, p.Adapter)
	}
	if strings.Join(p.Panel.Apps, ",") != "mysql/mysql,openresty/openresty,redis/redis" {
		t.Errorf("apps = %v", p.Panel.Apps)
	}
	if strings.Join(p.Websites, ",") != "blog.example.com,www.blog.example.com" {
		t.Errorf("websites = %v", p.Websites)
	}
	if len(p.WordPress) != 1 || !strings.Contains(p.WordPress[0], "version=6.7.1") {
		t.Errorf("wordpress = %v", p.WordPress)
	}
	if strings.Join(p.Databases, ",") != "mysqld 690MB,redis-server 12MB" {
		t.Errorf("databases = %v", p.Databases)
	}
	if p.Docker.Status != "27.3.1" || len(p.Docker.Containers) != 3 {
		t.Errorf("docker = %+v", p.Docker)
	}

	for _, want := range []struct{ level, title string }{
		{"warn", "可用内存偏低"},
		{"info", "没有 swap"},
		{"danger", "磁盘快满了：/"},
		{"danger", "OOM"},
		{"warn", "有服务启动失败"},
		{"warn", "SSH 允许密码登录"},
		{"warn", "root"},
	} {
		if !hasFinding(p, want.level, want.title) {
			t.Errorf("missing finding %s %q; got %+v", want.level, want.title, p.Findings)
		}
	}
	if hasFinding(p, "warn", "/data") || hasFinding(p, "danger", "/data") {
		t.Error("/data at 22% should not be flagged")
	}

	s := p.Summary()
	for _, want := range []string{"可用 150MB", "1panel v2.0.10", "blog.example.com", "磁盘 /: 35G/40G (92%)"} {
		if !strings.Contains(s, want) {
			t.Errorf("summary missing %q:\n%s", want, s)
		}
	}
}

func TestWebServerVersion(t *testing.T) {
	for in, want := range map[string]string{
		"nginx: nginx version: nginx/1.24.0 (Ubuntu) bin=/usr/sbin/nginx":   "nginx/1.24.0",
		"nginx: nginx version: openresty/1.25.3.2 bin=/usr/local/openresty": "openresty/1.25.3.2",
		"apache: Server version: Apache/2.4.58 (Ubuntu)":                    "Apache/2.4.58",
		"caddy: v2.8.4 h1:abc=": "caddy v2.8.4",
	} {
		if got := Parse("== web ==\n" + in + "\n").WebServer; got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
}

func TestParseEmptyAndBT(t *testing.T) {
	p := Parse("")
	if p.Adapter != "linux" || p.Panel.Kind != "none" || len(p.Findings) != 0 {
		t.Errorf("empty input: %+v", p)
	}
	p = Parse("== panel ==\nbt_panel: yes\nbt_panel_port: 8888\nbt_software: nginx php mysql\n1panel: no\n")
	if p.Adapter != "bt" || p.Panel.Port != "8888" || len(p.Panel.Apps) != 3 {
		t.Errorf("bt: %+v", p.Panel)
	}
}
