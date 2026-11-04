package app

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/bocmiao/CloudConsoleWithAI/internal/actions"
	"github.com/bocmiao/CloudConsoleWithAI/internal/core"
	"github.com/bocmiao/CloudConsoleWithAI/internal/store"
	"github.com/bocmiao/CloudConsoleWithAI/internal/tencent"
)

// The 云服务器 page: one Tencent Cloud instance with its firewall, its
// system disk's snapshots and the last day's monitoring, and checklists
// to start, stop or reboot it, take a snapshot or change the firewall.

// matchServer finds the server in Miao Panel that is this instance: the
// one reached through it with the automation agent, or at its public IP.
func matchServer(s tencent.Server, servers []store.Server) int64 {
	for _, sv := range servers {
		if sv.InstanceID != "" && sv.InstanceID == s.ID {
			return sv.ID
		}
	}
	for _, sv := range servers {
		for _, ip := range s.PublicIPs {
			if sv.Host == ip {
				return sv.ID
			}
		}
	}
	return 0
}

// CloudRule is one inbound firewall rule, as shown on the page.
type CloudRule struct {
	Protocol    string `json:"protocol"`
	Port        string `json:"port"`
	Source      string `json:"source"`
	Action      string `json:"action"`
	Description string `json:"description,omitempty"`
	// Everyone is a rule letting the whole internet in.
	Everyone bool `json:"everyone"`
}

// CloudMetric is one monitoring series over the last day.
type CloudMetric struct {
	Label  string          `json:"label"`
	Unit   string          `json:"unit"`
	Points []tencent.Point `json:"points"`
	Latest float64         `json:"latest"`
	Avg    float64         `json:"avg"`
	Max    float64         `json:"max"`
}

// CloudDetail is one instance on the 云服务器 page. A part that cannot be
// read says why in its error and the rest is still shown.
type CloudDetail struct {
	Instance      CloudServer        `json:"instance"`
	Firewall      []CloudRule        `json:"firewall"`
	Group         string             `json:"group,omitempty"` // CVM: the security group whose rules these are
	FirewallError string             `json:"firewallError,omitempty"`
	Snapshots     []tencent.Snapshot `json:"snapshots"`
	SnapshotError string             `json:"snapshotError,omitempty"`
	Metrics       []CloudMetric      `json:"metrics"`
	MetricError   string             `json:"metricError,omitempty"`
}

// CloudDetail reads one instance and what belongs to it, at once.
func (a *App) CloudDetail(ctx context.Context, region, id string) (CloudDetail, error) {
	c := a.tencentClient()
	if c == nil {
		return CloudDetail{}, userErr("还没有配置腾讯云密钥（设置 → 腾讯云）")
	}
	if !instanceRe.MatchString(id) || !regionRe.MatchString(region) {
		return CloudDetail{}, userErr("实例或地域不对")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	s, ok, err := c.Instance(ctx, region, id)
	if err != nil {
		return CloudDetail{}, userErr("读取实例失败：%v", err)
	}
	if !ok {
		return CloudDetail{}, userErr("在 %s 找不到实例 %s，可能已经退还或删除", region, id)
	}
	// One instance comes without its region's name; the list has it.
	if s.RegionName == "" {
		if l, err := a.TencentServers(ctx, false); err == nil {
			for _, x := range l.Servers {
				if x.Region == s.Region && x.RegionName != "" {
					s.RegionName = x.RegionName
					break
				}
			}
		}
	}
	servers, _ := a.Store.ListServers()
	v := CloudDetail{Instance: CloudServer{Server: s, ServerID: matchServer(s, servers)},
		Firewall: []CloudRule{}, Snapshots: []tencent.Snapshot{}, Metrics: []CloudMetric{}}

	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		var rules []tencent.FirewallRule
		var err error
		switch {
		case s.Kind == tencent.Lighthouse:
			rules, err = c.LighthouseFirewall(ctx, s.Region, s.ID)
		case len(s.Groups) > 0:
			v.Group = s.Groups[0]
			rules, err = c.SecurityGroupIngress(ctx, s.Region, v.Group)
		default:
			v.FirewallError = "这台云服务器没有绑定安全组"
			return
		}
		if err != nil {
			v.FirewallError = err.Error()
			return
		}
		for _, r := range rules {
			src := r.Source()
			v.Firewall = append(v.Firewall, CloudRule{Protocol: r.Protocol, Port: r.Port, Source: src, Action: r.Action,
				Description: r.Description, Everyone: src == "0.0.0.0/0" || src == "::/0"})
		}
	}()
	go func() {
		defer wg.Done()
		snaps, err := c.Snapshots(ctx, s.Region, s)
		if err != nil {
			v.SnapshotError = err.Error()
			return
		}
		v.Snapshots = append(v.Snapshots, snaps...)
	}()
	go func() {
		defer wg.Done()
		v.Metrics, v.MetricError = cloudMetrics(ctx, c, s)
	}()
	wg.Wait()
	return v, nil
}

// cloudMetrics reads the last day of CPU, memory and bandwidth.
func cloudMetrics(ctx context.Context, c *tencent.Client, s tencent.Server) ([]CloudMetric, string) {
	out := []CloudMetric{}
	ns := tencent.Namespace(s.Kind)
	metrics, err := c.Metrics(ctx, s.Region, ns)
	if err != nil {
		return out, err.Error()
	}
	end := time.Now()
	start := end.Add(-24 * time.Hour)
	var errs []string
	for _, w := range monitorWanted {
		var m *tencent.Metric
		for i := range metrics {
			for _, n := range w.names {
				if strings.EqualFold(metrics[i].Name, n) {
					m = &metrics[i]
				}
			}
		}
		if m == nil {
			continue
		}
		pts, err := c.MonitorData(ctx, s.Region, ns, m.Name, s.ID, pickPeriod(m.Periods, end.Sub(start)), tencent.TimeArg(start), tencent.TimeArg(end))
		if err != nil {
			errs = append(errs, w.label+"："+err.Error())
			continue
		}
		cm := CloudMetric{Label: w.label, Unit: m.Unit, Points: downsample(pts, 96)}
		if cm.Points == nil {
			cm.Points = []tencent.Point{}
		}
		var sum float64
		for _, p := range pts {
			sum += p.V
			cm.Max = max(cm.Max, p.V)
		}
		if len(pts) > 0 {
			cm.Avg = sum / float64(len(pts))
			cm.Latest = pts[len(pts)-1].V
		}
		out = append(out, cm)
	}
	return out, strings.Join(errs, "；")
}

// CloudRequest is a change asked for on the 云服务器 page.
type CloudRequest struct {
	Instance string `json:"instance"`
	Region   string `json:"region"`
	Op       string `json:"op"` // start, stop, reboot, snapshot, rollback, firewall_open, firewall_close, renew_on, renew_off
	// For a snapshot, or the snapshot to roll back to.
	Name     string `json:"name,omitempty"`
	Snapshot string `json:"snapshot,omitempty"`
	// For a firewall rule.
	Port        string `json:"port,omitempty"`
	Protocol    string `json:"protocol,omitempty"`
	CIDR        string `json:"cidr,omitempty"`
	Description string `json:"description,omitempty"`
}

// ProposeCloud turns a change on the 云服务器 page into a checklist.
func (a *App) ProposeCloud(ctx context.Context, req CloudRequest) (PlanView, error) {
	c := a.tencentClient()
	if c == nil {
		return PlanView{}, userErr("还没有配置腾讯云密钥（设置 → 腾讯云）")
	}
	if !instanceRe.MatchString(req.Instance) || !regionRe.MatchString(req.Region) {
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
	params := map[string]any{"instance": s.ID, "region": s.Region}
	var capability, title, summary, reason string
	var first []core.Step // steps before the change itself
	switch req.Op {
	case "rollback":
		lctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		list, err := c.Snapshots(lctx, s.Region, s)
		cancel()
		if err != nil {
			return PlanView{}, userErr("读取快照失败：%v", err)
		}
		var sn *tencent.Snapshot
		for i := range list {
			if list[i].ID == req.Snapshot {
				sn = &list[i]
			}
		}
		if sn == nil {
			return PlanView{}, userErr("%s 没有快照 %s", name, req.Snapshot)
		}
		if sn.State != "NORMAL" {
			return PlanView{}, userErr("快照 %s 还不能用（%s），等它完成再回滚", sn.ID, sn.State)
		}
		capability, title = "cloud.snapshot.rollback", "回滚到快照："+name
		params["snapshot"] = sn.ID
		summary = fmt.Sprintf("把 %s 的系统盘回滚到快照 %s", name, snapLabel(sn.Name, sn.ID))
		if sn.Created != "" {
			summary += "（" + snapTime(sn.Created) + " 创建）"
		}
		first = []core.Step{{Capability: "cloud.snapshot.create", Summary: "先给现在的系统盘做一个快照，回滚后想反悔可以再回滚到它",
			Params: map[string]any{"instance": s.ID, "region": s.Region, "name": rollbackSnapName()}}}
		reason = rollbackReason("腾讯云", s.Kind == tencent.CVM)
	case "start":
		capability, title = "cloud.server.start", "启动服务器："+name
		summary = "启动腾讯云" + kindName[s.Kind] + " " + name
		reason = "开机后网站和服务会按服务器上的设置自动启动，一般一两分钟内可以访问。"
	case "stop":
		capability, title = "cloud.server.stop", "关闭服务器："+name
		summary = "关闭腾讯云" + kindName[s.Kind] + " " + name
		reason = "关机后这台服务器上的网站和服务都会停止，直到再次启动；数据不会丢失。按量计费的云服务器关机后仍可能继续收取硬盘等费用。"
	case "reboot":
		capability, title = "cloud.server.reboot", "重启服务器："+name
		summary = "重启腾讯云" + kindName[s.Kind] + " " + name
		reason = "重启期间网站和服务会中断一两分钟。服务器卡死、SSH 连不上时用它；只是某个服务有问题的话，重启那个服务就够了。"
	case "snapshot":
		capability, title = "cloud.snapshot.create", "创建快照："+name
		if n := strings.TrimSpace(req.Name); n != "" {
			params["name"] = n
		}
		summary = "给 " + name + " 的系统盘创建快照"
		reason = "快照是系统盘的整盘备份，大改之前做一个，出问题可以在这里一键回滚到这个时刻。不影响运行。"
		if s.Kind == tencent.CVM {
			reason += "云服务器的快照按容量收费。"
		}
	case "renew_on", "renew_off":
		if s.ChargeType != "PREPAID" {
			return PlanView{}, userErr("%s 是按量计费的，没有续费这回事", name)
		}
		capability = "cloud.renew.set"
		if req.Op == "renew_on" {
			params["auto"] = "on"
			title, summary = "开启自动续费："+name, "把 "+name+" 改为到期前自动续费"
			reason = fmt.Sprintf("%s开启后到期前会自动从腾讯云账户余额扣费续费一个月，免得忘了续费被停机、数据被回收。请确保账户里有足够余额。", expiresText(s.ExpiredTime))
		} else {
			params["auto"] = "off"
			title, summary = "关闭自动续费："+name, "把 "+name+" 改为手动续费"
			reason = "关闭后到期前需要自己去续费，否则到期会停机，一段时间后数据被回收。不打算继续用这台服务器时再关闭。"
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
		if s.Kind == tencent.CVM {
			where = "安全组"
		}
		if req.Op == "firewall_open" {
			capability, title = "cloud.firewall.open", fmt.Sprintf("放行端口 %s：%s", port, name)
			if d := strings.TrimSpace(req.Description); d != "" {
				params["description"] = d
			}
			summary = fmt.Sprintf("在 %s 的%s里放行 %s 端口，允许%s访问", name, where, port, who)
			reason = "放行后外面才能访问这个端口上的服务。只给自己用的端口（数据库、面板）最好只允许自己的 IP。"
			if s.Kind == tencent.CVM {
				reason += "安全组可能同时绑定了其他服务器，规则对它们也生效。"
			}
		} else {
			if p := actions.LoginPortIn(port); p != 0 {
				return PlanView{}, userErr("端口 %d 是远程登录用的，关掉会连不上服务器，不能在这里关闭", p)
			}
			capability, title = "cloud.firewall.close", fmt.Sprintf("关闭端口 %s：%s", port, name)
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
	p, _, err := a.proposePlan(ctx, "user", matchServer(s, servers), title, reason, steps)
	if err != nil {
		return PlanView{}, err
	}
	return a.Plan(p.ID)
}
