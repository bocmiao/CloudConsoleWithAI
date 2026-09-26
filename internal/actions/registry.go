// Package actions holds the changes Miao Panel can make to a server: what
// each one needs, how it runs in each environment (a vetted shell script
// or the panel's API), and how to undo it.
package actions

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/bocmiao/CloudConsoleWithAI/internal/core"
	"github.com/bocmiao/CloudConsoleWithAI/internal/freecmd"
)

// splitList splits a list written with commas, spaces or new lines.
func splitList(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == '，' || r == '\n' || r == ' ' || r == '\t' })
}

// lines splits a newline-separated parameter value.
func lines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// Param describes one input of a capability.
type Param struct {
	Name     string
	Desc     string
	Kind     string // int | enum | name | host | subdomain | text | instance | region | port | cidr | iplist | path | script | pathlist | servicelist | localurl | lines
	Min, Max int
	Enum     []string
	Default  string
	Required bool
	// Hidden parameters are filled in by Miao Panel, not proposed by the AI.
	Hidden bool
}

// Impl is how a capability runs in one environment.
type Impl struct {
	Via      string   // shown to the user: 系统脚本 / 1Panel 接口
	Script   string   // action script file (Linux implementations)
	Args     []string // parameter names passed to the script, in order
	Panel    string   // panel operation (API implementations)
	Cloud    string   // Tencent Cloud operation
	Downtime string   // what the user will notice while it runs
	Undo     string   // what rolling it back does, in plain words
	// Encode passes the script's arguments base64-encoded (free text).
	Encode bool
	// Guarded scripts arm a restore on the server that Miao Panel cancels
	// once it has heard back, proving the connection still works.
	Guarded bool
}

// NeedsServer reports whether running this implementation needs the
// server (SSH, or the panel reached through it); cloud operations do not.
func (i Impl) NeedsServer() bool { return i.Script != "" || i.Panel != "" }

// Capability is one kind of change.
type Capability struct {
	Name       string
	Title      string
	Risk       core.Risk
	Reversible bool
	Params     []Param
	// Impls maps an adapter (1panel, bt, linux) to its implementation;
	// "*" applies to every adapter without its own entry.
	Impls map[string]Impl
	// NoUndo says why a change that is not Reversible cannot be rolled back.
	NoUndo string
	// Check validates parameters as a whole, after each one is checked.
	Check func(v map[string]string) error
}

var (
	nameRe      = regexp.MustCompile(`^[A-Za-z0-9@._-]{1,64}$`)
	hostRe      = regexp.MustCompile(`^([A-Za-z0-9_]([A-Za-z0-9_-]{0,61}[A-Za-z0-9])?\.)*[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)
	instanceRe  = regexp.MustCompile(`^(lhins|ins)-[a-z0-9]{6,20}$`)
	regionRe    = regexp.MustCompile(`^[a-z]{2,3}(-[a-z0-9]+){1,3}$`)
	portRe      = regexp.MustCompile(`^(ALL|[0-9]{1,5}(-[0-9]{1,5})?(,[0-9]{1,5}(-[0-9]{1,5})?)*)$`)
	emailRe     = regexp.MustCompile(`^[A-Za-z0-9._%+-]{1,64}@([A-Za-z0-9-]{1,63}\.)+[A-Za-z]{2,63}$`)
	localURLRe  = regexp.MustCompile(`^https?://(127\.0\.0\.1|localhost)(:[0-9]{1,5})?(/[A-Za-z0-9._~/?=&%-]*)?$`)
	pathRe      = regexp.MustCompile(`^/[A-Za-z0-9._~!&()*+,;=:@%/-]*$`)
	subdomainRe = regexp.MustCompile(`^(@|\*|(\*\.)?[A-Za-z0-9_]([A-Za-z0-9_-]{0,61}[A-Za-z0-9])?(\.[A-Za-z0-9_]([A-Za-z0-9_-]{0,61}[A-Za-z0-9])?)*)$`)
)

var registry = map[string]*Capability{}

func register(c *Capability) { registry[c.Name] = c }

func init() {
	register(&Capability{
		Name: "swap.set", Title: "添加 swap", Risk: core.R2, Reversible: true,
		Params: []Param{{Name: "size_gb", Kind: "int", Min: 1, Max: 16, Required: true,
			Desc: "swap 大小（GB）。小内存服务器一般 1~2GB"}},
		Impls: map[string]Impl{"*": {Via: "系统脚本", Script: "swap.sh", Args: []string{"size_gb"}, Downtime: "不影响网站",
			Undo: "关闭并删除新建的 swap 文件，swappiness 恢复成原来的值"}},
	})
	register(&Capability{
		Name: "logs.clean", Title: "清理旧日志", Risk: core.R2,
		NoUndo: "删掉的旧日志无法恢复。只删了 7 天前的归档日志、精简了系统日志和过大的容器日志，不影响网站运行",
		Impls:  map[string]Impl{"*": {Via: "系统脚本", Script: "logs_clean.sh", Downtime: "不影响网站"}},
	})
	register(&Capability{
		Name: "service.restart", Title: "重启服务", Risk: core.R2,
		NoUndo: "重启服务没有修改任何配置，不需要回滚",
		Params: []Param{{Name: "name", Kind: "name", Required: true, Desc: "systemd 服务名，例如 nginx、php8.2-fpm、mysql"}},
		Impls:  map[string]Impl{"*": {Via: "系统脚本", Script: "service_restart.sh", Args: []string{"name"}, Downtime: "这个服务会中断几秒"}},
	})
	register(&Capability{
		Name: "dns.record.set", Title: "设置 DNS 解析", Risk: core.R2, Reversible: true,
		Params: []Param{
			{Name: "domain", Kind: "host", Required: true, Desc: "DNSPod 里的主域名，例如 example.com"},
			{Name: "subdomain", Kind: "subdomain", Required: true, Desc: "主机记录，例如 www、blog；主域名本身填 @"},
			{Name: "point_to", Kind: "enum", Enum: []string{"eo"}, Desc: "填 eo 表示解析到 EdgeOne 为这个域名分配的 CNAME（执行时自动查询，要先有加速域名），这时不用填 type 和 value"},
			{Name: "type", Kind: "enum", Enum: []string{"A", "AAAA", "CNAME", "TXT"}, Desc: "记录类型"},
			{Name: "value", Kind: "text", Desc: "记录值：A 填 IPv4，CNAME 填域名，TXT 填文本"},
			{Name: "ttl", Kind: "int", Min: 60, Max: 86400, Desc: "TTL（秒），不填则沿用原记录，新记录用 600"},
		},
		Impls: map[string]Impl{"*": {Via: "腾讯云接口", Cloud: "dns_record_set", Downtime: "按 TTL 几分钟内在各地生效；A 改 CNAME 是原地修改，不会解析不到",
			Undo: "把这个主机记录的解析恢复成修改前的样子（新增的删除，改过的改回，删掉的加回）"}},
		Check: func(v map[string]string) error {
			switch {
			case v["point_to"] == "eo" && (v["type"] != "" || v["value"] != ""):
				return fmt.Errorf("point_to=eo 时不用填 type 和 value")
			case v["point_to"] == "" && (v["type"] == "" || v["value"] == ""):
				return fmt.Errorf("要填 type 和 value，或者填 point_to=eo")
			}
			return nil
		},
	})
	register(&Capability{
		Name: "eo.domain.add", Title: "添加 EdgeOne 加速域名", Risk: core.R1, Reversible: true,
		Params: []Param{
			{Name: "domain", Kind: "host", Required: true, Desc: "要加速的完整域名，例如 blog.example.com；它所在的站点（example.com）必须已经在 EdgeOne 里"},
			{Name: "origin", Kind: "host", Required: true, Desc: "源站：服务器的公网 IP 或域名"},
			{Name: "origin_protocol", Kind: "enum", Enum: []string{"HTTP", "HTTPS", "FOLLOW"}, Default: "HTTP",
				Desc: "回源协议。服务器上这个网站没有配 HTTPS 证书时用 HTTP（默认）"},
			{Name: "http_port", Kind: "int", Min: 1, Max: 65535, Default: "80", Desc: "HTTP 回源端口"},
			{Name: "https_port", Kind: "int", Min: 1, Max: 65535, Default: "443", Desc: "HTTPS 回源端口"},
		},
		Impls: map[string]Impl{"*": {Via: "腾讯云接口", Cloud: "eo_domain_add", Downtime: "不影响现有访问（DNS 解析到 EdgeOne 之后才生效）",
			Undo: "停用并删除这个加速域名"}},
	})
	register(&Capability{
		Name: "eo.https.set", Title: "设置 EdgeOne HTTPS 证书", Risk: core.R2, Reversible: true,
		Params: []Param{
			{Name: "domain", Kind: "host", Required: true, Desc: "EdgeOne 加速域名，例如 blog.example.com"},
			{Name: "mode", Kind: "enum", Enum: []string{"eofreecert", "disable"}, Default: "eofreecert",
				Desc: "eofreecert：申请并部署免费证书（自动续签，要求域名已经解析到 EdgeOne）；disable：关闭"},
		},
		Impls: map[string]Impl{"*": {Via: "腾讯云接口", Cloud: "eo_https", Downtime: "不影响访问，证书签发一般需要几分钟",
			Undo: "把证书设置恢复成修改前的样子"}},
	})
	register(&Capability{
		Name: "cloud.firewall.open", Title: "腾讯云防火墙放行端口", Risk: core.R1, Reversible: true,
		Params: []Param{
			{Name: "instance", Kind: "instance", Required: true, Desc: "腾讯云实例 ID（tencent_servers 返回的 id，lhins- 开头是轻量服务器，ins- 开头是云服务器 CVM）"},
			{Name: "region", Kind: "region", Required: true, Desc: "实例所在地域，例如 ap-guangzhou（tencent_servers 返回的 region）"},
			{Name: "port", Kind: "port", Required: true, Desc: "端口：80、80,443、8000-8100"},
			{Name: "protocol", Kind: "enum", Enum: []string{"TCP", "UDP"}, Default: "TCP", Desc: "协议"},
			{Name: "cidr", Kind: "cidr", Default: "0.0.0.0/0", Desc: "允许哪些来源访问：0.0.0.0/0 表示所有人，也可以只写一个 IP"},
			{Name: "description", Kind: "text", Desc: "规则备注"},
		},
		Impls: map[string]Impl{"*": {Via: "腾讯云接口", Cloud: "firewall_open", Downtime: "不影响现有访问",
			Undo: "删除这条放行规则"}},
	})
	register(&Capability{
		Name: "cloud.firewall.close", Title: "腾讯云防火墙关闭端口", Risk: core.R2, Reversible: true,
		Params: []Param{
			{Name: "instance", Kind: "instance", Required: true, Desc: "腾讯云实例 ID（tencent_servers 返回的 id，lhins- 开头是轻量服务器，ins- 开头是云服务器 CVM）"},
			{Name: "region", Kind: "region", Required: true, Desc: "实例所在地域，例如 ap-guangzhou（tencent_servers 返回的 region）"},
			{Name: "port", Kind: "port", Required: true, Desc: "要关闭的端口（22、3389 远程登录端口不允许关闭）"},
			{Name: "protocol", Kind: "enum", Enum: []string{"TCP", "UDP"}, Default: "TCP", Desc: "协议"},
			{Name: "cidr", Kind: "cidr", Default: "0.0.0.0/0", Desc: "要删除的规则的来源，默认 0.0.0.0/0"},
		},
		Impls: map[string]Impl{"*": {Via: "腾讯云接口", Cloud: "firewall_close", Downtime: "用这个端口的访问会被拒绝",
			Undo: "把删掉的放行规则加回去"}},
	})
	register(&Capability{
		Name: "cloud.snapshot.create", Title: "创建服务器快照", Risk: core.R1,
		NoUndo: "快照是新增的整盘备份，不需要回滚；不再需要时可以在腾讯云控制台删除（超出免费额度的快照会产生费用）",
		Params: []Param{
			{Name: "instance", Kind: "instance", Required: true, Desc: "腾讯云实例 ID（tencent_servers 返回的 id，lhins- 开头是轻量服务器，ins- 开头是云服务器 CVM）"},
			{Name: "region", Kind: "region", Required: true, Desc: "实例所在地域，例如 ap-guangzhou（tencent_servers 返回的 region）"},
			{Name: "name", Kind: "text", Desc: "快照名称，不填自动生成"},
		},
		Impls: map[string]Impl{"*": {Via: "腾讯云接口", Cloud: "snapshot", Downtime: "不影响运行；轻量服务器有免费快照额度，云服务器快照按容量收费"}},
	})
	register(&Capability{
		Name: "cloud.server.reboot", Title: "重启腾讯云服务器", Risk: core.R3,
		NoUndo: "重启没有修改任何配置，不需要回滚",
		Params: []Param{{Name: "instance", Kind: "instance", Required: true, Desc: "腾讯云实例 ID（tencent_servers 返回的 id，lhins- 开头是轻量服务器，ins- 开头是云服务器 CVM）"},
			{Name: "region", Kind: "region", Required: true, Desc: "实例所在地域，例如 ap-guangzhou（tencent_servers 返回的 region）"}},
		Impls: map[string]Impl{"*": {Via: "腾讯云接口", Cloud: "power_reboot", Downtime: "整台服务器上的网站和服务中断一到几分钟"}},
	})
	register(&Capability{
		Name: "cloud.server.stop", Title: "关闭腾讯云服务器", Risk: core.R3, Reversible: true,
		Params: []Param{{Name: "instance", Kind: "instance", Required: true, Desc: "腾讯云实例 ID（tencent_servers 返回的 id，lhins- 开头是轻量服务器，ins- 开头是云服务器 CVM）"},
			{Name: "region", Kind: "region", Required: true, Desc: "实例所在地域，例如 ap-guangzhou（tencent_servers 返回的 region）"}},
		Impls: map[string]Impl{"*": {Via: "腾讯云接口", Cloud: "power_stop", Downtime: "整台服务器上的网站和服务全部停止，直到重新开机",
			Undo: "重新开机"}},
	})
	register(&Capability{
		Name: "cloud.server.start", Title: "启动腾讯云服务器", Risk: core.R2, Reversible: true,
		Params: []Param{{Name: "instance", Kind: "instance", Required: true, Desc: "腾讯云实例 ID（tencent_servers 返回的 id，lhins- 开头是轻量服务器，ins- 开头是云服务器 CVM）"},
			{Name: "region", Kind: "region", Required: true, Desc: "实例所在地域，例如 ap-guangzhou（tencent_servers 返回的 region）"}},
		Impls: map[string]Impl{"*": {Via: "腾讯云接口", Cloud: "power_start", Downtime: "开机需要一两分钟",
			Undo: "重新关机"}},
	})
	register(&Capability{
		Name: "eo.cache.purge", Title: "清除 EdgeOne 缓存", Risk: core.R2,
		NoUndo: "清除缓存只是让节点重新从源站拉取内容，不需要也无法回滚",
		Params: []Param{
			{Name: "domain", Kind: "host", Required: true, Desc: "站点或加速域名，例如 blog.example.com"},
			{Name: "type", Kind: "enum", Enum: []string{"url", "prefix", "host", "all"}, Default: "url",
				Desc: "url：指定网址；prefix：目录；host：整个域名；all：整个站点（会让源站压力突增，尽量少用）"},
			{Name: "targets", Kind: "text", Desc: "要清除的完整网址或目录（https:// 开头），多个用逗号或换行分隔；host 类型填域名，不填就是 domain；all 不用填"},
			{Name: "method", Kind: "enum", Enum: []string{"invalidate", "delete"}, Default: "invalidate",
				Desc: "invalidate：只刷新有更新的内容（默认）；delete：全部删除"},
		},
		Impls: map[string]Impl{"*": {Via: "腾讯云接口", Cloud: "eo_purge", Downtime: "不中断访问，清除后的第一次访问会慢一点"}},
	})
	register(&Capability{
		Name: "eo.cache.prefetch", Title: "预热 EdgeOne 缓存", Risk: core.R1,
		NoUndo: "预热只是提前把内容缓存到节点，不需要回滚",
		Params: []Param{
			{Name: "domain", Kind: "host", Required: true, Desc: "站点或加速域名"},
			{Name: "targets", Kind: "text", Required: true, Desc: "要预热的完整网址（https:// 开头），多个用逗号或换行分隔"},
		},
		Impls: map[string]Impl{"*": {Via: "腾讯云接口", Cloud: "eo_prefetch", Downtime: "不影响访问"}},
	})
	register(&Capability{
		Name: "eo.domain.status", Title: "启用或停用 EdgeOne 加速域名", Risk: core.R2, Reversible: true,
		Params: []Param{
			{Name: "domain", Kind: "host", Required: true, Desc: "加速域名"},
			{Name: "status", Kind: "enum", Enum: []string{"online", "offline"}, Required: true, Desc: "online：启用；offline：停用"},
		},
		Impls: map[string]Impl{"*": {Via: "腾讯云接口", Cloud: "eo_status", Downtime: "停用后这个域名经过 EdgeOne 的访问会失败",
			Undo: "恢复原来的启用状态"}},
	})
	register(&Capability{
		Name: "eo.origin.set", Title: "修改 EdgeOne 回源地址", Risk: core.R2, Reversible: true,
		Params: []Param{
			{Name: "domain", Kind: "host", Required: true, Desc: "加速域名"},
			{Name: "origin", Kind: "host", Required: true, Desc: "新的源站：服务器公网 IP 或域名"},
			{Name: "origin_protocol", Kind: "enum", Enum: []string{"HTTP", "HTTPS", "FOLLOW"}, Desc: "回源协议，不填保持原样"},
			{Name: "http_port", Kind: "int", Min: 1, Max: 65535, Desc: "HTTP 回源端口，不填保持原样"},
			{Name: "https_port", Kind: "int", Min: 1, Max: 65535, Desc: "HTTPS 回源端口，不填保持原样"},
		},
		Impls: map[string]Impl{"*": {Via: "腾讯云接口", Cloud: "eo_origin", Downtime: "新源站如果没准备好这个网站，访问会出错",
			Undo: "改回原来的源站和端口"}},
	})
	register(&Capability{
		Name: "eo.zone.create", Title: "新建 EdgeOne 站点", Risk: core.R2, Reversible: true,
		Params: []Param{
			{Name: "domain", Kind: "host", Required: true, Desc: "主域名，例如 example.com（不要带 www 等前缀）"},
			{Name: "area", Kind: "enum", Enum: []string{"mainland", "overseas", "global"}, Required: true,
				Desc: "加速区域：mainland 中国大陆（域名要有 ICP 备案）、overseas 全球不含中国大陆、global 全球含中国大陆（要备案）"},
			{Name: "plan_id", Kind: "name", Desc: "绑定哪个套餐（edgeone- 开头），不填自动选一个还能绑定站点的套餐"},
		},
		Impls: map[string]Impl{"*": {Via: "腾讯云接口", Cloud: "eo_zone_create",
			Downtime: "不影响现有访问：CNAME 接入的站点要等 DNS 解析到 EdgeOne 后才生效。域名在本账号的 DNSPod 里时，会自动添加验证记录完成归属验证",
			Undo:     "删除新建的站点和自动添加的验证记录（站点里已经有加速域名时拒绝删除）"}},
	})
	register(&Capability{
		Name: "eo.ip.block", Title: "EdgeOne 封禁 IP", Risk: core.R2, Reversible: true,
		Params: []Param{
			{Name: "domain", Kind: "host", Required: true, Desc: "站点或站点下的域名，用来找到 EdgeOne 站点；封禁对整个站点生效"},
			{Name: "ips", Kind: "iplist", Required: true, Desc: "要封禁的 IP 或网段，多个用逗号分隔，例如 1.2.3.4,5.6.7.0/24"},
		},
		Impls: map[string]Impl{"*": {Via: "腾讯云接口", Cloud: "eo_ip_block", Downtime: "这些 IP 的访问会被 EdgeOne 拦截（返回拦截页面），几十秒内生效",
			Undo: "把 Miao Panel 的封禁列表恢复成修改前的样子"}},
	})
	register(&Capability{
		Name: "eo.ip.unblock", Title: "EdgeOne 解除 IP 封禁", Risk: core.R1, Reversible: true,
		Params: []Param{
			{Name: "domain", Kind: "host", Required: true, Desc: "站点或站点下的域名"},
			{Name: "ips", Kind: "iplist", Required: true, Desc: "要解除封禁的 IP 或网段，多个用逗号分隔（只能解除 Miao Panel 封禁的）"},
		},
		Impls: map[string]Impl{"*": {Via: "腾讯云接口", Cloud: "eo_ip_unblock", Downtime: "这些 IP 可以重新访问",
			Undo: "重新封禁这些 IP"}},
	})
	register(&Capability{
		Name: "eo.ratelimit.set", Title: "EdgeOne 速率限制", Risk: core.R2, Reversible: true,
		Params: []Param{
			{Name: "domain", Kind: "host", Required: true, Desc: "填站点主域名表示整个站点；填站点下的域名只限制这个域名"},
			{Name: "path", Kind: "path", Desc: "只统计路径里包含这段的请求，例如 /wp-login.php、/api/；不填统计所有请求"},
			{Name: "threshold", Kind: "int", Min: 1, Max: 100000, Required: true, Desc: "同一个 IP 在统计周期内最多请求多少次"},
			{Name: "period", Kind: "enum", Enum: []string{"1s", "5s", "10s", "20s", "30s", "40s", "50s", "1m", "2m", "5m", "10m", "1h"}, Default: "1m", Desc: "统计周期"},
			{Name: "action", Kind: "enum", Enum: []string{"challenge", "deny", "monitor"}, Default: "challenge",
				Desc: "超过后怎么处理：challenge JavaScript 挑战（真人浏览器能自动通过）、deny 直接拦截、monitor 只记录"},
			{Name: "duration", Kind: "text", Default: "10m", Desc: "处理持续多久，例如 10m、1h（秒/分钟最多 120，小时最多 48，天最多 30）"},
		},
		Impls: map[string]Impl{"*": {Via: "腾讯云接口", Cloud: "eo_ratelimit", Downtime: "超过阈值的访客会被挑战或拦截；阈值太低可能误伤正常用户",
			Undo: "删除这条规则；如果是修改了已有的同名规则，恢复原来的设置"}},
		Check: func(v map[string]string) error { return checkDuration(v["duration"]) },
	})
	register(&Capability{
		Name: "eo.ratelimit.remove", Title: "删除 EdgeOne 速率限制规则", Risk: core.R2, Reversible: true,
		Params: []Param{
			{Name: "domain", Kind: "host", Required: true, Desc: "站点或站点下的域名"},
			{Name: "name", Kind: "text", Required: true, Desc: "规则名称（tencent_eo_security 返回的 name）"},
		},
		Impls: map[string]Impl{"*": {Via: "腾讯云接口", Cloud: "eo_ratelimit_remove", Downtime: "这条限制不再生效",
			Undo: "把规则按原来的设置加回去"}},
	})
	register(&Capability{
		Name: "eo.cc.set", Title: "设置 EdgeOne CC 防护", Risk: core.R2, Reversible: true,
		Params: []Param{
			{Name: "domain", Kind: "host", Required: true, Desc: "站点或站点下的域名；对整个站点生效"},
			{Name: "enabled", Kind: "enum", Enum: []string{"on", "off"}, Required: true, Desc: "on 开启，off 关闭"},
			{Name: "sensitivity", Kind: "enum", Enum: []string{"Loose", "Moderate", "Strict"}, Default: "Moderate", Desc: "灵敏度：Loose 宽松、Moderate 适中、Strict 严格"},
			{Name: "action", Kind: "enum", Enum: []string{"challenge", "deny", "monitor"}, Default: "challenge", Desc: "识别到攻击后怎么处理"},
		},
		Impls: map[string]Impl{"*": {Via: "腾讯云接口", Cloud: "eo_cc", Downtime: "EdgeOne 按访问基线自动识别异常的高频访问并处理；严格模式可能误伤正常用户",
			Undo: "恢复原来的 CC 防护设置"}},
	})
	register(&Capability{
		Name: "free_command", Title: "AI 自由命令", Risk: core.R2, Reversible: true,
		Params: []Param{
			{Name: "goal", Kind: "text", Required: true, Desc: "用大白话说这一步要做什么、为什么，例如「给 Nginx 开启 gzip 压缩，减少网页传输大小」"},
			{Name: "script", Kind: "script", Required: true, Desc: "要执行的 shell 命令（规则见系统说明；最多 4000 字节）"},
			{Name: "files", Kind: "pathlist", Desc: "命令会写入、新建或删除的所有文件的绝对路径，逗号分隔；必须和命令完全一致"},
			{Name: "services", Kind: "servicelist", Desc: "命令会重载或重启的服务，逗号分隔，例如 nginx、php8.2-fpm；Docker 容器写 docker:容器名；必须和命令完全一致"},
			{Name: "check_url", Kind: "localurl", Desc: "执行后用来检查网站是否正常的本机地址，例如 http://127.0.0.1/ ；可不填"},
			{Name: "expect", Kind: "lines", Hidden: true},
		},
		Impls: map[string]Impl{"*": {Via: "AI 自由命令", Script: "free_command.sh", Args: []string{"script", "files", "services", "check_url", "expect"},
			Encode: true, Guarded: true, Downtime: "改动见下面的说明；重载服务一般不中断访问",
			Undo: "把改动过的文件恢复成执行前的备份，并重新加载相关服务"}},
		Check: func(v map[string]string) error {
			a := freecmd.Analyze(freecmd.Declaration{Script: v["script"], Files: lines(v["files"]), Services: lines(v["services"])})
			if !a.OK() {
				return fmt.Errorf("命令没有通过检查：%s", strings.Join(a.Problems, "；"))
			}
			return nil
		},
	})
	register(&Capability{
		Name: "container.restart", Title: "重启容器", Risk: core.R2,
		NoUndo: "重启容器没有修改任何配置，不需要回滚",
		Params: []Param{{Name: "name", Kind: "name", Required: true, Desc: "Docker 容器名（docker ps 里的 NAMES，例如 1Panel-halo-xxxx）"}},
		Impls:  map[string]Impl{"*": {Via: "系统脚本", Script: "container_restart.sh", Args: []string{"name"}, Downtime: "这个容器里的服务会中断几秒到几十秒"}},
	})
	register(&Capability{
		Name: "site.create", Title: "新建网站", Risk: core.R2, Reversible: true,
		Params: []Param{
			{Name: "domain", Kind: "host", Required: true, Desc: "网站域名，例如 blog.example.com"},
			{Name: "type", Kind: "enum", Enum: []string{"proxy", "static"}, Default: "proxy",
				Desc: "proxy 反向代理到一个应用（例如 1Panel 里装的 Halo、WordPress 容器）；static 静态网站（放 HTML 文件）"},
			{Name: "app", Kind: "name", Desc: "反向代理到哪个 1Panel 应用（用它对外的端口），例如 halo"},
			{Name: "proxy", Kind: "text", Desc: "或者直接填后端地址，例如 http://127.0.0.1:8090"},
		},
		Impls: map[string]Impl{
			"1panel": {Via: "1Panel 接口", Panel: "site_create", Downtime: "不影响其他网站；1Panel 会重新加载 OpenResty",
				Undo: "在 1Panel 里删除这个网站和它的目录（不删除应用和数据库；静态网站目录里后来放的文件也会一起删除）"},
		},
		Check: checkSiteParams,
	})
	register(&Capability{
		Name: "cert.issue", Title: "申请证书并开启自动续签", Risk: core.R2, Reversible: true,
		Params: []Param{
			{Name: "domain", Kind: "certhost", Required: true, Desc: "证书的主域名，例如 blog.example.com；泛域名写 *.example.com（必须用 DNS 验证）"},
			{Name: "other_domains", Kind: "hostlist", Desc: "同一张证书里的其他域名，逗号分隔，例如 www.example.com"},
			{Name: "method", Kind: "enum", Enum: []string{"http", "dns"}, Default: "http",
				Desc: "http：Let's Encrypt 访问网站验证（域名要能访问到这台服务器，经过 EdgeOne 也可以）；dns：用 1Panel 里配置好的 DNS 账号验证（泛域名必须用）"},
			{Name: "dns_account", Kind: "text", Desc: "method=dns 时用哪个 1Panel DNS 账号（只有一个时可以不填）"},
			{Name: "email", Kind: "email", Desc: "1Panel 里还没有 Let's Encrypt 账号时，用这个邮箱注册（接收到期提醒）"},
			{Name: "apply", Kind: "enum", Enum: []string{"yes", "no"}, Default: "yes", Desc: "yes：申请后给同名网站（或 website 指定的网站）开启 HTTPS"},
			{Name: "website", Kind: "host", Desc: "要开启 HTTPS 的 1Panel 网站主域名，不填就是 domain"},
			{Name: "http_mode", Kind: "enum", Enum: []string{"HTTPAlso", "HTTPToHTTPS", "HTTPSOnly"}, Default: "HTTPAlso",
				Desc: "HTTPAlso：HTTP 和 HTTPS 都能访问（EdgeOne 用 HTTP 回源时必须选这个）；HTTPToHTTPS：HTTP 跳转到 HTTPS；HTTPSOnly：只能 HTTPS"},
		},
		Impls: map[string]Impl{
			"1panel": {Via: "1Panel 接口", Panel: "cert_issue", Downtime: "不中断访问；1Panel 重新加载 OpenResty",
				Undo: "恢复网站原来的 HTTPS 设置，并删除新申请的证书"},
		},
		Check: func(v map[string]string) error {
			if strings.HasPrefix(v["domain"], "*.") && v["method"] != "dns" {
				return fmt.Errorf("泛域名证书必须用 DNS 验证（method=dns）")
			}
			for _, d := range lines(v["other_domains"]) {
				if strings.HasPrefix(d, "*.") && v["method"] != "dns" {
					return fmt.Errorf("泛域名证书必须用 DNS 验证（method=dns）")
				}
			}
			return nil
		},
	})
	register(&Capability{
		Name: "cert.renew", Title: "立即续签证书", Risk: core.R1,
		NoUndo: "续签只是换一张新的证书，不需要回滚",
		Params: []Param{{Name: "domain", Kind: "certhost", Required: true, Desc: "1Panel 里证书的主域名"}},
		Impls: map[string]Impl{
			"1panel": {Via: "1Panel 接口", Panel: "cert_renew", Downtime: "不中断访问"},
		},
	})
	register(&Capability{
		Name: "cert.autorenew.set", Title: "开关证书自动续签", Risk: core.R1, Reversible: true,
		Params: []Param{
			{Name: "domain", Kind: "certhost", Required: true, Desc: "1Panel 里证书的主域名"},
			{Name: "enabled", Kind: "enum", Enum: []string{"on", "off"}, Required: true, Desc: "on 开启，off 关闭"},
		},
		Impls: map[string]Impl{
			"1panel": {Via: "1Panel 接口", Panel: "cert_autorenew", Downtime: "不影响访问", Undo: "改回原来的设置"},
		},
	})
	register(&Capability{
		Name: "app.limits.set", Title: "设置应用内存上限", Risk: core.R2, Reversible: true,
		Params: []Param{
			{Name: "app", Kind: "name", Required: true, Desc: "1Panel 应用名称（应用商店 → 已安装 里显示的名字，例如 halo、mysql）"},
			{Name: "memory_mb", Kind: "int", Min: 0, Max: 262144, Required: true,
				Desc: "内存上限（MB），0 表示取消限制。不能低于当前实际占用的 1.2 倍。Java 应用（如 Halo）没有固定堆时，JVM 默认最大堆是上限的 1/4，所以要先用 java.heap.set 固定堆"},
		},
		Impls: map[string]Impl{
			"1panel": {Via: "1Panel 接口", Panel: "app_limits", Downtime: "1Panel 会重建这个应用的容器，服务中断十几秒到一分钟",
				Undo: "通过 1Panel 把内存上限改回原来的值（会再重建一次容器）"},
		},
		Check: func(v map[string]string) error {
			if n, _ := strconv.Atoi(v["memory_mb"]); n > 0 && n < 64 {
				return fmt.Errorf("memory_mb 至少 64，或者填 0 表示取消限制")
			}
			return nil
		},
	})
	register(&Capability{
		Name: "java.heap.set", Title: "固定 Java 应用的最大堆", Risk: core.R2, Reversible: true,
		Params: []Param{
			{Name: "app", Kind: "name", Required: true, Desc: "1Panel 应用名称（例如 halo）"},
			{Name: "max_heap_mb", Kind: "int", Min: 64, Max: 32768, Required: true,
				Desc: "Java 最大堆（MB）。之后要设容器内存上限的话，堆一般取上限的 70%~75%，给非堆内存留余量"},
		},
		Impls: map[string]Impl{
			"1panel": {Via: "1Panel 接口", Panel: "java_heap", Downtime: "1Panel 会重建这个应用的容器，服务中断十几秒到一分钟",
				Undo: "通过 1Panel 把应用的 docker-compose 配置恢复成修改前的样子（会再重建一次容器）"},
		},
	})
	register(&Capability{
		Name: "backup.create", Title: "备份应用或数据库", Risk: core.R1,
		NoUndo: "备份只是新增了一份备份文件，没有改动任何东西，不需要回滚；不需要时可以在 1Panel「备份」里删除",
		Params: []Param{
			{Name: "app", Kind: "name", Required: true, Desc: "1Panel 应用名称。MySQL/MariaDB 应用会备份里面的数据库，其他应用备份整个应用（程序和数据）"},
			{Name: "database", Kind: "name", Desc: "只备份这一个数据库（仅 MySQL/MariaDB；不填则备份全部）"},
		},
		Impls: map[string]Impl{
			"1panel": {Via: "1Panel 接口", Panel: "backup", Downtime: "不影响网站，大的数据库需要几分钟"},
		},
	})
	register(&Capability{
		Name: "php_fpm.set", Title: "调整 PHP-FPM 进程数", Risk: core.R2, Reversible: true,
		Params: []Param{
			{Name: "max_children", Kind: "int", Min: 2, Max: 500, Required: true,
				Desc: "最大进程数，约等于「可分给 PHP 的内存 ÷ 单个进程平均内存」"},
			{Name: "pm", Kind: "enum", Enum: []string{"dynamic", "ondemand", "static"}, Default: "dynamic",
				Desc: "进程管理方式；小内存服务器建议 ondemand"},
			{Name: "runtime", Kind: "name", Desc: "1Panel 的 PHP 运行环境名称（只有一个时可以不填）"},
		},
		Impls: map[string]Impl{
			"linux": {Via: "系统脚本", Script: "php_fpm.sh", Args: []string{"max_children", "pm"}, Downtime: "平滑重载，不中断网站",
				Undo: "用修改前备份的配置文件覆盖回去，检查通过后平滑重载 PHP-FPM"},
			"1panel": {Via: "1Panel 接口", Panel: "php_fpm", Downtime: "PHP 运行环境会重启，网站中断几秒",
				Undo: "通过 1Panel 把 PHP-FPM 参数改回原来的值（PHP 运行环境会重启，网站中断几秒）"},
		},
	})
	register(&Capability{
		Name: "mysql.vars.set", Title: "调整 MySQL 内存参数", Risk: core.R2, Reversible: true,
		Params: []Param{
			{Name: "innodb_buffer_pool_size_mb", Kind: "int", Min: 32, Max: 65536,
				Desc: "InnoDB 缓冲池大小（MB），MySQL 最主要的内存占用"},
			{Name: "max_connections", Kind: "int", Min: 10, Max: 10000, Desc: "最大连接数"},
			{Name: "database", Kind: "name", Desc: "1Panel 里 MySQL 应用的名称（只有一个时可以不填）"},
		},
		Impls: map[string]Impl{
			"1panel": {Via: "1Panel 接口", Panel: "mysql_vars", Downtime: "MySQL 会重启，网站中断约 10 秒",
				Undo: "通过 1Panel 把 MySQL 参数改回原来的值（MySQL 会重启，网站中断约 10 秒）"},
		},
		Check: func(v map[string]string) error {
			if v["innodb_buffer_pool_size_mb"] == "" && v["max_connections"] == "" {
				return fmt.Errorf("至少要改 innodb_buffer_pool_size_mb 或 max_connections 其中一个")
			}
			return nil
		},
	})
}

// Names lists every capability the AI may propose: the executable ones
// plus those in the risk policy that later versions will run.
func Names() []string {
	seen := map[string]bool{}
	var out []string
	for n := range registry {
		seen[n] = true
		out = append(out, n)
	}
	for _, n := range core.Capabilities() {
		if !seen[n] {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// Describe documents the executable capabilities for the AI's tool list.
func Describe() string {
	names := make([]string, 0, len(registry))
	for n := range registry {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, n := range names {
		c := registry[n]
		envs := make([]string, 0, len(c.Impls))
		for a := range c.Impls {
			envs = append(envs, a)
		}
		sort.Strings(envs)
		where := "支持环境：" + strings.ReplaceAll(strings.Join(envs, "/"), "*", "全部")
		if impl, ok := c.Impls["*"]; ok && impl.Cloud != "" {
			where = "腾讯云，不需要服务器"
		}
		fmt.Fprintf(&b, "- %s（%s；%s）", n, c.Title, where)
		if len(c.Params) > 0 {
			var ps []string
			for _, p := range c.Params {
				if p.Hidden {
					continue
				}
				s := p.Name
				switch p.Kind {
				case "int":
					s += fmt.Sprintf(" 整数 %d~%d", p.Min, p.Max)
				case "enum":
					s += " 可选 " + strings.Join(p.Enum, "|")
				}
				if p.Required {
					s += " 必填"
				}
				ps = append(ps, s+"："+p.Desc)
			}
			b.WriteString(" 参数：" + strings.Join(ps, "；"))
		}
		b.WriteString("\n")
	}
	return b.String()
}

// Resolved is a validated step, ready to run on one environment.
type Resolved struct {
	Cap    *Capability
	Impl   Impl
	Values map[string]string
}

// ErrNotExecutable explains why a proposed step cannot run (yet).
type ErrNotExecutable struct{ Reason string }

func (e *ErrNotExecutable) Error() string { return e.Reason }

// Resolve validates a step's parameters for an environment.
func Resolve(capability string, params map[string]any, adapter string) (Resolved, error) {
	c, ok := registry[capability]
	if !ok {
		if capability == "free_command" {
			return Resolved{}, &ErrNotExecutable{"需要「AI 自由命令」功能，下一个版本提供"}
		}
		return Resolved{}, &ErrNotExecutable{"这类操作还不能自动执行，后续版本会支持"}
	}
	impl, ok := c.Impls[adapter]
	if !ok {
		impl, ok = c.Impls["*"]
	}
	if !ok {
		return Resolved{}, &ErrNotExecutable{fmt.Sprintf("当前环境（%s）还不支持自动执行这个操作", adapterName(adapter))}
	}
	known := map[string]bool{}
	values := map[string]string{}
	for _, p := range c.Params {
		known[p.Name] = true
		v, err := paramValue(p, params[p.Name])
		if err != nil {
			return Resolved{}, err
		}
		if v != "" {
			values[p.Name] = v
		}
	}
	for k := range params {
		if !known[k] {
			return Resolved{}, fmt.Errorf("%s 没有参数 %s", capability, k)
		}
	}
	if c.Check != nil {
		if err := c.Check(values); err != nil {
			return Resolved{}, err
		}
	}
	return Resolved{Cap: c, Impl: impl, Values: values}, nil
}

func paramValue(p Param, raw any) (string, error) {
	var s string
	switch v := raw.(type) {
	case nil:
	case string:
		s = strings.TrimSpace(v)
	case float64:
		if v != math.Trunc(v) {
			return "", fmt.Errorf("参数 %s 需要是整数", p.Name)
		}
		s = strconv.FormatInt(int64(v), 10)
	case int:
		s = strconv.Itoa(v)
	case bool:
		s = strconv.FormatBool(v)
	default:
		return "", fmt.Errorf("参数 %s 的格式不对", p.Name)
	}
	if s == "" {
		if p.Required {
			return "", fmt.Errorf("缺少参数 %s（%s）", p.Name, p.Desc)
		}
		return p.Default, nil
	}
	switch p.Kind {
	case "int":
		n, err := strconv.Atoi(s)
		if err != nil || n < p.Min || n > p.Max {
			return "", fmt.Errorf("参数 %s 需要是 %d 到 %d 之间的整数", p.Name, p.Min, p.Max)
		}
		return strconv.Itoa(n), nil
	case "enum":
		for _, e := range p.Enum {
			if s == e {
				return s, nil
			}
		}
		return "", fmt.Errorf("参数 %s 只能是 %s", p.Name, strings.Join(p.Enum, "、"))
	case "name":
		if !nameRe.MatchString(s) {
			return "", fmt.Errorf("参数 %s 只能包含字母、数字和 @._-", p.Name)
		}
		return s, nil
	case "host":
		if len(s) > 253 || !hostRe.MatchString(s) {
			return "", fmt.Errorf("参数 %s 要是域名或 IP 地址，%q 不是", p.Name, s)
		}
		return strings.ToLower(s), nil
	case "subdomain":
		if len(s) > 200 || !subdomainRe.MatchString(s) {
			return "", fmt.Errorf("参数 %s 要是主机记录（例如 www、blog，主域名本身填 @），%q 不是", p.Name, s)
		}
		return strings.ToLower(s), nil
	case "instance":
		if !instanceRe.MatchString(s) {
			return "", fmt.Errorf("参数 %s 要是腾讯云实例 ID（lhins- 或 ins- 开头），%q 不是", p.Name, s)
		}
		return s, nil
	case "region":
		if !regionRe.MatchString(s) {
			return "", fmt.Errorf("参数 %s 要是地域，例如 ap-guangzhou，%q 不是", p.Name, s)
		}
		return s, nil
	case "port":
		s = strings.ToUpper(strings.ReplaceAll(s, " ", ""))
		if !portRe.MatchString(s) {
			return "", fmt.Errorf("参数 %s 要是端口，例如 80、80,443、8000-8100 或 ALL，%q 不是", p.Name, s)
		}
		for _, n := range regexp.MustCompile(`[0-9]+`).FindAllString(s, -1) {
			if v, _ := strconv.Atoi(n); v < 1 || v > 65535 {
				return "", fmt.Errorf("端口 %s 超出范围", n)
			}
		}
		return s, nil
	case "cidr":
		if err := checkCIDR(s); err != nil {
			return "", err
		}
		return s, nil
	case "iplist":
		ips, err := parseIPList(s)
		if err != nil {
			return "", fmt.Errorf("参数 %s：%v", p.Name, err)
		}
		if len(ips) > 200 {
			return "", fmt.Errorf("参数 %s 一次最多 200 个 IP", p.Name)
		}
		return strings.Join(ips, ","), nil
	case "path":
		if len(s) > 256 || !pathRe.MatchString(s) {
			return "", fmt.Errorf("参数 %s 要是以 / 开头的网址路径，例如 /wp-login.php，%q 不是", p.Name, s)
		}
		return s, nil
	case "script":
		if len(s) > freecmd.MaxScript || strings.ContainsFunc(s, func(r rune) bool { return (r < 0x20 && r != '\n' && r != '\t') || r == 0x7f }) {
			return "", fmt.Errorf("参数 %s 太长（最多 %d 字节）或含有控制字符", p.Name, freecmd.MaxScript)
		}
		return strings.ReplaceAll(s, "\r\n", "\n"), nil
	case "pathlist":
		var out []string
		for _, f := range splitList(s) {
			if err := freecmd.CheckPath(f); err != nil {
				return "", fmt.Errorf("参数 %s 里的 %s：%v", p.Name, f, err)
			}
			out = append(out, f)
		}
		return strings.Join(out, "\n"), nil
	case "servicelist":
		var out []string
		for _, n := range splitList(s) {
			if err := freecmd.CheckService(n); err != nil {
				return "", fmt.Errorf("参数 %s：%v", p.Name, err)
			}
			out = append(out, strings.TrimSuffix(n, ".service"))
		}
		return strings.Join(out, "\n"), nil
	case "certhost":
		name := strings.ToLower(s)
		if len(name) > 253 || !hostRe.MatchString(strings.TrimPrefix(name, "*.")) || !strings.Contains(name, ".") {
			return "", fmt.Errorf("参数 %s 要是域名，例如 blog.example.com 或 *.example.com，%q 不是", p.Name, s)
		}
		return name, nil
	case "hostlist":
		var out []string
		for _, d := range splitList(s) {
			d = strings.ToLower(d)
			if !hostRe.MatchString(strings.TrimPrefix(d, "*.")) || !strings.Contains(d, ".") {
				return "", fmt.Errorf("参数 %s 里的 %q 不是域名", p.Name, d)
			}
			out = append(out, d)
		}
		return strings.Join(out, "\n"), nil
	case "email":
		if !emailRe.MatchString(s) {
			return "", fmt.Errorf("参数 %s 要是邮箱地址，%q 不是", p.Name, s)
		}
		return s, nil
	case "localurl":
		if !localURLRe.MatchString(s) {
			return "", fmt.Errorf("参数 %s 要是本机地址，例如 http://127.0.0.1/ ，%q 不是", p.Name, s)
		}
		return s, nil
	case "lines":
		if len(s) > 4096 || strings.ContainsAny(s, "\x00\r") {
			return "", fmt.Errorf("参数 %s 不对", p.Name)
		}
		return s, nil
	case "text":
		if len(s) > 512 || strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
			return "", fmt.Errorf("参数 %s 太长或含有控制字符", p.Name)
		}
		return s, nil
	}
	return "", fmt.Errorf("未知的参数类型 %s", p.Kind)
}

func adapterName(a string) string {
	switch a {
	case "1panel":
		return "1Panel"
	case "bt":
		return "宝塔"
	case "linux":
		return "纯 Linux"
	}
	return "未识别"
}
