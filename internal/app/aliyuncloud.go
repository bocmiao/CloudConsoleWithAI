package app

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bocmiao/CloudConsoleWithAI/internal/actions"
	"github.com/bocmiao/CloudConsoleWithAI/internal/aliyun"
	"github.com/bocmiao/CloudConsoleWithAI/internal/core"
	"github.com/bocmiao/CloudConsoleWithAI/internal/store"
	"github.com/bocmiao/CloudConsoleWithAI/internal/tencent"
)

// 阿里云: the AccessKey, and ECS and Simple Application Server instances on
// the 云服务器 page next to Tencent Cloud's, with the same checklists.

const (
	aliyunIDKey     = "aliyun/access_key_id"
	aliyunSecretKey = "aliyun/access_key_secret"
)

var (
	aliyunKeyRe      = regexp.MustCompile(`^[A-Za-z0-9]{16,40}$`)
	aliyunInstanceRe = regexp.MustCompile(`^(i-[a-z0-9]{8,30}|[0-9a-f]{32})$`)
)

// AliyunSettings says whether 阿里云 access is configured.
type AliyunSettings struct {
	Configured  bool   `json:"configured"`
	AccessKeyID string `json:"accessKeyId"` // masked
}

// Aliyun returns the 阿里云 settings.
func (a *App) Aliyun() AliyunSettings {
	id, err := a.Secrets.Get(aliyunIDKey)
	if err != nil || id == "" {
		return AliyunSettings{}
	}
	if v, err := a.Secrets.Get(aliyunSecretKey); err != nil || v == "" {
		return AliyunSettings{}
	}
	return AliyunSettings{Configured: true, AccessKeyID: mask(id)}
}

// SaveAliyun stores an AccessKey.
func (a *App) SaveAliyun(id, secret string) (AliyunSettings, error) {
	id, secret = strings.TrimSpace(id), strings.TrimSpace(secret)
	if !aliyunKeyRe.MatchString(id) {
		return a.Aliyun(), userErr("AccessKey ID 的格式不对（一般是 LTAI 开头的一串字母和数字）")
	}
	if len(secret) < 16 {
		return a.Aliyun(), userErr("请填写 AccessKey Secret")
	}
	if err := a.Secrets.Set(aliyunIDKey, id); err != nil {
		return a.Aliyun(), err
	}
	if err := a.Secrets.Set(aliyunSecretKey, secret); err != nil {
		return a.Aliyun(), err
	}
	a.aliCloud.mu.Lock()
	a.aliCloud.at = time.Time{}
	a.aliCloud.mu.Unlock()
	_ = a.Store.Audit("user", "settings.aliyun", mask(id), "")
	return a.Aliyun(), nil
}

// ClearAliyun removes the stored AccessKey.
func (a *App) ClearAliyun() AliyunSettings {
	_ = a.Secrets.Delete(aliyunIDKey)
	_ = a.Secrets.Delete(aliyunSecretKey)
	a.aliCloud.mu.Lock()
	a.aliCloud.at, a.aliCloud.list = time.Time{}, nil
	a.aliCloud.mu.Unlock()
	_ = a.Store.Audit("user", "settings.aliyun", "清除", "")
	return a.Aliyun()
}

// aliyunClient returns an API client, or nil when not configured.
func (a *App) aliyunClient() *aliyun.Client {
	id, err1 := a.Secrets.Get(aliyunIDKey)
	key, err2 := a.Secrets.Get(aliyunSecretKey)
	if err1 != nil || err2 != nil || id == "" || key == "" {
		return nil
	}
	c := aliyun.New(id, key)
	if a.AliyunCloudEndpoint != nil {
		c.EndpointFor = a.AliyunCloudEndpoint
	}
	return c
}

// TestAliyun checks the AccessKey against servers, DNS and CDN, saying
// which permission is missing when one is.
func (a *App) TestAliyun(ctx context.Context) (string, error) {
	c := a.aliyunClient()
	if c == nil {
		return "", userErr("请先填写 AccessKey ID 和 AccessKey Secret")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	var parts, problems []string
	list, errs := c.Servers(ctx)
	if len(list) > 0 || len(errs) == 0 {
		parts = append(parts, fmt.Sprintf("服务器：%d 台", len(list)))
	}
	for _, e := range errs {
		problems = append(problems, e.Error())
	}
	if d, err := c.Domains(ctx); err != nil {
		problems = append(problems, "云解析："+err.Error())
	} else {
		parts = append(parts, fmt.Sprintf("云解析：%d 个域名", len(d)))
	}
	if d, err := c.CDNDomains(ctx); err != nil {
		problems = append(problems, "CDN："+err.Error())
	} else {
		parts = append(parts, fmt.Sprintf("CDN：%d 个加速域名", len(d)))
	}
	if len(parts) == 0 {
		return "", userErr("%s", strings.Join(problems, "；"))
	}
	msg := strings.Join(parts, "，")
	if len(problems) > 0 {
		msg += "；" + strings.Join(problems, "；")
	}
	return msg, nil
}

// aliCache keeps the instance list for a few minutes, as for Tencent Cloud.
type aliCache struct {
	mu    sync.Mutex
	at    time.Time
	list  []aliyun.Server
	errs  []string
	stale atomic.Bool // a checklist step changed a server: list again
}

// AliServer is a 阿里云 instance, in the shape the 云服务器 page shows.
type AliServer struct {
	aliyun.Server
	Provider  string `json:"provider"`            // "aliyun"
	RenewFlag string `json:"renewFlag,omitempty"` // NOTIFY_AND_AUTO_RENEW when it renews itself
	ServerID  int64  `json:"serverId,omitempty"`  // the Miao Panel server at its public IP
}

// AliServers is the answer to "which servers do I have on 阿里云".
type AliServers struct {
	Servers   []AliServer `json:"servers"`
	Errors    []string    `json:"errors"`
	FetchedAt string      `json:"fetchedAt"`
}

func aliServerOf(s aliyun.Server, servers []store.Server) AliServer {
	v := AliServer{Server: s, Provider: "aliyun"}
	if v.PublicIPs == nil {
		v.PublicIPs = []string{}
	}
	if v.PrivateIPs == nil {
		v.PrivateIPs = []string{}
	}
	if s.AutoRenew != nil && *s.AutoRenew {
		v.RenewFlag = "NOTIFY_AND_AUTO_RENEW"
	}
	for _, sv := range servers {
		for _, ip := range s.PublicIPs {
			if sv.Host == ip {
				v.ServerID = sv.ID
				return v
			}
		}
	}
	return v
}

// AliyunServers lists ECS and Simple Application Server instances, from
// the cache unless refresh is set.
func (a *App) AliyunServers(ctx context.Context, refresh bool) (AliServers, error) {
	c := a.aliyunClient()
	if c == nil {
		return AliServers{}, userErr("还没有配置阿里云 AccessKey（设置 → 阿里云）")
	}
	a.aliCloud.mu.Lock()
	defer a.aliCloud.mu.Unlock()
	if a.aliCloud.stale.Swap(false) || refresh || a.aliCloud.at.IsZero() || time.Since(a.aliCloud.at) > cloudCacheTTL {
		ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		list, errs := c.Servers(ctx)
		if len(list) == 0 && len(errs) > 0 {
			return AliServers{}, userErr("%v", errs[0])
		}
		a.aliCloud.list, a.aliCloud.at, a.aliCloud.errs = list, time.Now(), nil
		for _, e := range errs {
			a.aliCloud.errs = append(a.aliCloud.errs, e.Error())
		}
	}
	servers, _ := a.Store.ListServers()
	out := AliServers{Errors: a.aliCloud.errs, FetchedAt: a.aliCloud.at.UTC().Format(time.RFC3339), Servers: []AliServer{}}
	if out.Errors == nil {
		out.Errors = []string{}
	}
	for _, s := range a.aliCloud.list {
		out.Servers = append(out.Servers, aliServerOf(s, servers))
	}
	return out, nil
}

// CloudSnapshot is a snapshot as the 云服务器 page shows it, for either
// provider.
type CloudSnapshot struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	State   string `json:"state"` // NORMAL, CREATING, FAILED
	Percent int    `json:"percent"`
	Created string `json:"created"`
	SizeGB  int    `json:"sizeGB,omitempty"`
}

// AliDetail is one 阿里云 instance on the 云服务器 page.
type AliDetail struct {
	Instance      AliServer       `json:"instance"`
	Firewall      []CloudRule     `json:"firewall"`
	Group         string          `json:"group,omitempty"` // ECS: the security group whose rules these are
	FirewallError string          `json:"firewallError,omitempty"`
	Snapshots     []CloudSnapshot `json:"snapshots"`
	SnapshotError string          `json:"snapshotError,omitempty"`
	Metrics       []CloudMetric   `json:"metrics"`
	MetricError   string          `json:"metricError,omitempty"`
}

var aliMetrics = []struct{ label, metric, unit string }{
	{"CPU 使用率", aliyun.MetricCPU, "%"},
	{"内存使用率", aliyun.MetricMemory, "%"},
	{"公网出带宽", aliyun.MetricNetOut, "Mbps"},
	{"公网入带宽", aliyun.MetricNetIn, "Mbps"},
}

// AliyunDetail reads one instance with its firewall, snapshots and the
// last day's monitoring.
func (a *App) AliyunDetail(ctx context.Context, region, id string) (AliDetail, error) {
	c := a.aliyunClient()
	if c == nil {
		return AliDetail{}, userErr("还没有配置阿里云 AccessKey（设置 → 阿里云）")
	}
	if !aliyunInstanceRe.MatchString(id) || !regionRe.MatchString(region) {
		return AliDetail{}, userErr("实例或地域不对")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	s, ok, err := c.Instance(ctx, region, id)
	if err != nil {
		return AliDetail{}, userErr("读取实例失败：%v", err)
	}
	if !ok {
		return AliDetail{}, userErr("在 %s 找不到实例 %s，可能已经释放", region, id)
	}
	if s.RegionName == "" {
		a.aliCloud.mu.Lock()
		for _, x := range a.aliCloud.list {
			if x.Region == s.Region && x.RegionName != "" {
				s.RegionName = x.RegionName
				break
			}
		}
		a.aliCloud.mu.Unlock()
	}
	servers, _ := a.Store.ListServers()
	v := AliDetail{Instance: aliServerOf(s, servers), Firewall: []CloudRule{}, Snapshots: []CloudSnapshot{}, Metrics: []CloudMetric{}}
	if s.Kind == aliyun.KindECS && len(s.SecurityGroups) > 0 {
		v.Group = s.SecurityGroups[0]
	}
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		rules, err := c.Firewall(ctx, s)
		if err != nil {
			v.FirewallError = err.Error()
			return
		}
		for _, r := range rules {
			port := r.Port
			if port == "" {
				port = "ALL"
			}
			v.Firewall = append(v.Firewall, CloudRule{Protocol: strings.ToUpper(r.Protocol), Port: port, Source: r.Source,
				Action: strings.ToUpper(r.Policy), Description: r.Description, Everyone: r.Source == "0.0.0.0/0" || r.Source == "::/0"})
		}
	}()
	go func() {
		defer wg.Done()
		snaps, err := c.Snapshots(ctx, s)
		if err != nil {
			v.SnapshotError = err.Error()
			return
		}
		for _, sn := range snaps {
			state := map[string]string{"ACCOMPLISHED": "NORMAL", "PROGRESSING": "CREATING"}[sn.State]
			if state == "" {
				state = sn.State
			}
			v.Snapshots = append(v.Snapshots, CloudSnapshot{ID: sn.ID, Name: sn.Name, State: state, Percent: sn.Progress, Created: sn.Created, SizeGB: sn.SizeGB})
		}
	}()
	go func() {
		defer wg.Done()
		end := time.Now()
		start := end.Add(-24 * time.Hour)
		var errs []string
		for _, w := range aliMetrics {
			pts, err := c.MonitorData(ctx, s, w.metric, 900, start, end)
			if err != nil {
				errs = append(errs, w.label+"："+err.Error())
				continue
			}
			tp := make([]tencent.Point, 0, len(pts))
			for _, p := range pts {
				val := p.V
				if w.unit == "Mbps" {
					val = p.V / 1e6
				}
				tp = append(tp, tencent.Point{T: p.T, V: val})
			}
			cm := CloudMetric{Label: w.label, Unit: w.unit, Points: downsample(tp, 96)}
			if cm.Points == nil {
				cm.Points = []tencent.Point{}
			}
			var sum float64
			for _, p := range tp {
				sum += p.V
				cm.Max = max(cm.Max, p.V)
			}
			if len(tp) > 0 {
				cm.Avg, cm.Latest = sum/float64(len(tp)), tp[len(tp)-1].V
			}
			v.Metrics = append(v.Metrics, cm)
		}
		v.MetricError = strings.Join(errs, "；")
	}()
	wg.Wait()
	return v, nil
}

var aliKindName = map[string]string{aliyun.KindECS: "云服务器 ECS", aliyun.KindSWAS: "轻量应用服务器"}

// ProposeAliyun turns a change on the 云服务器 page into a checklist.
func (a *App) ProposeAliyun(ctx context.Context, req CloudRequest) (PlanView, error) {
	c := a.aliyunClient()
	if c == nil {
		return PlanView{}, userErr("还没有配置阿里云 AccessKey（设置 → 阿里云）")
	}
	if !aliyunInstanceRe.MatchString(req.Instance) || !regionRe.MatchString(req.Region) {
		return PlanView{}, userErr("实例或地域不对")
	}
	lctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	s, ok, err := c.Instance(lctx, req.Region, req.Instance)
	cancel()
	if err != nil {
		return PlanView{}, userErr("读取实例失败：%v", err)
	}
	if !ok {
		return PlanView{}, userErr("在 %s 找不到实例 %s", req.Region, req.Instance)
	}
	name := s.Name
	if name == "" {
		name = s.ID
	}
	kind := aliKindName[s.Kind]
	params := map[string]any{"instance": s.ID, "region": s.Region}
	var capability, title, summary, reason string
	var first []core.Step // steps before the change itself
	switch req.Op {
	case "rollback":
		lctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		list, err := c.Snapshots(lctx, s)
		cancel()
		if err != nil {
			return PlanView{}, userErr("读取快照失败：%v", err)
		}
		var sn *aliyun.Snapshot
		for i := range list {
			if list[i].ID == req.Snapshot {
				sn = &list[i]
			}
		}
		if sn == nil {
			return PlanView{}, userErr("%s 没有快照 %s", name, req.Snapshot)
		}
		if sn.State != "ACCOMPLISHED" {
			return PlanView{}, userErr("快照 %s 还没有完成，等它完成再回滚", sn.ID)
		}
		capability, title = "aliyun.snapshot.rollback", "回滚到快照："+name
		params["snapshot"] = sn.ID
		summary = fmt.Sprintf("把 %s 的系统盘回滚到快照 %s", name, snapLabel(sn.Name, sn.ID))
		if sn.Created != "" {
			summary += "（" + snapTime(sn.Created) + " 创建）"
		}
		first = []core.Step{{Capability: "aliyun.snapshot.create", Summary: "先给现在的系统盘做一个快照，回滚后想反悔可以再回滚到它",
			Params: map[string]any{"instance": s.ID, "region": s.Region, "name": rollbackSnapName()}}}
		reason = rollbackReason("阿里云", s.Kind == aliyun.KindECS)
	case "start":
		capability, title = "aliyun.server.start", "启动服务器："+name
		summary = "启动阿里云" + kind + " " + name
		reason = "开机后网站和服务会按服务器上的设置自动启动，一般一两分钟内可以访问。"
	case "stop":
		capability, title = "aliyun.server.stop", "关闭服务器："+name
		summary = "关闭阿里云" + kind + " " + name
		reason = "关机后这台服务器上的网站和服务都会停止，直到再次启动；数据不会丢失。"
		if s.Kind == aliyun.KindECS && s.ChargeType == "POSTPAID" {
			reason += "这台是按量付费的 ECS，关机后仍然计费（这样公网 IP 不变，一定能再开机）。"
		}
	case "reboot":
		capability, title = "aliyun.server.reboot", "重启服务器："+name
		summary = "重启阿里云" + kind + " " + name
		reason = "重启期间网站和服务会中断一两分钟。服务器卡死、SSH 连不上时用它；只是某个服务有问题的话，重启那个服务就够了。"
	case "snapshot":
		capability, title = "aliyun.snapshot.create", "创建快照："+name
		if n := strings.TrimSpace(req.Name); n != "" {
			params["name"] = n
		}
		summary = "给 " + name + " 的系统盘创建快照"
		reason = "快照是系统盘的整盘备份，大改之前做一个，出问题可以在这里一键回滚到这个时刻。不影响运行。"
		if s.Kind == aliyun.KindECS {
			reason += "ECS 的快照按容量收费。"
		}
	case "renew_on", "renew_off":
		if s.ChargeType != "PREPAID" {
			return PlanView{}, userErr("%s 是按量付费的，没有续费这回事", name)
		}
		if s.Kind != aliyun.KindECS {
			return PlanView{}, userErr("阿里云轻量应用服务器的自动续费请在阿里云控制台设置")
		}
		capability = "aliyun.renew.set"
		if req.Op == "renew_on" {
			params["auto"] = "on"
			title, summary = "开启自动续费："+name, "把 "+name+" 改为到期前自动续费"
			reason = fmt.Sprintf("%s开启后到期前会自动从阿里云账户余额扣费续费一个月，免得忘了续费被停机、数据被释放。请确保账户里有足够余额。", expiresText(s.ExpiredTime))
		} else {
			params["auto"] = "off"
			title, summary = "关闭自动续费："+name, "把 "+name+" 改为手动续费"
			reason = "关闭后到期前需要自己去续费，否则到期会停机，一段时间后数据被释放。不打算继续用这台服务器时再关闭。"
		}
	case "firewall_open", "firewall_close":
		port := strings.TrimSpace(req.Port)
		if port == "" {
			return PlanView{}, userErr("请填写端口")
		}
		params["port"] = port
		if p := strings.ToUpper(strings.TrimSpace(req.Protocol)); p != "" {
			params["protocol"] = p
		}
		cidr := strings.TrimSpace(req.CIDR)
		if cidr == "" {
			cidr = "0.0.0.0/0"
		}
		params["cidr"] = cidr
		who := "所有人"
		if cidr != "0.0.0.0/0" {
			who = cidr
		}
		where := "防火墙"
		if s.Kind == aliyun.KindECS {
			where = "安全组"
		}
		if req.Op == "firewall_open" {
			capability, title = "aliyun.firewall.open", fmt.Sprintf("放行端口 %s：%s", port, name)
			if d := strings.TrimSpace(req.Description); d != "" {
				params["description"] = d
			}
			summary = fmt.Sprintf("在 %s 的%s里放行 %s 端口，允许%s访问", name, where, port, who)
			reason = "放行后外面才能访问这个端口上的服务。只给自己用的端口（数据库、面板）最好只允许自己的 IP。"
			if s.Kind == aliyun.KindECS {
				reason += "安全组可能同时绑定了其他服务器，规则对它们也生效。"
			}
		} else {
			if p := actions.LoginPortIn(port); p != 0 {
				return PlanView{}, userErr("端口 %d 是远程登录用的，关掉会连不上服务器，不能在这里关闭", p)
			}
			capability, title = "aliyun.firewall.close", fmt.Sprintf("关闭端口 %s：%s", port, name)
			summary = fmt.Sprintf("在 %s 的%s里删除 %s 端口（来源 %s）的放行规则", name, where, port, cidr)
			reason = "关闭后外面访问不到这个端口上的服务，服务本身还在运行。"
		}
	default:
		return PlanView{}, userErr("不支持的操作 %q", req.Op)
	}
	if _, err := actions.Resolve(capability, params, noServer); err != nil {
		return PlanView{}, userErr("%v", err)
	}
	servers, _ := a.Store.ListServers()
	steps := append(first, core.Step{Capability: capability, Summary: summary, Params: params})
	p, _, err := a.proposePlan(ctx, "user", aliServerOf(s, servers).ServerID, title, reason, steps)
	if err != nil {
		return PlanView{}, err
	}
	return a.Plan(p.ID)
}

// toolAliyunServers is the AI's look at the 阿里云 servers, or at one of
// them in detail.
func (a *App) toolAliyunServers(ctx context.Context, raw json.RawMessage) (string, error) {
	var in struct {
		Instance string `json:"instance"`
		Region   string `json:"region"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &in); err != nil {
			return "", err
		}
	}
	if a.aliyunClient() == nil {
		return "", userErr("还没有配置阿里云 AccessKey，请让用户到「设置 → 阿里云」填写")
	}
	var b strings.Builder
	if in.Instance == "" {
		list, err := a.AliyunServers(ctx, false)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "阿里云服务器 %d 台：\n", len(list.Servers))
		for _, s := range list.Servers {
			fmt.Fprintf(&b, "- %s（%s）id=%s region=%s（%s）状态=%s 配置=%d 核 %gGB 系统盘 %dGB 带宽 %dMbps 公网 IP=%s 计费=%s",
				s.Name, aliKindName[s.Kind], s.ID, s.Region, s.RegionName, s.State, s.CPU, s.MemoryGB, s.DiskGB, s.BandwidthMbps,
				strings.Join(s.PublicIPs, ","), s.ChargeType)
			if s.ExpiredTime != "" {
				fmt.Fprintf(&b, " 到期=%s%s", s.ExpiredTime[:min(10, len(s.ExpiredTime))], daysLeft(s.ExpiredTime))
			}
			if s.RenewFlag != "" {
				b.WriteString(" 自动续费")
			}
			if s.TrafficTotal > 0 {
				fmt.Fprintf(&b, " 流量包 %s/%s", gb(s.TrafficUsed), gb(s.TrafficTotal))
			}
			if s.ServerID != 0 {
				fmt.Fprintf(&b, " Miao Panel 服务器编号=%d", s.ServerID)
			}
			b.WriteString("\n")
		}
		for _, e := range list.Errors {
			fmt.Fprintf(&b, "读取出错：%s\n", e)
		}
		return b.String(), nil
	}
	d, err := a.AliyunDetail(ctx, in.Region, in.Instance)
	if err != nil {
		return "", err
	}
	s := d.Instance
	fmt.Fprintf(&b, "阿里云%s %s（%s，%s）状态 %s\n", aliKindName[s.Kind], s.Name, s.ID, s.Region, s.State)
	if d.Group != "" {
		fmt.Fprintf(&b, "防火墙是安全组 %s（可能还绑定了其他服务器）\n", d.Group)
	}
	if d.FirewallError != "" {
		fmt.Fprintf(&b, "防火墙读取失败：%s\n", d.FirewallError)
	}
	for _, r := range d.Firewall {
		fmt.Fprintf(&b, "- 放行 %s %s 来源 %s %s %s\n", r.Protocol, r.Port, r.Source, r.Action, r.Description)
	}
	for _, sn := range d.Snapshots {
		fmt.Fprintf(&b, "快照 %s %s %s %s\n", sn.Name, sn.ID, sn.Created, sn.State)
	}
	for _, m := range d.Metrics {
		fmt.Fprintf(&b, "%s（24 小时）：最新 %.1f%s，平均 %.1f，最高 %.1f\n", m.Label, m.Latest, m.Unit, m.Avg, m.Max)
	}
	if d.MetricError != "" {
		fmt.Fprintf(&b, "监控读取失败：%s\n", d.MetricError)
	}
	return b.String(), nil
}

// serverNameFor is the name a step is logged under: its server's, or for
// a step without one, the cloud it changes.
func serverNameFor(sv store.Server, capability string) string {
	if sv.ID == 0 && strings.HasPrefix(capability, "aliyun.") {
		return "阿里云"
	}
	return sv.Name
}

// rollbackSnapName names the snapshot taken before a rollback: both clouds
// take letters, digits and dashes, starting with a letter.
func rollbackSnapName() string { return "before-rollback-" + time.Now().Format("20060102-1504") }

func snapLabel(name, id string) string {
	if name == "" || name == id {
		return id
	}
	return name + "（" + id + "）"
}

// rollbackReason explains a rollback checklist.
func rollbackReason(cloud string, billed bool) string {
	r := "回滚会把整个系统盘换成快照时的样子：之后改过的配置、上传的文件、数据库里新写入的数据都会丢失（数据盘不受影响）。" +
		"服务器会先关机，回滚要几分钟，原来开着的回滚后自动开机。清单第一步先给现在的系统盘做一个快照，回滚后想反悔可以再回滚到它；" +
		"快照数量到了上限时第一步会失败、不会继续回滚，可以先在" + cloud + "控制台删掉不要的快照，或者确定不需要时取消勾选第一步。"
	if billed {
		r += "这台服务器的快照按容量收费。"
	}
	return r
}

// snapTime shows when a snapshot was made, to the minute: the clouds
// write RFC 3339 or "2006-01-02 15:04:05".
func snapTime(s string) string {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.In(time.Local).Format("2006-01-02 15:04")
	}
	return s[:min(16, len(s))]
}
