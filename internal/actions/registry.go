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
)

// Param describes one input of a capability.
type Param struct {
	Name     string
	Desc     string
	Kind     string // int | enum | name
	Min, Max int
	Enum     []string
	Default  string
	Required bool
}

// Impl is how a capability runs in one environment.
type Impl struct {
	Via      string   // shown to the user: 系统脚本 / 1Panel 接口
	Script   string   // action script file (Linux implementations)
	Args     []string // parameter names passed to the script, in order
	Panel    string   // panel operation (API implementations)
	Downtime string   // what the user will notice while it runs
	Undo     string   // what rolling it back does, in plain words
}

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

var nameRe = regexp.MustCompile(`^[A-Za-z0-9@._-]{1,64}$`)

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
		Name: "container.restart", Title: "重启容器", Risk: core.R2,
		NoUndo: "重启容器没有修改任何配置，不需要回滚",
		Params: []Param{{Name: "name", Kind: "name", Required: true, Desc: "Docker 容器名（docker ps 里的 NAMES，例如 1Panel-halo-xxxx）"}},
		Impls:  map[string]Impl{"*": {Via: "系统脚本", Script: "container_restart.sh", Args: []string{"name"}, Downtime: "这个容器里的服务会中断几秒到几十秒"}},
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
		fmt.Fprintf(&b, "- %s（%s；支持环境：%s）", n, c.Title, strings.ReplaceAll(strings.Join(envs, "/"), "*", "全部"))
		if len(c.Params) > 0 {
			var ps []string
			for _, p := range c.Params {
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
