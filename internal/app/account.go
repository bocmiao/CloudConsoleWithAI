package app

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bocmiao/CloudConsoleWithAI/internal/aliyun"
	"github.com/bocmiao/CloudConsoleWithAI/internal/tencent"
)

// The cloud accounts themselves: the money in them and what is about to
// expire (prepaid servers, registered domains). The 云服务器 page shows it
// with a switch for automatic renewal, 总览 lists what needs doing, an
// alert says so in time, and the AI reads it with cloud_account.

const (
	serverSoonDays = 15 // a server that does not renew itself needs the user this close to expiry
	domainSoonDays = 30 // a domain, likewise
	urgentDays     = 7
)

// AccountBalance is one cloud account's money, in yuan.
type AccountBalance struct {
	Provider  string  `json:"provider"` // tencent, aliyun
	Available float64 `json:"available"`
	Owed      float64 `json:"owed,omitempty"`
	Error     string  `json:"error,omitempty"` // why it could not be read
}

// DueServer is a prepaid (包年包月) server and when it expires.
type DueServer struct {
	Provider  string `json:"provider"`
	ID        string `json:"id"`
	Region    string `json:"region"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`    // in words
	Expires   string `json:"expires"` // 2027-05-10
	Days      int    `json:"days"`    // left; negative once expired
	AutoRenew bool   `json:"autoRenew"`
	CanSwitch bool   `json:"canSwitch"` // automatic renewal can be turned on or off here
	Level     string `json:"level"`     // crit, warn, ok
	Note      string `json:"note,omitempty"`
	ServerID  int64  `json:"serverId,omitempty"`
}

// DueDomain is a domain registered with a cloud account.
type DueDomain struct {
	Provider  string `json:"provider"`
	Name      string `json:"name"`
	Expires   string `json:"expires"`
	Days      int    `json:"days"`
	AutoRenew bool   `json:"autoRenew"` // only known on 腾讯云
	Level     string `json:"level"`
	Note      string `json:"note,omitempty"`
}

// AccountView is the money in each cloud account and what expires,
// soonest first.
type AccountView struct {
	Balances []AccountBalance `json:"balances"`
	Servers  []DueServer      `json:"servers"`
	Domains  []DueDomain      `json:"domains"`
	Errors   []string         `json:"errors"` // parts that could not be read
}

var providerName = map[string]string{"tencent": "腾讯云", "aliyun": "阿里云"}

// daysTo counts whole days from today to t's day, in local time.
func daysTo(t time.Time) int {
	y, m, d := time.Now().Date()
	ty, tm, td := t.In(time.Local).Date()
	return int(math.Round(time.Date(ty, tm, td, 0, 0, 0, 0, time.Local).Sub(time.Date(y, m, d, 0, 0, 0, 0, time.Local)).Hours() / 24))
}

// dueLevel says how soon something expiring needs the user.
func dueLevel(days, soon int, auto, broke bool) (level, note string) {
	switch {
	case days < 0:
		return "crit", "已过期"
	case auto && broke && days <= urgentDays:
		return "warn", "账户余额不足，自动续费会失败"
	case auto:
		return "ok", ""
	case days <= urgentDays:
		return "crit", "不会自动续费"
	case days <= soon:
		return "warn", "不会自动续费"
	}
	return "ok", ""
}

// CloudAccount reads the cloud accounts: balances, prepaid servers and
// registered domains. A part that cannot be read (often the key may not
// read 费用中心 or 域名) says why; the rest is still shown.
func (a *App) CloudAccount(ctx context.Context) (AccountView, error) {
	v := AccountView{Balances: []AccountBalance{}, Servers: []DueServer{}, Domains: []DueDomain{}, Errors: []string{}}
	tc, ac := a.tencentClient(), a.aliyunClient()
	if tc == nil && ac == nil {
		return v, userErr("还没有配置腾讯云或阿里云的密钥（设置 → 腾讯云 / 阿里云）")
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	run := func(f func()) {
		wg.Add(1)
		go func() { defer wg.Done(); f() }()
	}
	failed := func(what string, err error) {
		mu.Lock()
		v.Errors = append(v.Errors, what+"："+err.Error())
		mu.Unlock()
	}
	balance := map[string]AccountBalance{}
	var servers []DueServer
	var domains []DueDomain
	if tc != nil {
		run(func() {
			b, err := tc.Balance(ctx)
			ab := AccountBalance{Provider: "tencent", Available: b.Available, Owed: b.Owed}
			if err != nil {
				ab = AccountBalance{Provider: "tencent", Error: err.Error()}
			}
			mu.Lock()
			balance["tencent"] = ab
			mu.Unlock()
		})
		run(func() {
			list, err := a.TencentServers(ctx, false)
			if err != nil {
				failed("腾讯云服务器", err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			for _, s := range list.Servers {
				t, err := time.Parse(time.RFC3339, s.ExpiredTime)
				if s.ChargeType != "PREPAID" || err != nil {
					continue
				}
				servers = append(servers, DueServer{Provider: "tencent", ID: s.ID, Region: s.Region, Name: s.Name, Kind: kindName[s.Kind],
					Expires: t.In(time.Local).Format("2006-01-02"), Days: daysTo(t), AutoRenew: s.RenewFlag == tencent.RenewAuto,
					CanSwitch: true, ServerID: s.ServerID})
			}
		})
		run(func() {
			list, err := tc.RegisteredDomains(ctx)
			if err != nil {
				failed("腾讯云域名", err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			for _, d := range list {
				domains = append(domains, DueDomain{Provider: "tencent", Name: d.Name, Expires: d.Expires, AutoRenew: d.AutoRenew})
			}
		})
	}
	if ac != nil {
		run(func() {
			b, err := ac.Balance(ctx)
			ab := AccountBalance{Provider: "aliyun", Available: b.Available}
			if b.Available < 0 {
				ab.Owed = -b.Available // what is spent beyond the balance is owed
			}
			if err != nil {
				ab = AccountBalance{Provider: "aliyun", Error: err.Error()}
			}
			mu.Lock()
			balance["aliyun"] = ab
			mu.Unlock()
		})
		run(func() {
			list, err := a.AliyunServers(ctx, false)
			if err != nil {
				failed("阿里云服务器", err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			for _, s := range list.Servers {
				t, err := time.Parse(time.RFC3339, s.ExpiredTime)
				if s.ChargeType != "PREPAID" || err != nil {
					continue
				}
				servers = append(servers, DueServer{Provider: "aliyun", ID: s.ID, Region: s.Region, Name: s.Name, Kind: aliKindName[s.Kind],
					Expires: t.In(time.Local).Format("2006-01-02"), Days: daysTo(t), AutoRenew: s.AutoRenew != nil && *s.AutoRenew,
					CanSwitch: s.Kind == aliyun.KindECS, ServerID: s.ServerID})
			}
		})
		run(func() {
			list, err := ac.RegisteredDomains(ctx)
			if err != nil {
				failed("阿里云域名", err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			for _, d := range list {
				domains = append(domains, DueDomain{Provider: "aliyun", Name: d.Name, Expires: d.Expires})
			}
		})
	}
	wg.Wait()

	for _, p := range []string{"tencent", "aliyun"} {
		if b, ok := balance[p]; ok {
			v.Balances = append(v.Balances, b)
		}
	}
	broke := func(provider string) bool {
		b, ok := balance[provider]
		return ok && b.Error == "" && b.Available <= 0
	}
	for _, s := range servers {
		s.Level, s.Note = dueLevel(s.Days, serverSoonDays, s.AutoRenew, broke(s.Provider))
		if s.Name == "" {
			s.Name = s.ID
		}
		v.Servers = append(v.Servers, s)
	}
	for _, d := range domains {
		t, err := time.ParseInLocation("2006-01-02", d.Expires, time.Local)
		if err != nil {
			continue
		}
		d.Days = daysTo(t)
		d.Level, d.Note = dueLevel(d.Days, domainSoonDays, d.AutoRenew, broke(d.Provider))
		if d.Provider == "aliyun" && d.Level != "ok" && d.Days >= 0 {
			d.Note = "是否自动续费请在阿里云控制台确认" // the list does not say
		}
		v.Domains = append(v.Domains, d)
	}
	sort.SliceStable(v.Servers, func(i, j int) bool { return v.Servers[i].Days < v.Servers[j].Days })
	sort.SliceStable(v.Domains, func(i, j int) bool { return v.Domains[i].Days < v.Domains[j].Days })
	sort.Strings(v.Errors)
	return v, nil
}

// CloudAccountPage is the account strip on the 云服务器 page.
func (a *App) CloudAccountPage(ctx context.Context, read PageRead) (AccountView, PageMeta, error) {
	return page(ctx, a, "page_account", read, a.CloudAccount)
}

// hasCloud says whether a cloud account is set up.
func (a *App) hasCloud() bool { return a.tencentClient() != nil || a.aliyunClient() != nil }

// accountTodo is what the cloud accounts need from the user, for 总览.
func accountTodo(v AccountView) []OverviewItem {
	var out []OverviewItem
	for _, b := range v.Balances {
		if b.Owed > 0 {
			out = append(out, OverviewItem{Level: "crit", Kind: "account", Action: "去看看",
				Title: fmt.Sprintf("%s账户欠费 ¥%.2f", providerName[b.Provider], b.Owed), Meta: "欠费后服务器和域名可能被停用"})
		}
	}
	for _, s := range v.Servers {
		if s.Level == "ok" {
			continue
		}
		out = append(out, OverviewItem{Level: s.Level, Kind: "account", Action: "去续费",
			Title: fmt.Sprintf("服务器 %s %s", s.Name, dueText(s.Days)), Meta: providerName[s.Provider] + " · " + s.Expires + " 到期 · " + s.Note})
	}
	for _, d := range v.Domains {
		if d.Level == "ok" {
			continue
		}
		out = append(out, OverviewItem{Level: d.Level, Kind: "account", Action: "去续费",
			Title: fmt.Sprintf("域名 %s %s", d.Name, dueText(d.Days)), Meta: providerName[d.Provider] + " · " + d.Expires + " 到期 · " + d.Note})
	}
	return out
}

// dueText says when something expires, in words.
func dueText(days int) string {
	switch {
	case days < 0:
		return "已过期"
	case days == 0:
		return "今天到期"
	}
	return fmt.Sprintf("%d 天后到期", days)
}

// accountAlert is the 续费和余额 section of an alert: what is new or has
// moved closer (a line comes again when its level changes, or after a
// week).
func accountAlert(v AccountView) (keys, lines []string) {
	for _, b := range v.Balances {
		if b.Owed > 0 {
			keys = append(keys, "owed:"+b.Provider)
			lines = append(lines, fmt.Sprintf("- %s账户欠费 ¥%.2f，请尽快充值，欠费后服务器和域名可能被停用", providerName[b.Provider], b.Owed))
		}
	}
	for _, s := range v.Servers {
		if s.Level == "ok" {
			continue
		}
		keys = append(keys, "renew:"+s.ID+":"+s.Expires+":"+s.Level)
		lines = append(lines, fmt.Sprintf("- 服务器 %s（%s%s）%s（%s），%s", s.Name, providerName[s.Provider], s.Kind, dueText(s.Days), s.Expires, s.Note))
	}
	for _, d := range v.Domains {
		if d.Level == "ok" {
			continue
		}
		keys = append(keys, "domain:"+d.Name+":"+d.Expires+":"+d.Level)
		lines = append(lines, fmt.Sprintf("- 域名 %s（%s）%s（%s），%s", d.Name, providerName[d.Provider], dueText(d.Days), d.Expires, d.Note))
	}
	return keys, lines
}

// accountReport is the daily report's 云账号 section.
func accountReport(v AccountView) string {
	lines := []string{"**云账号**"}
	for _, b := range v.Balances {
		switch {
		case b.Error != "":
			lines = append(lines, fmt.Sprintf("- %s余额读取失败：%s", providerName[b.Provider], b.Error))
		case b.Owed > 0:
			lines = append(lines, fmt.Sprintf("- %s欠费 ¥%.2f，请尽快充值", providerName[b.Provider], b.Owed))
		default:
			lines = append(lines, fmt.Sprintf("- %s余额 ¥%.2f", providerName[b.Provider], b.Available))
		}
	}
	_, due := accountAlert(AccountView{Servers: v.Servers, Domains: v.Domains})
	lines = append(lines, due...)
	auto, manual := 0, 0 // due within 30 days but not urgent
	for _, s := range v.Servers {
		if s.Level == "ok" && s.Days <= domainSoonDays {
			if s.AutoRenew {
				auto++
			} else {
				manual++
			}
		}
	}
	for _, d := range v.Domains {
		if d.Level == "ok" && d.Days <= domainSoonDays {
			auto++ // only a domain that renews itself is not urgent this close
		}
	}
	if manual > 0 {
		lines = append(lines, fmt.Sprintf("- 另有 %d 台服务器 30 天内到期，需要手动续费", manual))
	}
	if auto > 0 {
		lines = append(lines, fmt.Sprintf("- 另有 %d 项 30 天内到期，会自动续费", auto))
	}
	if len(due) == 0 && auto+manual == 0 && (len(v.Servers) > 0 || len(v.Domains) > 0) {
		lines = append(lines, "- 服务器和域名 30 天内都不会到期")
	}
	return strings.Join(lines, "\n")
}

// toolCloudAccount is the AI's look at the cloud accounts.
func (a *App) toolCloudAccount(ctx context.Context, raw json.RawMessage) (string, error) {
	var arg struct {
		Refresh bool `json:"refresh"`
	}
	if err := parseArgs(raw, &arg); err != nil {
		return "", err
	}
	if !a.hasCloud() {
		return "还没有配置腾讯云或阿里云的密钥，请用户先在「设置 → 腾讯云 / 阿里云」里填写。", nil
	}
	read := PageWait
	if arg.Refresh {
		read = PageRefresh
	}
	v, _, err := a.CloudAccountPage(ctx, read)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("账户余额：\n")
	for _, x := range v.Balances {
		if x.Error != "" {
			fmt.Fprintf(&b, "- %s：读取失败（%s）\n", providerName[x.Provider], x.Error)
			continue
		}
		fmt.Fprintf(&b, "- %s：可用 ¥%.2f", providerName[x.Provider], x.Available)
		if x.Owed > 0 {
			fmt.Fprintf(&b, "，欠费 ¥%.2f", x.Owed)
		}
		b.WriteString("\n")
	}
	b.WriteString("\n包年包月的服务器（按到期时间）：\n")
	if len(v.Servers) == 0 {
		b.WriteString("- 没有\n")
	}
	for _, s := range v.Servers {
		fmt.Fprintf(&b, "- %s %s id=%s region=%s：%s 到期（%s），%s", providerName[s.Provider], s.Name, s.ID, s.Region, s.Expires,
			dueText(s.Days), renewWords(s.AutoRenew))
		if !s.CanSwitch {
			b.WriteString("（自动续费要在云控制台设置）")
		}
		if s.Note != "" && s.Level != "ok" {
			fmt.Fprintf(&b, " ⚠ %s", s.Note)
		}
		b.WriteString("\n")
	}
	b.WriteString("\n在云账号注册的域名（按到期时间）：\n")
	if len(v.Domains) == 0 {
		b.WriteString("- 没有（只在 DNSPod / 云解析托管、在别处注册的域名不在这里）\n")
	}
	for _, d := range v.Domains {
		fmt.Fprintf(&b, "- %s %s：%s 到期（%s）", providerName[d.Provider], d.Name, d.Expires, dueText(d.Days))
		if d.Provider == "tencent" {
			b.WriteString("，" + renewWords(d.AutoRenew))
		}
		if d.Note != "" && d.Level != "ok" {
			fmt.Fprintf(&b, " ⚠ %s", d.Note)
		}
		b.WriteString("\n")
	}
	for _, e := range v.Errors {
		fmt.Fprintf(&b, "\n读取失败：%s", e)
	}
	return b.String(), nil
}

// expiresText starts a sentence with when a server expires.
func expiresText(expired string) string {
	if t, err := time.Parse(time.RFC3339, expired); err == nil {
		return fmt.Sprintf("这台服务器 %s 到期（%s），", t.In(time.Local).Format("2006-01-02"), dueText(daysTo(t)))
	}
	return ""
}

func renewWords(auto bool) string {
	if auto {
		return "自动续费"
	}
	return "手动续费"
}
